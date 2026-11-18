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

package syncmanager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/kcp-dev/api-syncagent/internal/discovery"
	"github.com/kcp-dev/api-syncagent/internal/kcp"
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	kcpapisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
)

type testManager struct {
	manager.Manager
	c     ctrlruntimeclient.Client
	cache cache.Cache
}

func (m testManager) GetAPIReader() ctrlruntimeclient.Reader { return m.c }

func (m testManager) GetClient() ctrlruntimeclient.Client { return m.c }
func (m testManager) GetCache() cache.Cache               { return m.cache }

type testCache struct {
	cache.Cache
	calls atomic.Int32
}

func (c *testCache) GetInformer(context.Context, ctrlruntimeclient.Object, ...cache.InformerGetOption) (cache.Informer, error) {
	c.calls.Add(1)
	return testInformer{fail: c.calls.Load() == 1}, nil
}

type testInformer struct {
	cache.Informer
	fail bool
}

func (c *testCache) WaitForCacheSync(context.Context) bool { return true }

type syncedRegistration struct {
	toolscache.ResourceEventHandlerRegistration
}

func (syncedRegistration) HasSynced() bool { return true }

func (i testInformer) AddEventHandlerWithOptions(toolscache.ResourceEventHandler, toolscache.HandlerOptions) (toolscache.ResourceEventHandlerRegistration, error) {
	if i.fail {
		return nil, errors.New("injected source startup failure")
	}
	return syncedRegistration{}, nil
}

func newDiscoveryServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body string
		switch r.URL.Path {
		case "/api":
			body = `{"kind":"APIVersions","apiVersion":"v1","versions":[]}`
		case "/apis":
			body = `{"kind":"APIGroupList","apiVersion":"v1","groups":[{"name":"apis.kcp.io","versions":[{"groupVersion":"apis.kcp.io/v1alpha1","version":"v1alpha1"}],"preferredVersion":{"groupVersion":"apis.kcp.io/v1alpha1","version":"v1alpha1"}},{"name":"repro.example","versions":[{"groupVersion":"repro.example/v1","version":"v1"}],"preferredVersion":{"groupVersion":"repro.example/v1","version":"v1"}}]}`
		case "/apis/apis.kcp.io/v1alpha1":
			body = `{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"apis.kcp.io/v1alpha1","resources":[{"name":"apiexportendpointslices","kind":"APIExportEndpointSlice","namespaced":false,"verbs":["get","list","watch"]}]}`
		case "/apis/repro.example/v1":
			body = `{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"repro.example/v1","resources":[{"name":"widgets","kind":"Widget","namespaced":true,"verbs":["get","list","watch"]}]}`
		case "/apis/apiextensions.k8s.io/v1/customresourcedefinitions/widgets.repro.example":
			body = `{"kind":"CustomResourceDefinition","apiVersion":"apiextensions.k8s.io/v1","metadata":{"name":"widgets.repro.example"},"spec":{"group":"repro.example","names":{"kind":"Widget","plural":"widgets"},"scope":"Namespaced","versions":[{"name":"v1","served":true,"storage":true}]}}`
		default:
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestSyncControllerRecovery(t *testing.T) {
	server := newDiscoveryServer(t)
	cfg := &rest.Config{Host: server.URL}
	disc, err := discovery.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dmcm, err := kcp.NewDynamicMultiClusterManager(cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	if err := kcpapisv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	local := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&kcpapisv1alpha1.APIExportEndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "test"}}).Build()
	pr := &syncagentv1alpha1.PublishedResource{}
	if err := json.Unmarshal([]byte(`{"metadata":{"name":"widgets","uid":"test-uid","resourceVersion":"1"},"spec":{"resource":{"apiGroup":"repro.example","kind":"Widget","versions":["v1"]}}}`), pr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c := &testCache{}
	core, logs := observer.New(zap.DebugLevel)
	log := zap.New(core).Sugar()
	r := &Reconciler{
		ctx:              ctx,
		localManager:     testManager{c: local, cache: c},
		dmcm:             dmcm,
		log:              log,
		discoveryClient:  disc,
		resourceProber:   discovery.NewResourceProber(cfg, local, "test"),
		syncControllers:  map[string]*syncController{},
		controllerEvents: make(chan event.TypedGenericEvent[*syncagentv1alpha1.PublishedResource], 1),
	}
	result, err := r.ensureSyncController(ctx, log, pr)
	if err != nil || !result.IsZero() {
		t.Fatalf("initial create: %v %v", result, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for logs.FilterMessage("Removing controller").Len() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("controller did not fail: %+v", logs.All())
		}
		time.Sleep(time.Millisecond)
	}
	if logs.FilterMessage("Controller failed to start").Len() != 1 {
		t.Fatalf("missing failure: %+v", logs.All())
	}

	select {
	case e := <-r.controllerEvents:
		if e.Object.Name != pr.Name {
			t.Fatalf("unexpected event: %v", e.Object.Name)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("failed controller did not request reconciliation")
	}
	result, err = r.ensureSyncController(ctx, log, pr)
	if err != nil || result.RequeueAfter <= 0 {
		t.Fatalf("expected delayed retry, got %v, %v", result, err)
	}
	// The retry must work for exactly the same UID and resourceVersion.
	select {
	case <-time.After(result.RequeueAfter):
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err = r.ensureSyncController(ctx, log, pr); err != nil {
		t.Fatal(err)
	}
	if logs.FilterMessage("Creating new sync controller…").Len() != 2 {
		t.Fatal("unchanged PublishedResource did not recreate failed controller")
	}
	deadline = time.Now().Add(5 * time.Second)
	for logs.FilterMessage("Starting workers").Len() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("replacement did not start workers")
		}
		time.Sleep(time.Millisecond)
	}
	if result, err := r.ensureSyncController(ctx, log, pr); err != nil || !result.IsZero() {
		t.Fatalf("healthy controller: %v %v", result, err)
	}
	if logs.FilterMessage("Creating new sync controller…").Len() != 2 {
		t.Fatal("duplicated healthy controller")
	}
	cancel()
	select {
	case <-r.syncControllers[getPublishedResourceKey(pr)].done:
	case <-time.After(5 * time.Second):
		t.Fatal("replacement did not stop")
	}
}

