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

	"github.com/go-logr/zapr"
	"go.uber.org/zap"

	"github.com/kcp-dev/api-syncagent/internal/discovery"
	"github.com/kcp-dev/api-syncagent/internal/mutation"
	"github.com/kcp-dev/api-syncagent/internal/projection"
	"github.com/kcp-dev/api-syncagent/internal/sync"
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	"github.com/kcp-dev/logicalcluster/v3"
	kcpcore "github.com/kcp-dev/sdk/apis/core"
	kcpcorev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	mccontroller "sigs.k8s.io/multicluster-runtime/pkg/controller"
	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
	mcsource "sigs.k8s.io/multicluster-runtime/pkg/source"
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
		Logger:                  zapr.NewLogger(log.Desugar()),
	}

	log.Info("Setting up unmanaged controller...")

	// The manager parameter is mostly unused and will be removed in future CR versions.
	c, err := mccontroller.NewUnmanaged(ControllerName, remoteManager, ctrlOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to instantiate new controller: %w", err)
	}

	// watch the target resource in the virtual workspace
	if err := c.MultiClusterWatch(mcsource.TypedKind(remoteDummy, mchandler.TypedEnqueueRequestForObject[*unstructured.Unstructured]())); err != nil {
		return nil, fmt.Errorf("failed to setup remote-side watch: %w", err)
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
		return nil, fmt.Errorf("failed to setup local-side watch: %w", err)
	}

	if err := setupRelatedResourceWatches(c, localManager, remoteManager, pubRes, localDummy, remoteDummy, log); err != nil {
		return nil, err
	}

	log.Info("Done setting up unmanaged controller.")

	return c, nil
}

// setupRelatedResourceWatches sets up watches for all related resources that have a Watch
// config, on their respective origin side, so that changes trigger primary reconciliation.
func setupRelatedResourceWatches(
	c mccontroller.Controller,
	localManager manager.Manager,
	remoteManager mcmanager.Manager,
	pubRes *syncagentv1alpha1.PublishedResource,
	localDummy, remoteDummy *unstructured.Unstructured,
	log *zap.SugaredLogger,
) error {
	// Deduplication is per-origin to allow the same GVK on both sides.
	watchedKcpGVKs := sets.New[schema.GroupVersionKind]()
	watchedServiceGVKs := sets.New[schema.GroupVersionKind]()

	for _, relRes := range pubRes.Spec.Related {
		if relRes.Watch == nil {
			continue
		}

		// this handles the legacy .Kind-based related resources
		gvr := projection.RelatedResourceGVR(&relRes)

		// Use the REST mapper of the origin side: related resources may have projected GVKs
		// that differ between kcp and the service cluster, so we must resolve using the
		// mapper that actually knows about the GVR on that side.
		var originRESTMapper meta.RESTMapper
		if relRes.Origin == syncagentv1alpha1.RelatedResourceOriginKcp {
			originRESTMapper = remoteManager.GetLocalManager().GetRESTMapper()
		} else {
			originRESTMapper = localManager.GetRESTMapper()
		}

		gvk, err := originRESTMapper.KindFor(gvr)
		if err != nil {
			return fmt.Errorf("failed to determine Kind for related resource %v (origin: %s): %w", gvr, relRes.Origin, err)
		}

		relatedDummy := &unstructured.Unstructured{}
		relatedDummy.SetGroupVersionKind(gvk)

		if relRes.Origin == syncagentv1alpha1.RelatedResourceOriginKcp {
			if watchedKcpGVKs.Has(gvk) {
				continue
			}
			watchedKcpGVKs.Insert(gvk)

			enqueueForRelated, err := buildKcpRelatedHandler(relRes.Watch, gvk, remoteDummy, log)
			if err != nil {
				return err
			}

			if err := c.MultiClusterWatch(mcsource.TypedKind(relatedDummy, enqueueForRelated)); err != nil {
				return fmt.Errorf("failed to setup watch for kcp-origin related resource %v: %w", gvk, err)
			}
		} else {
			if watchedServiceGVKs.Has(gvk) {
				continue
			}
			watchedServiceGVKs.Insert(gvk)

			enqueueForRelated, err := buildServiceRelatedHandler(relRes.Watch, gvk, localDummy, localManager, log)
			if err != nil {
				return err
			}

			if err := c.Watch(source.TypedKind(localManager.GetCache(), relatedDummy, enqueueForRelated)); err != nil {
				return fmt.Errorf("failed to setup watch for service-origin related resource %v: %w", gvk, err)
			}
		}

		log.Infow("Set up watch for related resource", "gvk", gvk, "origin", relRes.Origin)
	}

	return nil
}

