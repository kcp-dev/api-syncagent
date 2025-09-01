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

package sync

import (
	"context"
	"fmt"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"
	"go.uber.org/zap"

	"github.com/kcp-dev/api-syncagent/internal/discovery"
	"github.com/kcp-dev/api-syncagent/internal/mutation"
	"github.com/kcp-dev/api-syncagent/internal/projection"
	"github.com/kcp-dev/api-syncagent/internal/sync"
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	kcpcore "github.com/kcp-dev/kcp/sdk/apis/core"
	kcpdevcorev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	mccontroller "sigs.k8s.io/multicluster-runtime/pkg/controller"
	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
	mcsource "sigs.k8s.io/multicluster-runtime/pkg/source"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	ControllerName = "syncagent-sync"
)

type Reconciler struct {
	localClient    ctrlruntimeclient.Client
	remoteManager  mcmanager.Manager
	log            *zap.SugaredLogger
	remoteDummy    *unstructured.Unstructured
	pubRes         *syncagentv1alpha1.PublishedResource
	localCRD       *apiextensionsv1.CustomResourceDefinition
	stateNamespace string
	agentName      string
}

// Create creates a new controller and importantly does *not* add it to the manager,
// as this controller is started/stopped by the syncmanager controller instead.
func Create(
	ctx context.Context,
	localManager manager.Manager,
	remoteManager mcmanager.Manager,
	pubRes *syncagentv1alpha1.PublishedResource,
	discoveryClient *discovery.Client,
	stateNamespace string,
	agentName string,
	log *zap.SugaredLogger,
	numWorkers int,
) (mccontroller.Controller, error) {
	log = log.Named(ControllerName)

	// find the local CRD so we know the actual local object scope
	localCRD, err := discoveryClient.RetrieveCRD(ctx, projection.PublishedResourceSourceGK(pubRes))
	if err != nil {
		return nil, fmt.Errorf("failed to find local CRD: %w", err)
	}

	// create a dummy that represents the type used on the local service cluster
	localGVK, err := projection.PublishedResourceSourceGVK(localCRD, pubRes)
	if err != nil {
		return nil, err
	}

	localDummy := &unstructured.Unstructured{}
	localDummy.SetGroupVersionKind(localGVK)

	// create a dummy unstructured object with the projected GVK inside the workspace
	remoteGVK, err := projection.PublishedResourceProjectedGVK(localCRD, pubRes)
	if err != nil {
		return nil, err
	}

	remoteDummy := &unstructured.Unstructured{}
	remoteDummy.SetGroupVersionKind(remoteGVK)

	// create the syncer that holds the meat&potatoes of the synchronization logic

	// setup the reconciler
	reconciler := &Reconciler{
		localClient:    localManager.GetClient(),
		remoteManager:  remoteManager,
		log:            log,
		remoteDummy:    remoteDummy,
		pubRes:         pubRes,
		stateNamespace: stateNamespace,
		agentName:      agentName,
		localCRD:       localCRD,
	}

	ctrlOptions := mccontroller.Options{
		Reconciler:              reconciler,
		MaxConcurrentReconciles: numWorkers,
		SkipNameValidation:      ptr.To(true),
	}

	log.Info("Setting up unmanaged controller...")

	// The manager parameter is mostly unused and will be removed in future CR versions.
	c, err := mccontroller.NewUnmanaged(ControllerName, remoteManager, ctrlOptions)
	if err != nil {
		return nil, err
	}

	// watch the target resource in the virtual workspace
	if err := c.MultiClusterWatch(mcsource.TypedKind(remoteDummy, mchandler.TypedEnqueueRequestForObject[*unstructured.Unstructured]())); err != nil {
		return nil, err
	}

	// watch the source resource in the local cluster, but enqueue the origin remote object
	enqueueRemoteObjForLocalObj := handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, o *unstructured.Unstructured) []mcreconcile.Request {
		req := sync.RemoteNameForLocalObject(o)
		if req == nil {
			return nil
		}

		return []mcreconcile.Request{*req}
	})

	// only watch local objects that we own
	nameFilter := predicate.NewTypedPredicateFuncs(func(u *unstructured.Unstructured) bool {
		return sync.OwnedBy(u, agentName)
	})

	if err := c.Watch(source.TypedKind(localManager.GetCache(), localDummy, enqueueRemoteObjForLocalObj, nameFilter)); err != nil {
		return nil, err
	}

	log.Info("Done setting up unmanaged controller.")

	return c, nil
}