type testRunnable struct {
	mcmanager.Runnable
	start func(context.Context) error
}

func (r testRunnable) Start(ctx context.Context) error { return r.start(ctx) }

func TestSyncControllerExit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cancel bool
		err    error
	}{
		{name: "failure", err: errors.New("startup failed")},
		{name: "unexpected clean exit"},
		{name: "shutdown", cancel: true, err: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			notified := false
			c := &syncController{Runnable: testRunnable{start: func(context.Context) error { return tc.err }}, cancel: cancel, done: make(chan struct{}), notify: func() { notified = true }}
			if tc.cancel {
				cancel(errors.New("intentional shutdown"))
			}
			if err := c.Start(ctx); !errors.Is(err, tc.err) {
				t.Fatalf("unexpected error: %v", err)
			}
			if notified == tc.cancel {
				t.Fatalf("notified=%v, intentional cancellation=%v", notified, tc.cancel)
			}
			select {
			case <-c.done:
			default:
				t.Fatal("completion not recorded")
			}
			if ctx.Err() == nil {
				t.Fatal("controller context was not cancelled")
			}
		})
	}
}

func TestCleanupDoesNotRemoveReplacement(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	old := &syncController{Runnable: testRunnable{start: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }}, cancel: cancel, done: make(chan struct{}), notify: func() { t.Error("intentional cleanup requested retry") }}
	pr := &syncagentv1alpha1.PublishedResource{ObjectMeta: metav1.ObjectMeta{Name: "test", UID: "uid", ResourceVersion: "1"}}
	key := getPublishedResourceKey(pr)
	r := &Reconciler{syncControllers: map[string]*syncController{key: old}}
	if err := r.cleanupController(zap.NewNop().Sugar(), pr); err != nil {
		t.Fatal(err)
	}
	replacement := &syncController{done: make(chan struct{})}
	r.syncControllers[key] = replacement
	if err := old.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if r.syncControllers[key] != replacement {
		t.Fatal("old completion removed replacement")
	}
}

func TestNextRetryDelay(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name    string
		delay   time.Duration
		running time.Duration
		want    time.Duration
	}{
		{name: "doubles", delay: syncControllerMinRetryDelay, want: 2 * syncControllerMinRetryDelay},
		{name: "capped", delay: syncControllerMaxRetryDelay, want: syncControllerMaxRetryDelay},
		{name: "resets after a healthy run", delay: syncControllerMaxRetryDelay, running: syncControllerMaxRetryDelay, want: syncControllerMinRetryDelay},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &syncController{retryDelay: tc.delay, startedAt: now.Add(-tc.running), finishedAt: now}
			if got := nextRetryDelay(c); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
