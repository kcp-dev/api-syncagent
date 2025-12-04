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

package syncmanager

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"go.uber.org/zap"

	controllersync "github.com/kcp-dev/api-syncagent/internal/controller/sync"
	"github.com/kcp-dev/api-syncagent/internal/controllerutil"
	"github.com/kcp-dev/api-syncagent/internal/controllerutil/predicate"
	"github.com/kcp-dev/api-syncagent/internal/discovery"
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	apiexportprovider "github.com/kcp-dev/multicluster-provider/apiexport"
	kcpapisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	kcpcorev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	mccontroller "sigs.k8s.io/multicluster-runtime/pkg/controller"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	ControllerName = "syncagent-syncmanager"

	// numSyncWorkers is the number of concurrent workers within each sync controller.
	numSyncWorkers = 4
)

type Reconciler struct {
	// choose to break good practice of never storing a context in a struct,
	// and instead opt to use the app's root context for the dynamically
	// started clusters, so when the Sync Agent shuts down, their shutdown is
	// also triggered.
	ctx context.Context

	localManager    manager.Manager
	kcpCluster      cluster.Cluster
	kcpRestConfig   *rest.Config
	log             *zap.SugaredLogger
	recorder        record.EventRecorder
	discoveryClient *discovery.Client
	prFilter        labels.Selector
	stateNamespace  string
	agentName       string

	// endpointSlice is preferred over apiExport
	apiExport     *kcpapisv1alpha1.APIExport
	endpointSlice *kcpapisv1alpha1.APIExportEndpointSlice

	// URL for which the current vwCluster instance has been created
	vwURL string

	// A multi-cluster Manager representing the virtual workspace cluster; this manager will
	// not handle the individual controllers' lifecycle, because their lifecycle depends on
	// PublishedResources, not the set of workspaces/clusters in the APIExport's virtual workspace.
	// This manager is stopped and recreated whenever the APIExport's URL changes.
	vwManager       mcmanager.Manager
	vwManagerCtx    context.Context
	vwManagerCancel context.CancelFunc

	// The provider based on the APIExport; like the vwManager, this is stopped and recreated
	// whenever the APIExport's URL changes.
	providerOnce sync.Once
	vwProvider   *apiexportprovider.Provider

	syncWorkersLock sync.RWMutex
	// A map of sync controllers, one for each PublishedResource, using their
	// UIDs and resourceVersion as the map keys; using the version ensures that
	// when a PR changes, the old controller is orphaned and will be shut down.
	syncWorkers map[string]syncWorker

	clustersLock sync.RWMutex
	// A map of clusters that have been engaged with the shim layer. Since this
	// reconciler dynamically starts and stops controllers, we need to keep track
	// of clusters and engage them with sync controllers started at a later point in time.
	clusters map[string]engagedCluster
}

type syncWorker struct {
	controller mccontroller.Controller
	cancel     context.CancelFunc
}

