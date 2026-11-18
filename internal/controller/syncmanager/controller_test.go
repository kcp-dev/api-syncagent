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
	"errors"
	"testing"

	"go.uber.org/zap"

	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var testName = types.NamespacedName{Name: "my-pr"}

type fakeManager struct {
	manager.Manager
	client ctrlruntimeclient.Client
}

func (m fakeManager) GetClient() ctrlruntimeclient.Client {
	return m.client
}

func newTestReconciler(t *testing.T, objects ...ctrlruntimeclient.Object) *Reconciler {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := syncagentv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to build scheme: %v", err)
	}

	return &Reconciler{
		ctx:          context.Background(),
		log:          zap.NewNop().Sugar(),
		localManager: fakeManager{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()},
		syncCancels:  map[types.NamespacedName]syncCancel{},
	}
}

func recordTestController(r *Reconciler, key string) context.Context {
	ctx, cancel := context.WithCancelCause(context.Background())
	r.recordController(r.log, testName, key, cancel)

	return ctx
}

func TestChangedPublishedResourceReplacesController(t *testing.T) {
	r := newTestReconciler(t)

	oldCtx := recordTestController(r, "uid-1")
	newCtx := recordTestController(r, "uid-2")

	if oldCtx.Err() == nil {
		t.Error("Controller for the old version of the PublishedResource must be stopped.")
	}

	if newCtx.Err() != nil {
		t.Error("Controller for the new version of the PublishedResource must keep running.")
	}

	if current := r.syncCancels[testName]; current.key != "uid-2" {
		t.Errorf("Expected controller for the new version to be recorded, got %q.", current.key)
	}
}

func TestFailedStartKeepsOutdatedController(t *testing.T) {
	r := newTestReconciler(t)

	oldCtx := recordTestController(r, "uid-1")

	newCtx, newCancel := context.WithCancelCause(context.Background())
	if err := r.startAndRecord(r.log, testName, "uid-2", newCancel, func() error { return errors.New("boom") }); err == nil {
		t.Fatal("Expected an error.")
	}

	if oldCtx.Err() != nil {
		t.Error("Controller for the old version must keep running when its successor failed to start.")
	}

	if newCtx.Err() == nil {
		t.Error("Controller that failed to start must be cancelled.")
	}

	if current := r.syncCancels[testName]; current.key != "uid-1" {
		t.Errorf("Expected the old controller to stay recorded so that the start is retried, got %q.", current.key)
	}

	// the retry succeeds and replaces the old controller
	_, retryCancel := context.WithCancelCause(context.Background())
	if err := r.startAndRecord(r.log, testName, "uid-2", retryCancel, func() error { return nil }); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if oldCtx.Err() == nil {
		t.Error("Controller for the old version must be stopped once its successor has started.")
	}

	if current := r.syncCancels[testName]; current.key != "uid-2" {
		t.Errorf("Expected controller for the new version to be recorded, got %q.", current.key)
	}
}

func TestDeletedPublishedResourceStopsController(t *testing.T) {
	r := newTestReconciler(t)

	ctx := recordTestController(r, "uid-1")

	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: testName}); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if ctx.Err() == nil {
		t.Error("Controller of a deleted PublishedResource must be stopped.")
	}

	if _, exists := r.syncCancels[testName]; exists {
		t.Error("Controller of a deleted PublishedResource must be forgotten.")
	}
}

func TestPublishedResourceInDeletionStopsController(t *testing.T) {
	now := metav1.Now()
	pubRes := &syncagentv1alpha1.PublishedResource{
		ObjectMeta: metav1.ObjectMeta{
			Name:              testName.Name,
			UID:               "uid",
			DeletionTimestamp: &now,
			// the fake client refuses objects with a deletionTimestamp but no finalizers
			Finalizers: []string{"example.com/test"},
		},
	}

	r := newTestReconciler(t, pubRes)

	// the controller was started for an older version, before the deletionTimestamp was set
	ctx := recordTestController(r, "uid-1")

	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: testName}); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if ctx.Err() == nil {
		t.Error("Controller of a PublishedResource in deletion must be stopped.")
	}

	if _, exists := r.syncCancels[testName]; exists {
		t.Error("Controller of a PublishedResource in deletion must be forgotten.")
	}
}
