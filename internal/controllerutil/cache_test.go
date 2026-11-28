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

package controllerutil

import (
	"context"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

type fakeRegistration struct{}

func (fakeRegistration) HasSynced() bool { return true }

func (fakeRegistration) HasSyncedChecker() toolscache.DoneChecker { return nil }

type fakeInformer struct {
	cache.Informer

	lock          sync.Mutex
	registrations map[toolscache.ResourceEventHandlerRegistration]struct{}
}

func (f *fakeInformer) AddEventHandler(toolscache.ResourceEventHandler) (toolscache.ResourceEventHandlerRegistration, error) {
	return f.add()
}

func (f *fakeInformer) AddEventHandlerWithResyncPeriod(toolscache.ResourceEventHandler, time.Duration) (toolscache.ResourceEventHandlerRegistration, error) {
	return f.add()
}

func (f *fakeInformer) AddEventHandlerWithOptions(toolscache.ResourceEventHandler, toolscache.HandlerOptions) (toolscache.ResourceEventHandlerRegistration, error) {
	return f.add()
}

func (f *fakeInformer) add() (toolscache.ResourceEventHandlerRegistration, error) {
	f.lock.Lock()
	defer f.lock.Unlock()

	registration := &fakeRegistration{}
	f.registrations[registration] = struct{}{}

	return registration, nil
}

func (f *fakeInformer) RemoveEventHandler(registration toolscache.ResourceEventHandlerRegistration) error {
	f.lock.Lock()
	defer f.lock.Unlock()

	delete(f.registrations, registration)

	return nil
}

func (f *fakeInformer) numHandlers() int {
	f.lock.Lock()
	defer f.lock.Unlock()

	return len(f.registrations)
}

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

func TestKindSourceRemovesHandlerWhenStopped(t *testing.T) {
	informer := &fakeInformer{registrations: map[toolscache.ResourceEventHandlerRegistration]struct{}{}}

	src := source.TypedKind(HandlerRemovingCache{Cache: fakeCache{informer: informer}}, &unstructured.Unstructured{}, handler.TypedFuncs[*unstructured.Unstructured, reconcile.Request]{})
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	defer queue.ShutDown()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := src.Start(ctx, queue); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	syncCtx, syncCancel := context.WithTimeout(ctx, 5*time.Second)
	defer syncCancel()

	if err := src.WaitForSync(syncCtx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if n := informer.numHandlers(); n != 1 {
		t.Fatalf("expected 1 handler while the source is running, got %d", n)
	}

	cancel()

	deadline := time.Now().Add(5 * time.Second)
	for informer.numHandlers() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("handler must be removed once the source is stopped")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAllHandlerVariantsAreRemoved(t *testing.T) {
	add := map[string]func(cache.Informer) (toolscache.ResourceEventHandlerRegistration, error){
		"AddEventHandler": func(i cache.Informer) (toolscache.ResourceEventHandlerRegistration, error) {
			return i.AddEventHandler(toolscache.ResourceEventHandlerFuncs{})
		},
		"AddEventHandlerWithResyncPeriod": func(i cache.Informer) (toolscache.ResourceEventHandlerRegistration, error) {
			return i.AddEventHandlerWithResyncPeriod(toolscache.ResourceEventHandlerFuncs{}, time.Minute)
		},
		"AddEventHandlerWithOptions": func(i cache.Informer) (toolscache.ResourceEventHandlerRegistration, error) {
			return i.AddEventHandlerWithOptions(toolscache.ResourceEventHandlerFuncs{}, toolscache.HandlerOptions{})
		},
	}

	for name, addHandler := range add {
		t.Run(name, func(t *testing.T) {
			informer := &fakeInformer{registrations: map[toolscache.ResourceEventHandlerRegistration]struct{}{}}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			wrapped, err := HandlerRemovingCache{Cache: fakeCache{informer: informer}}.GetInformer(ctx, &unstructured.Unstructured{})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if _, err := addHandler(wrapped); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if n := informer.numHandlers(); n != 1 {
				t.Fatalf("expected 1 handler while the context is alive, got %d", n)
			}

			cancel()

			deadline := time.Now().Add(5 * time.Second)
			for informer.numHandlers() != 0 {
				if time.Now().After(deadline) {
					t.Fatal("handler must be removed once the context is done")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}
