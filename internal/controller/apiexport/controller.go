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

package apiexport

import (
	"context"
	"fmt"
	"slices"

	"github.com/kcp-dev/logicalcluster/v3"
	"go.uber.org/zap"

	"github.com/kcp-dev/api-syncagent/internal/controllerutil"
	predicateutil "github.com/kcp-dev/api-syncagent/internal/controllerutil/predicate"
	"github.com/kcp-dev/api-syncagent/internal/discovery"
	"github.com/kcp-dev/api-syncagent/internal/kcp"
	"github.com/kcp-dev/api-syncagent/internal/projection"
	"github.com/kcp-dev/api-syncagent/internal/resources/reconciling"
	"github.com/kcp-dev/api-syncagent/internal/validation"
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	kcpdevv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	ControllerName = "syncagent-apiexport"
)

type Reconciler struct {
	localClient     ctrlruntimeclient.Client
	kcpClient       ctrlruntimeclient.Client
	discoveryClient *discovery.Client
	log             *zap.SugaredLogger
	recorder        record.EventRecorder
	lcName          logicalcluster.Name
	apiExportName   string
	agentName       string
	prFilter        labels.Selector
}

// Add creates a new controller and adds it to the given manager.
func Add(
	mgr manager.Manager,
	kcpCluster cluster.Cluster,
	lcName logicalcluster.Name,
	log *zap.SugaredLogger,
	apiExportName string,
	agentName string,
	prFilter labels.Selector,
) error {
	discoveryClient, err := discovery.NewClient(mgr.GetConfig())
	if err != nil {
		return fmt.Errorf("failed to create discovery client: %w", err)
	}

	reconciler := &Reconciler{
		localClient:     mgr.GetClient(),
		kcpClient:       kcpCluster.GetClient(),
		discoveryClient: discoveryClient,
		lcName:          lcName,
		log:             log.Named(ControllerName),
		recorder:        kcpCluster.GetEventRecorderFor(ControllerName),
		apiExportName:   apiExportName,
		agentName:       agentName,
		prFilter:        prFilter,
	}

	hasARS := predicate.NewPredicateFuncs(func(object ctrlruntimeclient.Object) bool {
		publishedResource, ok := object.(*syncagentv1alpha1.PublishedResource)
		if !ok {
			return false
		}

		return publishedResource.Status.ResourceSchemaName != ""
	})

	_, err = builder.ControllerManagedBy(mgr).
		Named(ControllerName).
		WithOptions(controller.Options{
			// we reconcile a single object in kcp, no need for parallel workers
			MaxConcurrentReconciles: 1,
		}).
		// Watch for changes to APIExport on the kcp side to start/restart the actual syncing controllers;
		// the cache is already restricted by a fieldSelector in the main.go to respect the RBC restrictions,
		// so there is no need here to add an additional filter.
		WatchesRawSource(source.Kind(kcpCluster.GetCache(), &kcpdevv1alpha1.APIExport{}, controllerutil.EnqueueConst[*kcpdevv1alpha1.APIExport]("dummy"))).
		// Watch for changes to PublishedResources on the local service cluster
		Watches(&syncagentv1alpha1.PublishedResource{}, controllerutil.EnqueueConst[ctrlruntimeclient.Object]("dummy"), builder.WithPredicates(predicateutil.ByLabels(prFilter), hasARS)).
		Build(reconciler)

	return err
}

func (r *Reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	r.log.Debug("Processing")

	apiExport := &kcpdevv1alpha1.APIExport{}
	if err := r.kcpClient.Get(ctx, types.NamespacedName{Name: r.apiExportName}, apiExport); err != nil {
		return reconcile.Result{}, ctrlruntimeclient.IgnoreNotFound(err)
	}

	return reconcile.Result{}, r.reconcile(ctx, apiExport)
}