// Add creates a new controller and adds it to the given manager.
func Add(
	ctx context.Context,
	localManager manager.Manager,
	kcpCluster cluster.Cluster,
	kcpRestConfig *rest.Config,
	log *zap.SugaredLogger,
	apiExport *kcpapisv1alpha1.APIExport,
	endpointSlice *kcpapisv1alpha1.APIExportEndpointSlice,
	prFilter labels.Selector,
	stateNamespace string,
	agentName string,
) error {
	discoveryClient, err := discovery.NewClient(localManager.GetConfig())
	if err != nil {
		return fmt.Errorf("failed to create discovery client: %w", err)
	}

	reconciler := &Reconciler{
		ctx:             ctx,
		localManager:    localManager,
		apiExport:       apiExport,
		endpointSlice:   endpointSlice,
		kcpCluster:      kcpCluster,
		kcpRestConfig:   kcpRestConfig,
		log:             log,
		recorder:        localManager.GetEventRecorderFor(ControllerName),
		discoveryClient: discoveryClient,
		prFilter:        prFilter,
		stateNamespace:  stateNamespace,
		agentName:       agentName,

		providerOnce: sync.Once{},

		syncWorkersLock: sync.RWMutex{},
		syncWorkers:     map[string]syncWorker{},

		clustersLock: sync.RWMutex{},
		clusters:     make(map[string]engagedCluster),
	}

	bldr := builder.ControllerManagedBy(localManager).
		Named(ControllerName).
		WithOptions(controller.Options{
			// this controller is meant to control others, so we only want 1 thread
			MaxConcurrentReconciles: 1,
		}).
		// Watch for changes to the PublishedResources
		Watches(&syncagentv1alpha1.PublishedResource{}, controllerutil.EnqueueConst[ctrlruntimeclient.Object]("dummy"), builder.WithPredicates(predicate.ByLabels(prFilter)))

	// Watch for changes to APIExport/EndpointSlice on the kcp side to start/restart the actual syncing controllers;
	// the cache is already restricted by a fieldSelector in the main.go to respect the RBAC restrictions,
	// so there is no need here to add an additional filter.
	if endpointSlice != nil {
		bldr.WatchesRawSource(source.Kind(kcpCluster.GetCache(), &kcpapisv1alpha1.APIExportEndpointSlice{}, controllerutil.EnqueueConst[*kcpapisv1alpha1.APIExportEndpointSlice]("dummy")))
	} else {
		bldr.WatchesRawSource(source.Kind(kcpCluster.GetCache(), &kcpapisv1alpha1.APIExport{}, controllerutil.EnqueueConst[*kcpapisv1alpha1.APIExport]("dummy")))
	}

	_, err = bldr.Build(reconciler)

	return err
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := r.log.Named(ControllerName)
	log.Debug("Processing")

	var err error

	if r.endpointSlice != nil {
		if err := r.kcpCluster.GetClient().Get(ctx, ctrlruntimeclient.ObjectKeyFromObject(r.endpointSlice), r.endpointSlice); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to retrieve APIExportEndpointSlice: %w", err)
		}

		urls := r.endpointSlice.Status.APIExportEndpoints

		if len(urls) == 0 {
			// the virtual workspace is not ready yet
			log.Warn("APIExportEndpointSlice has no URLs.")
		} else {
			err = r.reconcile(ctx, log, urls[0].URL)
		}
	} else {
		if err := r.kcpCluster.GetClient().Get(ctx, ctrlruntimeclient.ObjectKeyFromObject(r.apiExport), r.apiExport); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to retrieve APIExport: %w", err)
		}

		//nolint:staticcheck
		urls := r.apiExport.Status.VirtualWorkspaces

		if len(urls) == 0 {
			// the virtual workspace is not ready yet
			log.Warn("APIExport has no virtual workspace URLs.")
		} else {
			err = r.reconcile(ctx, log, urls[0].URL)
		}
	}

	return reconcile.Result{}, err
}

func (r *Reconciler) reconcile(ctx context.Context, log *zap.SugaredLogger, vwURL string) error {
	// if the VW URL changed, stop the manager and all sync controllers
	if r.vwURL != "" && vwURL != r.vwURL {
		r.shutdown(log)
	}

	// if kcp had a hiccup and wrote a status without an actual URL
	if vwURL == "" {
		return nil
	}

	// make sure we have a running manager object for the virtual workspace
	if err := r.ensureManager(log, vwURL); err != nil {
		return fmt.Errorf("failed to ensure virtual workspace manager: %w", err)
	}

	// find all PublishedResources
	pubResources := &syncagentv1alpha1.PublishedResourceList{}
	if err := r.localManager.GetClient().List(ctx, pubResources, &ctrlruntimeclient.ListOptions{
		LabelSelector: r.prFilter,
	}); err != nil {
		return fmt.Errorf("failed to list PublishedResources: %w", err)
	}

	// make sure that for every PublishedResource, a matching sync controller exists
	if err := r.ensureSyncControllers(ctx, log, pubResources.Items); err != nil {
		return fmt.Errorf("failed to ensure sync controllers: %w", err)
	}

	return nil
}

