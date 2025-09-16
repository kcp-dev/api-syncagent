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
	"fmt"

	"go.uber.org/zap"

	"github.com/kcp-dev/api-syncagent/internal/controller/sync"
	"github.com/kcp-dev/api-syncagent/internal/controllerutil"
	"github.com/kcp-dev/api-syncagent/internal/controllerutil/predicate"
	"github.com/kcp-dev/api-syncagent/internal/discovery"
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	kcpapisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	apiexportprovider "github.com/kcp-dev/multicluster-provider/apiexport"
	mccontroller "sigs.k8s.io/multicluster-runtime/pkg/controller"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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

	apiExport *kcpapisv1alpha1.APIExport

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
	vwProvider *apiexportprovider.Provider

	// A map of sync controllers, one for each PublishedResource, using their
	// UIDs and resourceVersion as the map keys; using the version ensures that
	// when a PR changes, the old controller is orphaned and will be shut down.
	syncWorkers map[string]syncWorker
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
		kcpCluster:      kcpCluster,
		kcpRestConfig:   kcpRestConfig,
		log:             log,
		recorder:        localManager.GetEventRecorderFor(ControllerName),
		discoveryClient: discoveryClient,
		prFilter:        prFilter,
		stateNamespace:  stateNamespace,
		agentName:       agentName,
		syncWorkers:     map[string]syncWorker{},
	}

	_, err = builder.ControllerManagedBy(localManager).
		Named(ControllerName).
		WithOptions(controller.Options{
			// this controller is meant to control others, so we only want 1 thread
			MaxConcurrentReconciles: 1,
		}).
		// Watch for changes to APIExport on the kcp side to start/restart the actual syncing controllers;
		// the cache is already restricted by a fieldSelector in the main.go to respect the RBAC restrictions,
		// so there is no need here to add an additional filter.
		WatchesRawSource(source.Kind(kcpCluster.GetCache(), &kcpapisv1alpha1.APIExport{}, controllerutil.EnqueueConst[*kcpapisv1alpha1.APIExport]("dummy"))).
		// Watch for changes to the PublishedResources
		Watches(&syncagentv1alpha1.PublishedResource{}, controllerutil.EnqueueConst[ctrlruntimeclient.Object]("dummy"), builder.WithPredicates(predicate.ByLabels(prFilter))).
		Build(reconciler)

	return err
}

func (r *Reconciler) Reconcile(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
	log := r.log.Named(ControllerName)
	log.Debug("Processing")

	key := types.NamespacedName{Name: r.apiExport.Name}

	apiExport := &kcpapisv1alpha1.APIExport{}
	if err := r.kcpCluster.GetClient().Get(ctx, key, apiExport); ctrlruntimeclient.IgnoreNotFound(err) != nil {
		return reconcile.Result{}, fmt.Errorf("failed to retrieve APIExport: %w", err)
	}

	return reconcile.Result{}, r.reconcile(ctx, log, apiExport)
}

func (r *Reconciler) reconcile(ctx context.Context, log *zap.SugaredLogger, apiExport *kcpapisv1alpha1.APIExport) error {
	// We're not yet making use of APIEndpointSlices, as we don't even fully
	// support a sharded kcp setup yet. Hence for now we're safe just using
	// this deprecated VW URL.
	//nolint:staticcheck
	urls := apiExport.Status.VirtualWorkspaces

	// the virtual workspace is not ready yet
	if len(urls) == 0 {
		return nil
	}

	vwURL := urls[0].URL

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
	// Use the global app context so this provider is independent of the reconcile
	// context, which might get cancelled right after Reconcile() is done.
	r.vwManagerCtx, r.vwManagerCancel = context.WithCancel(r.ctx)

	vwConfig := rest.CopyConfig(r.kcpRestConfig)
	vwConfig.Host = vwURL

	scheme := runtime.NewScheme()

	if err := corev1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("failed to register scheme %s: %w", corev1.SchemeGroupVersion, err)
	}

	if err := kcpapisv1alpha1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("failed to register scheme %s: %w", kcpapisv1alpha1.SchemeGroupVersion, err)
	}

	if r.vwProvider == nil {
		log.Debug("Setting up APIExport provider…")

		provider, err := apiexportprovider.New(vwConfig, apiexportprovider.Options{
			Scheme: scheme,
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

	// start the provider
	go func() {
		// Use the global app context so this provider is independent of the reconcile
		// context, which might get cancelled right after Reconcile() is done.
		if err := r.vwProvider.Run(r.vwManagerCtx, r.vwManager); err != nil {
			log.Fatalw("Failed to start apiexport provider.", zap.Error(err))
		}
	}()

	return nil
}

type controllerShim struct {
	reconciler *Reconciler
}

func (s *controllerShim) Engage(ctx context.Context, clusterName string, cl cluster.Cluster) error {
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

	// Free all workers; since their contexts are based on the manager's context,
	// they have also been cancelled already above.
	r.syncWorkers = nil
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
		delete(r.syncWorkers, key)
	}

	// start missing controllers
	for idx := range publishedResources {
		pubRes := publishedResources[idx]
		key := getPublishedResourceKey(&pubRes)

		// controller already exists
		if _, exists := r.syncWorkers[key]; exists {
			continue
		}

		log.Infow("Starting new sync controller…", "key", key)

		ctrlCtx, ctrlCancel := context.WithCancel(r.vwManagerCtx)

		// create the sync controller;
		// use the reconciler's log without any additional reconciling context
		syncController, err := sync.Create(
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

		log.Infof("storing worker at %s", key)
		r.syncWorkers[key] = syncWorker{
			controller: syncController,
			cancel:     ctrlCancel,
		}

		// let 'er rip (remember to use the long-lived context here)
		if err := syncController.Start(ctrlCtx); err != nil {
			ctrlCancel()
			log.Info("deleting again")
			delete(r.syncWorkers, key)
			return fmt.Errorf("failed to start sync controller: %w", err)
		}
	}

	return nil
}
