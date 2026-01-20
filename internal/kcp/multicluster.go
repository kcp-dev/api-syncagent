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

package kcp

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"go.uber.org/zap"

	apiexportprovider "github.com/kcp-dev/multicluster-provider/apiexport"
	kcpapisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	kcpcorev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
)

// DynamicMultiClusterManager (DMCM) is an extension to the regular multicluster manager.
// It's capable of starting new controllers at any time by keeping track of all
// engaged clusters (so that controllers that start later can be pre-seeded).
// Likewise it's possible to stop any of the controllers at any time.
//
// The DMCM interface is goroutine-safe.
type DynamicMultiClusterManager struct {
	manager mcmanager.Manager

	runnablesLock sync.RWMutex
	// A map of sync controllers, one for each PublishedResource, using their
	// UIDs and resourceVersion as the map keys; using the version ensures that
	// when a PR changes, the old controller is orphaned and will be shut down.
	runnables map[string]mcmanager.Runnable

	tracker *clusterTracker
}

func NewDynamicMultiClusterManager(cfg *rest.Config, endpointSliceName string) (*DynamicMultiClusterManager, error) {
	scheme := runtime.NewScheme()

	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to register scheme %s: %w", corev1.SchemeGroupVersion, err)
	}

	if err := kcpapisv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to register scheme %s: %w", kcpapisv1alpha1.SchemeGroupVersion, err)
	}

	if err := kcpcorev1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to register scheme %s: %w", kcpcorev1alpha1.SchemeGroupVersion, err)
	}

	// Setup the multicluster provider, it will watch the EndpointSlice on its own.
	provider, err := apiexportprovider.New(cfg, endpointSliceName, apiexportprovider.Options{
		Scheme: scheme,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create multicluster provider: %w", err)
	}

	// Setup a multiClusterManager, which will use the apiexport provider and
	// try to engage a dummy controller, which will just keep track of all the
	// engaged clusters over the entire lifetime of the process.
	multiClusterManager, err := mcmanager.New(cfg, provider, manager.Options{
		Scheme:         scheme,
		LeaderElection: false,
		Metrics: server.Options{
			BindAddress: "0",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize manager: %w", err)
	}

	dynManager := &DynamicMultiClusterManager{
		manager:       multiClusterManager,
		runnablesLock: sync.RWMutex{},
		runnables:     map[string]mcmanager.Runnable{},
	}

	tracker := newClusterTracker(dynManager)
	dynManager.tracker = tracker

	// Start our tracker as the first and most important runnable in this mcmanager.
	if err := multiClusterManager.Add(tracker); err != nil {
		return nil, fmt.Errorf("failed to initialize cluster: %w", err)
	}

	return dynManager, nil
}

func (dmcm *DynamicMultiClusterManager) GetManager() mcmanager.Manager {
	return dmcm.manager
}

func (dmcm *DynamicMultiClusterManager) Start(ctx context.Context) error {
	return dmcm.manager.Start(ctx)
}

// StartController is like pre-seeding the new controller, adding it to the DMCM and
// then starting it in its own goroutine.
func (dmcm *DynamicMultiClusterManager) StartController(ctx context.Context, log *zap.SugaredLogger, controller mcmanager.Runnable) error {
	// we need some sort of map key, but it does not matter what that key is
	mapKey := uuid.New().String()

	// keep track of this controller so we can forward the Engage() calls to it
	dmcm.runnablesLock.Lock()
	defer dmcm.runnablesLock.Unlock()
	dmcm.runnables[mapKey] = controller

	// start it
	log = log.With("component", "DynamicMultiClusterManager", "dmcmkey", mapKey)

	go func() {
		log.Debug("Starting controller")
		if err := controller.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Errorw("Controller failed to start", zap.Error(err))
		}

		log.Debug("Removing controller")
		dmcm.runnablesLock.Lock()
		delete(dmcm.runnables, mapKey)
		dmcm.runnablesLock.Unlock()
	}()

	// pre-seed the controller
	if err := dmcm.tracker.PreSeedController(controller); err != nil {
		return fmt.Errorf("failed to pre-seed controller: %w", err)
	}

	return nil
}

type engagedCluster struct {
	ctx context.Context
	cl  cluster.Cluster
}

// clusterTracker is a dummy controller, which doesn't do anything besides keeping
// track of all Engage() calls, so the DMCM can pre-seed new controllers.
type clusterTracker struct {
	dynManager *DynamicMultiClusterManager
	lock       sync.RWMutex
	clusters   map[string]engagedCluster
}

func newClusterTracker(dmcm *DynamicMultiClusterManager) *clusterTracker {
	return &clusterTracker{
		dynManager: dmcm,
		lock:       sync.RWMutex{},
		clusters:   map[string]engagedCluster{},
	}
}

func (s *clusterTracker) Start(_ context.Context) error {
	// NOP, this shim doesn't need any further setup
	return nil
}

func (s *clusterTracker) Engage(ctx context.Context, clusterName string, cl cluster.Cluster) error {
	if _, ok := s.clusters[clusterName]; !ok {
		s.lock.Lock()
		s.clusters[clusterName] = engagedCluster{ctx: ctx, cl: cl}
		s.lock.Unlock()

		// start a goroutine to make sure we remove the cluster when the context is done
		go func() {
			<-ctx.Done()

			s.lock.Lock()
			defer s.lock.Unlock()

			delete(s.clusters, clusterName)
		}()
	}

	// forward the Engage() call to all running controllers
	s.dynManager.runnablesLock.RLock()
	defer s.dynManager.runnablesLock.RUnlock()

	for _, ctrl := range s.dynManager.runnables {
		if err := ctrl.Engage(ctx, clusterName, cl); err != nil {
			return err
		}
	}

	return nil
}

func (s *clusterTracker) PreSeedController(controller mcmanager.Runnable) error {
	s.lock.RLock()
	defer s.lock.RUnlock()

	for name, engaged := range s.clusters {
		if err := controller.Engage(engaged.ctx, name, engaged.cl); err != nil {
			return fmt.Errorf("failed to engage with cluster: %w", err)
		}
	}

	return nil
}