func (r *Reconciler) ensureManager(log *zap.SugaredLogger, vwURL string) error {
	if r.vwManagerCtx == nil {
		// Use the global app context so this provider is independent of the reconcile
		// context, which might get cancelled right after Reconcile() is done.
		r.vwManagerCtx, r.vwManagerCancel = context.WithCancel(r.ctx)
	}

	vwConfig := rest.CopyConfig(r.kcpRestConfig)
	vwConfig.Host = vwURL

	scheme := runtime.NewScheme()

	if err := corev1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("failed to register scheme %s: %w", corev1.SchemeGroupVersion, err)
	}

	if err := kcpapisv1alpha1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("failed to register scheme %s: %w", kcpapisv1alpha1.SchemeGroupVersion, err)
	}

	if err := kcpcorev1alpha1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("failed to register scheme %s: %w", kcpcorev1alpha1.SchemeGroupVersion, err)
	}

	if r.vwProvider == nil {
		log.Debug("Setting up APIExport provider…")

		provider, err := apiexportprovider.New(vwConfig, "BOBO", apiexportprovider.Options{
			Scheme: scheme,
			// The provider is still on kcp 0.28, hence it has an entirely differnt
			// kcp SDK module; to make sure the scheme we provide actually contains
			// the real, new 0.29-style kcp SDK, we have to override this object.
			// TODO: Once the multicluster-provider we use is also on 0.29+, this
			// shouldn't be needed anymore.
			ObjectToWatch: &kcpapisv1alpha1.APIBinding{},
		})
		if err != nil {
			return fmt.Errorf("failed to init apiexport provider: %w", err)
		}

		r.vwProvider = provider
	}

	if r.vwManager == nil {
		log.Debug("Setting up virtual workspace manager…")

		manager, err := mcmanager.New(vwConfig, r.vwProvider, manager.Options{
			Scheme:         scheme,
			LeaderElection: false,
			Metrics: server.Options{
				BindAddress: "0",
			},
		})
		if err != nil {
			return fmt.Errorf("failed to initialize cluster: %w", err)
		}

		// Make sure the vwManager can Engage() on the controller, even though we
		// start and stop them outside the control of the manager. This shim will
		// ensure Engage() calls are handed to the underlying sync controller as
		// as long as the controller is running.
		if err := manager.Add(&controllerShim{reconciler: r}); err != nil {
			return fmt.Errorf("failed to initialize cluster: %w", err)
		}

		// use the app's root context as the base, not the reconciling context, which
		// might get cancelled after Reconcile() is done;
		// likewise use the reconciler's log without any additional reconciling context
		go func() {
			if err := manager.Start(r.vwManagerCtx); err != nil {
				log.Fatalw("Failed to start manager.", zap.Error(err))
			}
		}()

		log.Debug("Virtual workspace cluster setup completed.")

		r.vwURL = vwURL
		r.vwManager = manager
	}

	r.providerOnce.Do(func() {
		log.Debug("Starting virtual workspace provider…")
		// start the provider
		go func() {
			// Use the global app context so this provider is independent of the reconcile
			// context, which might get cancelled right after Reconcile() is done.
			if err := r.vwProvider.Start(r.vwManagerCtx, r.vwManager); err != nil {
				log.Fatalw("Failed to start apiexport provider", zap.Error(err))
			}
		}()
	})

	return nil
}

type engagedCluster struct {
	ctx context.Context
	cl  cluster.Cluster
}

type controllerShim struct {
	reconciler *Reconciler
}

func (s *controllerShim) Engage(ctx context.Context, clusterName string, cl cluster.Cluster) error {
	if _, ok := s.reconciler.clusters[clusterName]; !ok {
		s.reconciler.clustersLock.Lock()
		s.reconciler.clusters[clusterName] = engagedCluster{ctx: ctx, cl: cl}
		s.reconciler.clustersLock.Unlock()

		// start a goroutine to make sure we remove the cluster when the context is done
		go func() {
			<-ctx.Done()
			s.reconciler.clustersLock.Lock()
			delete(s.reconciler.clusters, clusterName)
			s.reconciler.clustersLock.Unlock()
		}()
	}

	s.reconciler.syncWorkersLock.RLock()
	defer s.reconciler.syncWorkersLock.RUnlock()
	for _, worker := range s.reconciler.syncWorkers {
		if err := worker.controller.Engage(ctx, clusterName, cl); err != nil {
			return err
		}
	}

	return nil
}

