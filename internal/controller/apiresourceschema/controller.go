/*
Copyright 2025 The KCP Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package apiresourceschema

import (
	"context"
	"fmt"
	"reflect"

	"github.com/kcp-dev/logicalcluster/v3"
	"go.uber.org/zap"

	"github.com/kcp-dev/api-syncagent/internal/controllerutil/predicate"
	"github.com/kcp-dev/api-syncagent/internal/crypto"
	"github.com/kcp-dev/api-syncagent/internal/discovery"
	"github.com/kcp-dev/api-syncagent/internal/projection"
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	kcpdevv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/kontext"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	ControllerName = "syncagent-apiresourceschema"
)

type Reconciler struct {
	localClient ctrlruntimeclient.Client
	kcpClient   ctrlruntimeclient.Client
	restConfig  *rest.Config
	log         *zap.SugaredLogger
	recorder    record.EventRecorder
	lcName      logicalcluster.Name
	agentName   string
}

// Add creates a new controller and adds it to the given manager.
func Add(
	mgr manager.Manager,
	kcpCluster cluster.Cluster,
	lcName logicalcluster.Name,
	log *zap.SugaredLogger,
	numWorkers int,
	agentName string,
	prFilter labels.Selector,
) error {
	reconciler := &Reconciler{
		localClient: mgr.GetClient(),
		kcpClient:   kcpCluster.GetClient(),
		restConfig:  mgr.GetConfig(),
		lcName:      lcName,
		log:         log.Named(ControllerName),
		recorder:    mgr.GetEventRecorderFor(ControllerName),
		agentName:   agentName,
	}

	_, err := builder.ControllerManagedBy(mgr).
		Named(ControllerName).
		WithOptions(controller.Options{MaxConcurrentReconciles: numWorkers}).
		// Watch for changes to PublishedResources on the local service cluster
		For(&syncagentv1alpha1.PublishedResource{}, builder.WithPredicates(predicate.ByLabels(prFilter))).
		Watches(&apiextensionsv1.CustomResourceDefinition{}, handler.TypedEnqueueRequestsFromMapFunc(reconciler.enqueueMatchingPublishedResources)).
		Build(reconciler)

	return err
}

func (r *Reconciler) enqueueMatchingPublishedResources(ctx context.Context, obj ctrlruntimeclient.Object) []reconcile.Request {
	crd := obj.(*apiextensionsv1.CustomResourceDefinition)

	pubResources := &syncagentv1alpha1.PublishedResourceList{}
	if err := r.localClient.List(ctx, pubResources); err != nil {
		runtime.HandleError(err)
		return nil
	}

	var requests []reconcile.Request
	for _, pr := range pubResources.Items {
		if pr.Spec.Resource.APIGroup == crd.Spec.Group && pr.Spec.Resource.Kind == crd.Spec.Names.Kind {
			requests = append(requests, reconcile.Request{
				NamespacedName: ctrlruntimeclient.ObjectKeyFromObject(&pr),
			})
		}
	}

	return requests
}

func (r *Reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log := r.log.With("publishedresource", request)
	log.Debug("Processing")

	pubResource := &syncagentv1alpha1.PublishedResource{}
	if err := r.localClient.Get(ctx, request.NamespacedName, pubResource); err != nil {
		return reconcile.Result{}, ctrlruntimeclient.IgnoreNotFound(err)
	}

	// There is no special cleanup. When a PublishedResource is deleted, the
	// APIResourceSchema in kcp should remain, otherwise we risk deleting all
	// users' data just because a service admin might temporarily accidentally
	// delete the PublishedResource.
	if pubResource.DeletionTimestamp != nil {
		return reconcile.Result{}, nil
	}

	result, err := r.reconcile(ctx, log, pubResource)
	if err != nil {
		r.recorder.Event(pubResource, corev1.EventTypeWarning, "ReconcilingError", err.Error())
	}
	if result == nil {
		result = &reconcile.Result{}
	}

	return *result, err
}

func (r *Reconciler) reconcile(ctx context.Context, log *zap.SugaredLogger, pubResource *syncagentv1alpha1.PublishedResource) (*reconcile.Result, error) {
	// find the resource that the PublishedResource is referring to
	localGK := projection.PublishedResourceSourceGK(pubResource)

	client, err := discovery.NewClient(r.restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create discovery client: %w", err)
	}

	// fetch the original, full CRD from the cluster
	crd, err := client.RetrieveCRD(ctx, localGK)
	if err != nil {
		return nil, fmt.Errorf("failed to discover resource defined in PublishedResource: %w", err)
	}

	// project the CRD (i.e. strip unwanted versions, rename values etc.)
	projectedCRD, err := projection.ProjectCRD(crd, pubResource)
	if err != nil {
		return nil, fmt.Errorf("failed to apply projection rules: %w", err)
	}

	// generate a unique name for this exact state of the CRD
	arsName := r.getAPIResourceSchemaName(projectedCRD)

	// ensure ARS exists (don't try to reconcile it, it's basically entirely immutable)
	wsCtx := kontext.WithCluster(ctx, r.lcName)
	ars := &kcpdevv1alpha1.APIResourceSchema{}
	err = r.kcpClient.Get(wsCtx, types.NamespacedName{Name: arsName}, ars, &ctrlruntimeclient.GetOptions{})

	if apierrors.IsNotFound(err) {
		if err := r.createAPIResourceSchema(wsCtx, log, projectedCRD, arsName); err != nil {
			return nil, fmt.Errorf("failed to create APIResourceSchema: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("failed to check for APIResourceSchema: %w", err)
	}

	// update Status with ARS name
	if pubResource.Status.ResourceSchemaName != arsName {
		original := pubResource.DeepCopy()
		pubResource.Status.ResourceSchemaName = arsName

		if !reflect.DeepEqual(original, pubResource) {
			log.Info("Patching PublishedResource status…")
			if err := r.localClient.Status().Patch(ctx, pubResource, ctrlruntimeclient.MergeFrom(original)); err != nil {
				return nil, fmt.Errorf("failed to update PublishedResource status: %w", err)
			}
		}
	}

	return nil, nil
}

func (r *Reconciler) createAPIResourceSchema(ctx context.Context, log *zap.SugaredLogger, projectedCRD *apiextensionsv1.CustomResourceDefinition, arsName string) error {
	// prefix is irrelevant as the name is overridden later
	converted, err := kcpdevv1alpha1.CRDToAPIResourceSchema(projectedCRD, "irrelevant")
	if err != nil {
		return fmt.Errorf("failed to convert CRD: %w", err)
	}

	ars := &kcpdevv1alpha1.APIResourceSchema{}
	ars.Name = arsName
	ars.Annotations = map[string]string{
		syncagentv1alpha1.SourceGenerationAnnotation: fmt.Sprintf("%d", projectedCRD.Generation),
		syncagentv1alpha1.AgentNameAnnotation:        r.agentName,
	}
	ars.Labels = map[string]string{
		syncagentv1alpha1.AgentNameLabel: r.agentName,
	}
	ars.Spec.Group = converted.Spec.Group
	ars.Spec.Names = converted.Spec.Names
	ars.Spec.Scope = converted.Spec.Scope
	ars.Spec.Versions = converted.Spec.Versions

	if len(converted.Spec.Versions) > 1 {
		ars.Spec.Conversion = &kcpdevv1alpha1.CustomResourceConversion{
			// as of kcp 0.27, there is no constant for this
			Strategy: kcpdevv1alpha1.ConversionStrategyType("None"),
		}
	}

	log.With("name", arsName).Info("Creating APIResourceSchema…")

	return r.kcpClient.Create(ctx, ars)
}

// getAPIResourceSchemaName generates the name for the ARS in kcp. Note that
// kcp requires, just like CRDs, that ARS are named following a specific pattern.
func (r *Reconciler) getAPIResourceSchemaName(crd *apiextensionsv1.CustomResourceDefinition) string {
	crd = crd.DeepCopy()
	crd.Spec.Conversion = nil

	checksum := crypto.Hash(crd.Spec)

	// include a leading "v" to prevent SHA-1 hashes with digits to break the name
	return fmt.Sprintf("v%s.%s.%s", checksum[:8], crd.Spec.Names.Plural, crd.Spec.Group)
}
