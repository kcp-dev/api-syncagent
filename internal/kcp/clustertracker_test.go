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
	"fmt"
	"sync"
	"testing"
	"time"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

func newTestDMCM() *DynamicMultiClusterManager {
	dmcm := &DynamicMultiClusterManager{}
	dmcm.tracker = newClusterTracker(dmcm)

	return dmcm
}

func TestConcurrentClusterEngagements(t *testing.T) {
	dmcm := newTestDMCM()

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			clusterCtx, disconnect := context.WithCancel(context.Background())
			defer disconnect()

			if err := dmcm.tracker.Engage(clusterCtx, multicluster.ClusterName(fmt.Sprintf("cluster-%d", i%5)), nil); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
	wg.Wait()
}

func TestReEngagedClusterStaysTracked(t *testing.T) {
	dmcm := newTestDMCM()

	oldCtx, disconnectOld := context.WithCancel(context.Background())
	if err := dmcm.tracker.Engage(oldCtx, "cluster", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	newCtx, disconnectNew := context.WithCancel(context.Background())
	defer disconnectNew()

	if err := dmcm.tracker.Engage(newCtx, "cluster", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// the old engagement ending must not remove the new one
	disconnectOld()
	time.Sleep(100 * time.Millisecond)

	dmcm.tracker.lock.RLock()
	defer dmcm.tracker.lock.RUnlock()

	// controllers started from now on are pre-seeded from this entry
	engaged, ok := dmcm.tracker.clusters["cluster"]
	if !ok {
		t.Fatal("the re-engaged cluster is no longer tracked")
	}
	if engaged.ctx != newCtx {
		t.Fatal("the tracked engagement must be the live one")
	}
}

func TestEndedEngagementDoesNotReplaceLiveOne(t *testing.T) {
	dmcm := newTestDMCM()

	liveCtx, disconnect := context.WithCancel(context.Background())
	defer disconnect()

	if err := dmcm.tracker.Engage(liveCtx, "cluster", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	endedCtx, end := context.WithCancel(context.Background())
	end()

	if err := dmcm.tracker.Engage(endedCtx, "cluster", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	dmcm.tracker.lock.RLock()
	defer dmcm.tracker.lock.RUnlock()

	engaged, ok := dmcm.tracker.clusters["cluster"]
	if !ok {
		t.Fatal("the live engagement is no longer tracked")
	}
	if engaged.ctx != liveCtx {
		t.Fatal("the tracked engagement must be the live one")
	}
}
