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

	"github.com/kcp-dev/logicalcluster/v3"
	"go.uber.org/zap"

	"github.com/kcp-dev/api-syncagent/internal/controller/sync"
	"github.com/kcp-dev/api-syncagent/internal/controller/syncmanager/lifecycle"
	"github.com/kcp-dev/api-syncagent/internal/controllerutil"
	"github.com/kcp-dev/api-syncagent/internal/controllerutil/predicate"
	"github.com/kcp-dev/api-syncagent/internal/discovery"
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	kcpdevv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/kontext"
	"sigs.k8s.io/controller-runtime/pkg/manager"
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

	apiExport *kcpdevv1alpha1.APIExport

	// URL for which the current vwCluster instance has been created
	vwURL string

	// a Cluster representing the virtual workspace for the APIExport
	vwCluster *lifecycle.Cluster

	// a map of sync controllers, one for each PublishedResource, using their
	// UIDs and resourceVersion as the map keys; using the version ensures that
	// when a PR changes, the old controller is orphaned and will be shut down.
	syncWorkers map[string]lifecycle.Controller
}

// Add creates a new controller and adds it to the given manager.
func Add(
	ctx context.Context,
	localManager manager.Manager,
	kcpCluster cluster.Cluster,
	kcpRestConfig *rest.Config,
	log *zap.SugaredLogger,
	apiExport *kcpdevv1alpha1.APIExport,
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
		syncWorkers:     map[string]lifecycle.Controller{},
		discoveryClient: discoveryClient,
		prFilter:        prFilter,
		stateNamespace:  stateNamespace,
		agentName:       agentName,
	}

	_, err = builder.ControllerManagedBy(localManager).
		Named(ControllerName).
		WithOptions(controller.Options{
			// this controller is meant to control others, so we only want 1 thread
			MaxConcurrentReconciles: 1,
		}).
		// Watch for changes to APIExport on the kcp side to start/restart the actual syncing controllers;
		// the cache is already restricted by a fieldSelector in the main.go to respect the RBC restrictions,
		// so there is no need here to add an additional filter.
		WatchesRawSource(source.Kind(kcpCluster.GetCache(), &kcpdevv1alpha1.APIExport{}, controllerutil.EnqueueConst[*kcpdevv1alpha1.APIExport]("dummy"))).
		// Watch for changes to the PublishedResources
		Watches(&syncagentv1alpha1.PublishedResource{}, controllerutil.EnqueueConst[ctrlruntimeclient.Object]("dummy"), builder.WithPredicates(predicate.ByLabels(prFilter))).
		Build(reconciler)
	return err
}

func (r *Reconciler) Reconcile(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
	log := r.log.Named(ControllerName)
	log.Debug("Processing")

	wsCtx := kontext.WithCluster(ctx, logicalcluster.From(r.apiExport))
	key := types.NamespacedName{Name: r.apiExport.Name}

	apiExport := &kcpdevv1alpha1.APIExport{}
	if err := r.kcpCluster.GetClient().Get(wsCtx, key, apiExport); ctrlruntimeclient.IgnoreNotFound(err) != nil {
		return reconcile.Result{}, fmt.Errorf("failed to retrieve APIExport: %w", err)
	}

	return reconcile.Result{}, r.reconcile(ctx, log, apiExport)
}