func (s *controllerShim) Start(_ context.Context) error {
	// NOP, controllers are started outside the control of the manager.
	return nil
}

// shutdown will cancel the current context and thereby stop the manager and all
// sync controllers at the same time.
func (r *Reconciler) shutdown(log *zap.SugaredLogger) {
	log.Debug("Shutting down existing manager…")

	if r.vwManagerCancel != nil {
		r.vwManagerCancel()
	}

	r.vwProvider = nil
	r.vwManager = nil
	r.vwManagerCtx = nil
	r.vwManagerCancel = nil
	r.vwURL = ""
	r.providerOnce = sync.Once{}

	r.clustersLock.Lock()
	r.clusters = make(map[string]engagedCluster)
	r.clustersLock.Unlock()

	// Free all workers; since their contexts are based on the manager's context,
	// they have also been cancelled already above.
	r.syncWorkersLock.Lock()
	r.syncWorkers = make(map[string]syncWorker)
	r.syncWorkersLock.Unlock()
}

func getPublishedResourceKey(pr *syncagentv1alpha1.PublishedResource) string {
	return fmt.Sprintf("%s-%s", pr.UID, pr.ResourceVersion)
}

func (r *Reconciler) ensureSyncControllers(ctx context.Context, log *zap.SugaredLogger, publishedResources []syncagentv1alpha1.PublishedResource) error {
	requiredWorkers := sets.New[string]()
	for _, pr := range publishedResources {
		requiredWorkers.Insert(getPublishedResourceKey(&pr))
	}

	// stop controllers that are no longer needed
	for key, worker := range r.syncWorkers {
		if requiredWorkers.Has(key) {
			continue
		}

		log.Infow("Stopping sync controller…", "key", key)

		worker.cancel()

		r.syncWorkersLock.Lock()
		delete(r.syncWorkers, key)
		r.syncWorkersLock.Unlock()
	}

	// start missing controllers
	for idx := range publishedResources {
		pubRes := publishedResources[idx]
		key := getPublishedResourceKey(&pubRes)

		// controller already exists
		if _, exists := r.syncWorkers[key]; exists {
			continue
		}

		prlog := log.With("key", key, "name", pubRes.Name)
		ctrlCtx, ctrlCancel := context.WithCancel(r.vwManagerCtx)

		prlog.Info("Creating new sync controller…")

		// create the sync controller;
		// use the reconciler's log without any additional reconciling context
		syncController, err := controllersync.Create(
			// This can be the reconciling context, as it's only used to find the target CRD during setup;
			// this context *must not* be stored in the sync controller!
			ctx,
			r.localManager,
			r.vwManager,
			&pubRes,
			r.discoveryClient,
			r.stateNamespace,
			r.agentName,
			r.log,
			numSyncWorkers,
		)
		if err != nil {
			ctrlCancel()
			return fmt.Errorf("failed to create sync controller: %w", err)
		}

		r.syncWorkersLock.Lock()
		r.syncWorkers[key] = syncWorker{
			controller: syncController,
			cancel:     ctrlCancel,
		}
		r.syncWorkersLock.Unlock()

		go func() {
			log.Infow("Starting sync controller…", "key", key)
			if err := syncController.Start(ctrlCtx); err != nil && !errors.Is(err, context.Canceled) {
				ctrlCancel()
				prlog.Errorw("failed to start sync controller", zap.Error(err))
			}

			prlog.Debug("Stopped sync controller")

			r.syncWorkersLock.Lock()
			delete(r.syncWorkers, key)
			r.syncWorkersLock.Unlock()
		}()

		r.clustersLock.RLock()
		defer r.clustersLock.RUnlock()
		for name, ec := range r.clusters {
			if err := syncController.Engage(ec.ctx, name, ec.cl); err != nil {
				prlog.Errorw("failed to engage cluster", zap.Error(err), "cluster", name)
			}
		}
	}

	return nil
}