func (r *Reconciler) Reconcile(ctx context.Context, request mcreconcile.Request) (reconcile.Result, error) {
	log := r.log.With("request", request, "cluster", request.ClusterName)
	log.Debug("Processing")

	cl, err := r.remoteManager.GetCluster(ctx, request.ClusterName)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to get cluster: %w", err)
	}
	vwClient := cl.GetClient()

	remoteObj := r.remoteDummy.DeepCopy()
	if err := vwClient.Get(ctx, request.NamespacedName, remoteObj); ctrlruntimeclient.IgnoreNotFound(err) != nil {
		return reconcile.Result{}, fmt.Errorf("failed to retrieve remote object: %w", err)
	}

	// object was not found anymore
	if remoteObj.GetName() == "" {
		return reconcile.Result{}, nil
	}

	// if there is a namespace, get it if a namespace filter is also configured
	var namespace *corev1.Namespace
	if filter := r.pubRes.Spec.Filter; filter != nil && filter.Namespace != nil && remoteObj.GetNamespace() != "" {
		namespace = &corev1.Namespace{}
		key := types.NamespacedName{Name: remoteObj.GetNamespace()}

		if err := vwClient.Get(ctx, key, namespace); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to retrieve remote object's namespace: %w", err)
		}
	}

	// apply filtering rules to scope down the number of objects we sync
	include, err := r.objectMatchesFilter(remoteObj, namespace)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to apply filtering rules: %w", err)
	}

	if !include {
		return reconcile.Result{}, nil
	}

	recorder := cl.GetEventRecorderFor(ControllerName)

	ctx = sync.WithClusterName(ctx, logicalcluster.Name(request.ClusterName))
	ctx = sync.WithEventRecorder(ctx, recorder)

	// if desired, fetch the workspace path as well (some downstream service providers might make use of it,
	// but since it requires an additional permission claim, it's optional)
	if r.pubRes.Spec.EnableWorkspacePaths {
		lc := &kcpdevcorev1alpha1.LogicalCluster{}
		if err := vwClient.Get(ctx, types.NamespacedName{Name: kcpdevcorev1alpha1.LogicalClusterName}, lc); err != nil {
			recorder.Event(remoteObj, corev1.EventTypeWarning, "ReconcilingError", "Failed to retrieve workspace path, cannot process object.")
			return reconcile.Result{}, fmt.Errorf("failed to retrieve remote logicalcluster: %w", err)
		}

		path := lc.Annotations[kcpcore.LogicalClusterPathAnnotationKey]
		ctx = sync.WithWorkspacePath(ctx, logicalcluster.NewPath(path))
	}

	// sync main object
	syncer, err := sync.NewResourceSyncer(log, r.localClient, vwClient, r.pubRes, r.localCRD, mutation.NewMutator, r.stateNamespace, r.agentName)
	if err != nil {
		recorder.Event(remoteObj, corev1.EventTypeWarning, "ReconcilingError", "Failed to process object: a provider-side issue has occurred.")
		return reconcile.Result{}, fmt.Errorf("failed to create syncer: %w", err)
	}

	requeue, err := syncer.Process(ctx, remoteObj)
	if err != nil {
		recorder.Event(remoteObj, corev1.EventTypeWarning, "ReconcilingError", "Failed to process object: a provider-side issue has occurred.")
		return reconcile.Result{}, err
	}

	result := reconcile.Result{}
	if requeue {
		// 5s was chosen at random, winning narrowly against 6s and 4.7s
		result.RequeueAfter = 5 * time.Second
	}

	return result, nil
}

func (r *Reconciler) objectMatchesFilter(remoteObj *unstructured.Unstructured, namespace *corev1.Namespace) (bool, error) {
	if r.pubRes.Spec.Filter == nil {
		return true, nil
	}

	objMatches, err := r.matchesFilter(remoteObj, r.pubRes.Spec.Filter.Resource)
	if err != nil || !objMatches {
		return false, err
	}

	nsMatches, err := r.matchesFilter(namespace, r.pubRes.Spec.Filter.Namespace)
	if err != nil || !nsMatches {
		return false, err
	}

	return true, nil
}

func (r *Reconciler) matchesFilter(obj metav1.Object, selector *metav1.LabelSelector) (bool, error) {
	if selector == nil {
		return true, nil
	}

	s, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return false, err
	}

	return s.Matches(labels.Set(obj.GetLabels())), nil
}