// buildKcpRelatedHandler constructs the per-cluster event handler for a kcp-origin related resource.
func buildKcpRelatedHandler(
	watch *syncagentv1alpha1.RelatedResourceWatch,
	gvk schema.GroupVersionKind,
	remoteDummy *unstructured.Unstructured,
	log *zap.SugaredLogger,
) (mchandler.TypedEventHandlerFunc[*unstructured.Unstructured, mcreconcile.Request], error) {
	switch {
	case watch.ByOwner != nil:
		ownerGVK := remoteDummy.GroupVersionKind()
		return func(clusterName multicluster.ClusterName, _ cluster.Cluster) handler.TypedEventHandler[*unstructured.Unstructured, mcreconcile.Request] {
			return &byOwnerEventHandler{
				clusterName: clusterName,
				ownerGVK:    ownerGVK,
			}
		}, nil

	case watch.BySelector != nil:
		labelSelector := watch.BySelector
		primaryDummy := remoteDummy.DeepCopy()
		return func(clusterName multicluster.ClusterName, cl cluster.Cluster) handler.TypedEventHandler[*unstructured.Unstructured, mcreconcile.Request] {
			return &bySelectorEventHandler{
				clusterName:   clusterName,
				client:        cl.GetClient(),
				primaryDummy:  primaryDummy,
				labelSelector: labelSelector,
				log:           log,
			}
		}, nil

	default:
		return nil, fmt.Errorf("related resource %v (origin: kcp) has Watch set but neither byOwner nor bySelector configured", gvk)
	}
}

// buildServiceRelatedHandler constructs the event handler for a service-cluster-origin related resource.
// It maps the changed related resource back to the remote (kcp) primary via sync metadata on the local primary.
func buildServiceRelatedHandler(
	watch *syncagentv1alpha1.RelatedResourceWatch,
	gvk schema.GroupVersionKind,
	localDummy *unstructured.Unstructured,
	localManager manager.Manager,
	log *zap.SugaredLogger,
) (handler.TypedEventHandler[*unstructured.Unstructured, mcreconcile.Request], error) {
	localClient := localManager.GetClient()

	switch {
	case watch.ByOwner != nil:
		ownerGVK := localDummy.GroupVersionKind()
		primaryDummy := localDummy.DeepCopy()
		return handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, obj *unstructured.Unstructured) []mcreconcile.Request {
			for _, ref := range obj.GetOwnerReferences() {
				refGV, err := schema.ParseGroupVersion(ref.APIVersion)
				if err != nil || refGV.Group != ownerGVK.Group || refGV.Version != ownerGVK.Version || ref.Kind != ownerGVK.Kind {
					continue
				}
				localPrimary := primaryDummy.DeepCopy()
				if err := localClient.Get(ctx, types.NamespacedName{Namespace: obj.GetNamespace(), Name: ref.Name}, localPrimary); err != nil {
					log.Warnw("Failed to fetch local primary for byOwner watch", "owner", ref.Name, "error", err)
					return nil
				}
				if req := sync.RemoteNameForLocalObject(localPrimary); req != nil {
					return []mcreconcile.Request{*req}
				}
				return nil
			}
			return nil
		}), nil

	case watch.BySelector != nil:
		selector, err := metav1.LabelSelectorAsSelector(watch.BySelector)
		if err != nil {
			return nil, fmt.Errorf("failed to convert bySelector for service-origin related resource %v: %w", gvk, err)
		}
		primaryDummy := localDummy.DeepCopy()
		return handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, _ *unstructured.Unstructured) []mcreconcile.Request {
			primaryList := &unstructured.UnstructuredList{}
			primaryList.SetAPIVersion(primaryDummy.GetAPIVersion())
			primaryList.SetKind(primaryDummy.GetKind() + "List")
			if err := localClient.List(ctx, primaryList, &ctrlruntimeclient.ListOptions{LabelSelector: selector}); err != nil {
				log.Warnw("Failed to list local primary objects for bySelector watch", "selector", selector.String(), "error", err)
				return nil
			}
			var reqs []mcreconcile.Request
			for i := range primaryList.Items {
				if req := sync.RemoteNameForLocalObject(&primaryList.Items[i]); req != nil {
					reqs = append(reqs, *req)
				}
			}
			return reqs
		}), nil

	default:
		return nil, fmt.Errorf("related resource %v (origin: service) has Watch set but neither byOwner nor bySelector configured", gvk)
	}
}

func (r *Reconciler) Reconcile(ctx context.Context, request mcreconcile.Request) (reconcile.Result, error) {
	log := r.log.With("cluster", request.ClusterName, "request", request.NamespacedName)
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

	recorder := cl.GetEventRecorderFor(ControllerName) //nolint:staticcheck // https://github.com/kcp-dev/api-syncagent/issues/157

	ctx = sync.WithClusterName(ctx, logicalcluster.Name(request.ClusterName))
	ctx = sync.WithEventRecorder(ctx, recorder)

	// if desired, fetch the workspace path as well (some downstream service providers might make use of it,
	// but since it requires an additional permission claim, it's optional)
	if r.pubRes.Spec.EnableWorkspacePaths {
		lc := &kcpcorev1alpha1.LogicalCluster{}
		if err := vwClient.Get(ctx, types.NamespacedName{Name: kcpcorev1alpha1.LogicalClusterName}, lc); err != nil {
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