func (r *Reconciler) reconcile(ctx context.Context, log *zap.SugaredLogger, apiExport *kcpdevv1alpha1.APIExport) error {
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

	// if the VW URL changed, stop the cluster and all sync controllers
	if r.vwURL != "" && vwURL != r.vwURL {
		r.stopSyncControllers(log)
		r.stopVirtualWorkspaceCluster(log)
	}

	// if kcp had a hiccup and wrote a status without an actual URL
	if vwURL == "" {
		return nil
	}

	// make sure we have a running cluster object for the virtual workspace
	if err := r.ensureVirtualWorkspaceCluster(log, vwURL); err != nil {
		return fmt.Errorf("failed to ensure virtual workspace cluster: %w", err)
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

func (r *Reconciler) ensureVirtualWorkspaceCluster(log *zap.SugaredLogger, vwURL string) error {
	if r.vwCluster == nil {
		log.Info("Setting up virtual workspace cluster…")

		stoppableCluster, err := lifecycle.NewCluster(vwURL, r.kcpRestConfig)
		if err != nil {
			return fmt.Errorf("failed to initialize cluster: %w", err)
		}

		// use the app's root context as the base, not the reconciling context, which
		// might get cancelled after Reconcile() is done;
		// likewise use the reconciler's log without any additional reconciling context
		if err := stoppableCluster.Start(r.ctx, r.log); err != nil {
			return fmt.Errorf("failed to start cluster: %w", err)
		}

		log.Debug("Virtual workspace cluster setup completed.")

		r.vwURL = vwURL
		r.vwCluster = stoppableCluster
	}

	return nil
}

func (r *Reconciler) stopVirtualWorkspaceCluster(log *zap.SugaredLogger) {
	if r.vwCluster != nil {
		if err := r.vwCluster.Stop(log); err != nil {
			log.Errorw("Failed to stop cluster", zap.Error(err))
		}
	}

	r.vwCluster = nil
	r.vwURL = ""
}

func getPublishedResourceKey(pr *syncagentv1alpha1.PublishedResource) string {
	return fmt.Sprintf("%s-%s", pr.UID, pr.ResourceVersion)
}

func (r *Reconciler) ensureSyncControllers(ctx context.Context, log *zap.SugaredLogger, publishedResources []syncagentv1alpha1.PublishedResource) error {
	currentPRWorkers := sets.New[string]()
	for _, pr := range publishedResources {
		currentPRWorkers.Insert(getPublishedResourceKey(&pr))
	}

	// stop controllers that are no longer needed
	for key, ctrl := range r.syncWorkers {
		// if the controller failed to properly start, its goroutine will have
		// ended already, but it's still lingering around in the syncWorkers map;
		// controller is still required and running
		if currentPRWorkers.Has(key) && ctrl.Running() {
			continue
		}

		log.Infow("Stopping sync controller…", "key", key)

		var cause error
		if ctrl.Running() {
			cause = errors.New("PublishedResource not available anymore")
		} else {
			cause = errors.New("gc'ing failed controller")
		}

		// can only fail if the controller wasn't running; a situation we do not care about here
		_ = ctrl.Stop(log, cause)
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

		// create the sync controller;
		// use the reconciler's log without any additional reconciling context
		syncController, err := sync.Create(
			// This can be the reconciling context, as it's only used to find the target CRD during setup;
			// this context *must not* be stored in the sync controller!
			ctx,
			r.localManager,
			r.vwCluster.GetCluster(),
			&pubRes,
			r.discoveryClient,
			r.apiExport.Name,
			r.stateNamespace,
			r.agentName,
			r.log,
			numSyncWorkers,
		)
		if err != nil {
			return fmt.Errorf("failed to create sync controller: %w", err)
		}

		// wrap it so we can start/stop it easily
		wrappedController, err := lifecycle.NewController(syncController)
		if err != nil {
			return fmt.Errorf("failed to wrap sync controller: %w", err)
		}

		// let 'er rip (remember to use the long-lived app root context here)
		if err := wrappedController.Start(r.ctx, log); err != nil {
			return fmt.Errorf("failed to start sync controller: %w", err)
		}

		r.syncWorkers[key] = wrappedController
	}

	return nil
}

func (r *Reconciler) stopSyncControllers(log *zap.SugaredLogger) {
	cause := errors.New("virtual workspace cluster is recreating")

	for uid, ctrl := range r.syncWorkers {
		if err := ctrl.Stop(log, cause); err != nil {
			log.Errorw("Failed to stop controller", "uid", uid, zap.Error(err))
		}

		delete(r.syncWorkers, uid)
	}
}
