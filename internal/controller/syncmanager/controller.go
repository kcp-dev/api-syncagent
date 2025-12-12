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
	"time"

	"go.uber.org/zap"

	controllersync "github.com/kcp-dev/api-syncagent/internal/controller/sync"
	"github.com/kcp-dev/api-syncagent/internal/controllerutil/predicate"
	"github.com/kcp-dev/api-syncagent/internal/discovery"
	"github.com/kcp-dev/api-syncagent/internal/kcp"
	"github.com/kcp-dev/api-syncagent/internal/projection"
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	ControllerName = "syncagent-syncmanager"

	// numSyncWorkers is the number of concurrent workers within each sync controller.
	numSyncWorkers = 4
)

type ClusterProviderFunc func() []string

type Reconciler struct {
	// choose to break good practice of never storing a context in a struct,
	// and instead opt to use the app's root context for the dynamically
	// started clusters, so when the Sync Agent shuts down, their shutdown is
	// also triggered.
	ctx context.Context

	localManager    manager.Manager
	dmcm            *kcp.DynamicMultiClusterManager
	log             *zap.SugaredLogger
	recorder        record.EventRecorder
	discoveryClient *discovery.Client
	resourceProber  *discovery.ResourceProber
	prFilter        labels.Selector
	stateNamespace  string
	agentName       string

	syncCancelsLock sync.RWMutex
	// A map of sync controllers, one for each PublishedResource, using their
	// UIDs and resourceVersion as the map keys; using the version ensures that
	// when a PR changes, the old controller is orphaned and will be shut down.
	syncCancels map[string]context.CancelCauseFunc
}

// Add creates a new controller and adds it to the given manager.
func Add(
	ctx context.Context,
	localManager manager.Manager,
	resourceProber *discovery.ResourceProber,
	dmcm *kcp.DynamicMultiClusterManager,
	log *zap.SugaredLogger,
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
		dmcm:            dmcm,
		log:             log,
		recorder:        localManager.GetEventRecorderFor(ControllerName),
		discoveryClient: discoveryClient,
		prFilter:        prFilter,
		stateNamespace:  stateNamespace,
		agentName:       agentName,
		resourceProber:  resourceProber,
		syncCancelsLock: sync.RWMutex{},
		syncCancels:     map[string]context.CancelCauseFunc{},
	}

	bldr := builder.
		ControllerManagedBy(localManager).
		Named(ControllerName).
		Watches(
			&syncagentv1alpha1.PublishedResource{},
			&handler.TypedEnqueueRequestForObject[ctrlruntimeclient.Object]{},
			builder.WithPredicates(predicate.ByLabels(prFilter)),
		)

	_, err = bldr.Build(reconciler)

	return err
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := r.log.Named(ControllerName)
	log.With("request", req.Name).Debug("Processing")

	pubRes := &syncagentv1alpha1.PublishedResource{}
	if err := r.localManager.GetClient().Get(ctx, req.NamespacedName, pubRes); err != nil {
		return reconcile.Result{}, ctrlruntimeclient.IgnoreNotFound(err)
	}

	var (
		err    error
		result reconcile.Result
	)

	if pubRes.DeletionTimestamp != nil || pubRes.Status.ResourceSchemaName == "" || !isSyncEnabled(pubRes) {
		// For PubRes that have not yet been processed, have their sync disabled or are in
		// deletion, we cleanup and stop any potentially running sync controller.
		err = r.cleanupController(log, pubRes)
	} else {
		// Otherwise, ensure a sync controller is running.
		result, err = r.ensureSyncController(ctx, log, pubRes)
	}

	return result, err
}

func (r *Reconciler) ensureSyncController(ctx context.Context, log *zap.SugaredLogger, pubRes *syncagentv1alpha1.PublishedResource) (reconcile.Result, error) {
	key := getPublishedResourceKey(pubRes)

	// controller already exists
	if _, exists := r.syncCancels[key]; exists {
		return reconcile.Result{}, nil
	}

	prlog := log.With("prkey", key, "name", pubRes.Name)

	// Multicluster-runtime *hates* it when you create a controller that watches
	// a resource that doesn't exist yet. So before we can proceed, we need to make
	// sure the synced resource is available (there is no good condition to
	// check anywhere, so a deep service discovery is required).
	resourceBound, err := r.checkResourceIsBoundEverywhere(ctx, log, pubRes)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to probe for existence of resource in virtual workspace: %w", err)
	}

	if !resourceBound {
		prlog.Info("Not all required resources are yet available, re-trying controller creation in a moment")
		// TODO: Or return an error to have it exponentially back off?
		return reconcile.Result{RequeueAfter: 1 * time.Second}, nil
	}

	// Use the global app context so this provider is independent of the reconcile
	// context, which might get cancelled right after Reconcile() is done.
	ctrlCtx, ctrlCancel := context.WithCancelCause(r.ctx)

	prlog.Info("Creating new sync controller…")

	// create the sync controller;
	// use the reconciler's log without any additional reconciling context
	syncController, err := controllersync.Create(
		// This can be the reconciling context, as it's only used to find the target CRD during setup;
		// this context *must not* be stored in the sync controller!
		ctx,
		r.localManager,
		r.dmcm.GetManager(),
		pubRes,
		r.discoveryClient,
		r.stateNamespace,
		r.agentName,
		r.log,
		numSyncWorkers,
	)
	if err != nil {
		ctrlCancel(errors.New("failed to create sync controller"))
		return reconcile.Result{}, fmt.Errorf("failed to create sync controller: %w", err)
	}

	r.syncCancels[key] = ctrlCancel

	// time to start the controller; this will spawn a new goroutine if
	// successful; the new controller will be pre-seeded with all knonwn
	// (engaged) clusters by the DMCM.
	if err = r.dmcm.StartController(ctrlCtx, log.With("prkey", key), syncController); err != nil {
		ctrlCancel(errors.New("failed to start sync controller"))
		return reconcile.Result{}, fmt.Errorf("failed to start sync controller: %w", err)
	}

	return reconcile.Result{}, nil
}

func (r *Reconciler) cleanupController(log *zap.SugaredLogger, pubRes *syncagentv1alpha1.PublishedResource) error {
	key := getPublishedResourceKey(pubRes)
	log.Infow("Stopping sync controller…", "prkey", key)

	r.syncCancelsLock.Lock()
	defer r.syncCancelsLock.Unlock()

	cancel, ok := r.syncCancels[key]
	if ok {
		cancel(errors.New("controller is no longer needed"))
		delete(r.syncCancels, key)
	}

	return nil
}

func (r *Reconciler) checkResourceIsBoundEverywhere(ctx context.Context, log *zap.SugaredLogger, pubRes *syncagentv1alpha1.PublishedResource) (bool, error) {
	projectedGVK, err := projection.ProjectPublishedResourceGVK(ctx, r.discoveryClient, pubRes)
	if err != nil {
		return false, fmt.Errorf("failed to determine projected primary GVK: %w", err)
	}

	return r.resourceProber.HasGVK(ctx, projectedGVK)

	// TODO: If the syncagent ever watches related resources, it also needs to check their GVRs here.
}

func getPublishedResourceKey(pr *syncagentv1alpha1.PublishedResource) string {
	return fmt.Sprintf("%s-%s", pr.UID, pr.ResourceVersion)
}

func isSyncEnabled(pr *syncagentv1alpha1.PublishedResource) bool {
	return pr.Spec.Synchronization == nil || pr.Spec.Synchronization.Enabled
}
