/*
Copyright 2026 The KCP Authors.

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
	"testing"
	"time"

	"go.uber.org/zap"

	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

type engagingRunnable struct{ engaged chan context.Context }

func (e engagingRunnable) Start(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (e engagingRunnable) Engage(ctx context.Context, _ multicluster.ClusterName, _ cluster.Cluster) error {
	e.engaged <- ctx
	return nil
}

func TestStoppedControllerIsDisengaged(t *testing.T) {
	for _, preSeeded := range []bool{true, false} {
		dmcm := &DynamicMultiClusterManager{runnables: map[string]dynamicRunnable{}}
		dmcm.tracker = newClusterTracker(dmcm)

		clusterCtx, disconnect := context.WithCancel(context.Background())
		defer disconnect()

		if preSeeded {
			if err := dmcm.tracker.Engage(clusterCtx, "cluster", nil); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		}

		ctrl := engagingRunnable{engaged: make(chan context.Context, 1)}
		ctrlCtx, stop := context.WithCancel(context.Background())

		if err := dmcm.StartController(ctrlCtx, zap.NewNop().Sugar(), ctrl); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !preSeeded {
			if err := dmcm.tracker.Engage(clusterCtx, "cluster", nil); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		}

		engagement := <-ctrl.engaged
		if engagement.Err() != nil {
			t.Fatal("engagement should be live while controller and cluster are")
		}

		stop()

		select {
		case <-engagement.Done():
		case <-time.After(5 * time.Second):
			t.Fatalf("engagement (pre-seeded: %v) must end when the controller is stopped", preSeeded)
		}

		if clusterCtx.Err() != nil {
			t.Fatal("stopping a controller must not affect the cluster")
		}
	}
}

func TestDisconnectedClusterIsDisengaged(t *testing.T) {
	dmcm := &DynamicMultiClusterManager{runnables: map[string]dynamicRunnable{}}
	dmcm.tracker = newClusterTracker(dmcm)

	ctrl := engagingRunnable{engaged: make(chan context.Context, 1)}
	ctrlCtx, stop := context.WithCancel(context.Background())
	defer stop()

	if err := dmcm.StartController(ctrlCtx, zap.NewNop().Sugar(), ctrl); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	clusterCtx, disconnect := context.WithCancel(context.Background())
	if err := dmcm.tracker.Engage(clusterCtx, "cluster", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	engagement := <-ctrl.engaged
	disconnect()

	select {
	case <-engagement.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("engagement must end when the cluster disconnects")
	}

	if ctrlCtx.Err() != nil {
		t.Fatal("a disconnecting cluster must not stop the controller")
	}
}
