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
	"runtime"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/kcp-dev/api-syncagent/internal/controllerutil"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	toolscache "k8s.io/client-go/tools/cache"
	fcache "k8s.io/client-go/tools/cache/testing"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	mccontroller "sigs.k8s.io/multicluster-runtime/pkg/controller"
	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
	mcsource "sigs.k8s.io/multicluster-runtime/pkg/source"
)

type fakeCache struct {
	cache.Cache
	informer cache.Informer
}

func (f fakeCache) GetInformer(context.Context, ctrlruntimeclient.Object, ...cache.InformerGetOption) (cache.Informer, error) {
	return f.informer, nil
}

func (f fakeCache) WaitForCacheSync(context.Context) bool {
	return true
}

type fakeCluster struct {
	cluster.Cluster
	cache cache.Cache
}

func (f fakeCluster) GetCache() cache.Cache {
	return f.cache
}

func runInformer(ctx context.Context, t *testing.T) cache.Informer {
	t.Helper()

	lw := fcache.NewFakeControllerSource()
	lw.Add(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "obj", Namespace: "default"}})

	informer := toolscache.NewSharedIndexInformer(lw, &corev1.ConfigMap{}, 0, toolscache.Indexers{})
	go informer.RunWithContext(ctx)

	if !toolscache.WaitForCacheSync(ctx.Done(), informer.HasSynced) {
		t.Fatal("informer did not sync")
	}

	return informer
}

// TestStoppedControllersDoNotLeak starts and stops a controller wired like a sync controller
// (a local Kind source and a per-cluster Kind source) many times and checks that no
// goroutines are left behind.
func TestStoppedControllersDoNotLeak(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dmcm := &DynamicMultiClusterManager{runnables: map[string]dynamicRunnable{}}
	dmcm.tracker = newClusterTracker(dmcm)

	localCache := fakeCache{informer: runInformer(ctx, t)}
	remoteCluster := fakeCluster{cache: fakeCache{informer: runInformer(ctx, t)}}

	if err := dmcm.tracker.Engage(ctx, "workspace", remoteCluster); err != nil {
		t.Fatalf("failed to engage cluster: %v", err)
	}

	startAndStop := func() {
		reconciled := make(chan string, 10)

		c, err := mccontroller.NewUnmanaged("leak-test", nil, mccontroller.Options{
			Reconciler: mcreconcile.Func(func(_ context.Context, req mcreconcile.Request) (reconcile.Result, error) {
				reconciled <- string(req.ClusterName)
				return reconcile.Result{}, nil
			}),
			SkipNameValidation: ptr.To(true),
		})
		if err != nil {
			t.Fatalf("failed to create controller: %v", err)
		}

		if err := c.MultiClusterWatch(mcsource.TypedKind(&corev1.ConfigMap{}, mchandler.TypedEnqueueRequestForObject[*corev1.ConfigMap]())); err != nil {
			t.Fatalf("failed to watch remote: %v", err)
		}

		if err := c.Watch(source.TypedKind(controllerutil.HandlerRemovingCache{Cache: localCache}, &corev1.ConfigMap{}, handler.TypedFuncs[*corev1.ConfigMap, mcreconcile.Request]{
			CreateFunc: func(_ context.Context, _ event.TypedCreateEvent[*corev1.ConfigMap], q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
				q.Add(mcreconcile.Request{ClusterName: "local"})
			},
		})); err != nil {
			t.Fatalf("failed to watch local: %v", err)
		}

		ctrlCtx, stop := context.WithCancel(ctx)
		exited := make(chan struct{})

		if err := dmcm.StartController(ctrlCtx, zap.NewNop().Sugar(), exitNotifyingController{Controller: c, exited: exited}); err != nil {
			t.Fatalf("failed to start controller: %v", err)
		}

		// wait until both sources have delivered events
		seen := map[string]bool{}
		for len(seen) < 2 {
			select {
			case cluster := <-reconciled:
				seen[cluster] = true
			case <-time.After(10 * time.Second):
				t.Fatalf("controller did not reconcile both sides, got %v", seen)
			}
		}

		stop()
		<-exited
	}

	startAndStop()
	time.Sleep(time.Second)
	baseline := runtime.NumGoroutine()

	const iterations = 20
	for range iterations {
		startAndStop()
	}

	n := runtime.NumGoroutine()
	for deadline := time.Now().Add(5 * time.Second); n > baseline && time.Now().Before(deadline); n = runtime.NumGoroutine() {
		time.Sleep(50 * time.Millisecond)
	}

	if n > baseline {
		t.Fatalf("expected goroutines to return to baseline %d after %d controller restarts, got %d", baseline, iterations, n)
	}

	t.Logf("goroutines after warm-up: %d, after %d restarts: %d", baseline, iterations, n)
}

// exitNotifyingController closes exited once the wrapped controller's Start() has returned.
type exitNotifyingController struct {
	mccontroller.Controller
	exited chan struct{}
}

func (c exitNotifyingController) Start(ctx context.Context) error {
	defer close(c.exited)
	return c.Controller.Start(ctx)
}