func (r *Reconciler) reconcile(ctx context.Context, apiExport *kcpdevv1alpha1.APIExport) error {
	// find all PublishedResources
	pubResources := &syncagentv1alpha1.PublishedResourceList{}
	if err := r.localClient.List(ctx, pubResources, &ctrlruntimeclient.ListOptions{
		LabelSelector: r.prFilter,
	}); err != nil {
		return fmt.Errorf("failed to list PublishedResources: %w", err)
	}

	// filter out those PRs that are invalid; we keep those that are not yet converted into ARS,
	// just to reduce the amount of re-reconciles when the agent processes a number of PRs in a row
	// and would constantly update the APIExport; instead we rely on kcp to handle the eventual
	// consistency.
	// This allows us to already determine all resources we "own", which helps us in
	// bilding the proper permission claims if one of the related resources is using a resource
	// type managed via PublishedResource. Otherwise the controller might not see the PR for the
	// related resource and temporarily incorrectly assume it needs to add a permission claim.
	filteredPubResources := slices.DeleteFunc(pubResources.Items, func(pr syncagentv1alpha1.PublishedResource) bool {
		// TODO: Turn this into a webhook or CEL expressions.
		err := validation.ValidatePublishedResource(&pr)
		if err != nil {
			r.log.With("pr", pr.Name, "error", err).Warn("Ignoring invalid PublishedResource.")
		}

		return err != nil
	})

	// for each PR, we note down the created ARS and also the GVKs of related resources
	newARSList := sets.New[string]()
	for _, pubResource := range filteredPubResources {
		schemaName, err := r.getSchemaName(ctx, &pubResource)
		if err != nil {
			return fmt.Errorf("failed to determine schema name for PublishedResource %s: %w", pubResource.Name, err)
		}

		newARSList.Insert(schemaName)
	}

	// To determine if the GVR of a related resource needs to be listed as a permission claim,
	// we first need to figure out all the GVRs our APIExport contains. This is not just the
	// list of ARS we just built, but also potentially other ARS's that exist in the APIExport
	// and that we would not touch.
	allARSList := mergeResourceSchemas(apiExport.Spec.LatestResourceSchemas, newARSList)

	// turn the flat list of schema names ("version.resource.group") into a lookup table consisting
	// of group/resource only
	ourOwnResources := sets.New[schema.GroupResource]()
	for _, schemaName := range allARSList {
		gvr, err := parseSchemaName(schemaName)
		if err != nil {
			return fmt.Errorf("failed to assemble own resources: %w", err)
		}

		ourOwnResources.Insert(gvr.GroupResource())
	}

	// Now we can finally assemble the list of required permission claims.
	claimedResources := sets.New[permissionClaim]()

	for _, pubResource := range filteredPubResources {
		// to evaluate the namespace filter, the agent needs to fetch the namespace
		if filter := pubResource.Spec.Filter; filter != nil && filter.Namespace != nil {
			claimedResources.Insert(permissionClaim{
				Group:    "",
				Resource: "namespaces",
			})
		}

		if pubResource.Spec.EnableWorkspacePaths {
			claimedResources.Insert(permissionClaim{
				Group:    "core.kcp.io",
				Resource: "logicalclusters",
			})
		}

		// Add a claim for every foreign (!) related resource, but make sure to
		// project the GVR of related resources to their kcp-side equivalent first
		// if they originate on the service cluster.
		for _, rr := range pubResource.Spec.Related {
			kcpGVR := projection.RelatedResourceKcpGVR(&rr)

			// We always want to see namespaces, for simplicity. Technically if we knew the scope
			// of every related resource, we could determine if a namespace claim is truly necessary.
			claimedResources.Insert(permissionClaim{
				Group:    "",
				Resource: "namespaces",
			})

			if !ourOwnResources.Has(kcpGVR.GroupResource()) {
				claimedResources.Insert(permissionClaim{
					Group:        kcpGVR.Group,
					Resource:     kcpGVR.Resource,
					IdentityHash: rr.IdentityHash,
				})
			}
		}
	}

	// We always want to create events.
	claimedResources.Insert(permissionClaim{
		Group:    "",
		Resource: "events",
	})

	// reconcile an APIExport in kcp
	factories := []reconciling.NamedAPIExportReconcilerFactory{
		r.createAPIExportReconciler(newARSList, claimedResources, r.agentName, r.apiExportName, r.recorder),
	}

	if err := reconciling.ReconcileAPIExports(ctx, factories, "", r.kcpClient); err != nil {
		return fmt.Errorf("failed to reconcile APIExport: %w", err)
	}

	// try to get the virtual workspace URL of the APIExport;
	// TODO: This controller should watch the APIExport for changes
	// and then update
	// if err := wait.PollImmediate(100*time.Millisecond, 3*time.Second, func() (done bool, err error) {
	// 	apiExport := &kcpdevv1alpha1.APIExport{}
	// 	key := types.NamespacedName{Name: exportName}

	// 	if err := r.kcpClient.Get(wsCtx, key, apiExport); ctrlruntimeclient.IgnoreNotFound(err) != nil {
	// 		return false, err
	// 	}

	// 	// NotFound (yet)
	// 	if apiExport.Name == "" {
	// 		return false, nil
	// 	}

	// 	// not ready
	// 	if len(apiExport.Status.VirtualWorkspaces) == 0 {
	// 		return false, nil
	// 	}

	// 	// do something with the URL...

	// 	return true, nil
	// }); err != nil {
	// 	return fmt.Errorf("failed to wait for virtual workspace to be ready: %w", err)
	// }

	return nil
}

func (r *Reconciler) getSchemaName(ctx context.Context, pubRes *syncagentv1alpha1.PublishedResource) (string, error) {
	if pubRes.Status.ResourceSchemaName != "" {
		return pubRes.Status.ResourceSchemaName, nil
	}

	// Technically we *could* wait and let the apiresourceschema controller do its
	// job and provide the status field above. But this would mean we potentially
	// temporarily misidentify related resources, adding unnecessary and invalid
	// permission claims in the APIExport. To avoid these it's worth it to basically
	// do the same projection logic as the other controller here, just to calculate
	// what the name of the schema would/will be.
	projectedCRD, err := projection.ProjectPublishedResource(ctx, r.discoveryClient, pubRes)
	if err != nil {
		return "", fmt.Errorf("failed to apply projection rules: %w", err)
	}

	return kcp.GetAPIResourceSchemaName(projectedCRD), nil
}
