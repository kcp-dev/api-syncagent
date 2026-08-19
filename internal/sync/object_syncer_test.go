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

package sync

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/record"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// fakeStateStore is a minimal ObjectStateStore for unit tests.
type fakeStateStore struct {
	lastKnown *unstructured.Unstructured
}

func (f *fakeStateStore) Get(_ context.Context, _ syncSide) (*unstructured.Unstructured, error) {
	return f.lastKnown, nil
}

func (f *fakeStateStore) Put(_ context.Context, _ *unstructured.Unstructured, _ logicalcluster.Name, _ []string) error {
	return nil
}

func makeUnstructuredWithStatus(name, namespace string, status map[string]interface{}) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("test.example.com/v1")
	obj.SetKind("Widget")
	obj.SetName(name)
	obj.SetNamespace(namespace)
	if status != nil {
		if err := unstructured.SetNestedMap(obj.Object, status, "status"); err != nil {
			panic(err)
		}
	}
	return obj
}

func TestHandleDeletionUsesPropagationPolicy(t *testing.T) {
	testcases := []struct {
		name     string
		policy   metav1.DeletionPropagation
		expected metav1.DeletionPropagation
	}{
		{
			name:     "foreground",
			policy:   metav1.DeletePropagationForeground,
			expected: metav1.DeletePropagationForeground,
		},
		{
			name:     "orphan",
			policy:   metav1.DeletePropagationOrphan,
			expected: metav1.DeletePropagationOrphan,
		},
		{
			name:     "background",
			policy:   metav1.DeletePropagationBackground,
			expected: metav1.DeletePropagationBackground,
		},
		{
			name:     "background by default",
			expected: metav1.DeletePropagationBackground,
		},
	}

	for _, testcase := range testcases {
		t.Run(testcase.name, func(t *testing.T) {
			destination := makeUnstructuredWithStatus("destination", "default", nil)

			var got *metav1.DeletionPropagation
			destinationClient := newFakeClientBuilder().
				WithObjects(destination).
				WithInterceptorFuncs(interceptor.Funcs{
					Delete: func(ctx context.Context, client ctrlruntimeclient.WithWatch, obj ctrlruntimeclient.Object, options ...ctrlruntimeclient.DeleteOption) error {
						deleteOptions := &ctrlruntimeclient.DeleteOptions{}
						for _, option := range options {
							option.ApplyToDelete(deleteOptions)
						}
						got = deleteOptions.PropagationPolicy
						return client.Delete(ctx, obj, options...)
					},
				}).
				Build()

			source := makeUnstructuredWithStatus("source", "default", nil)
			source.SetFinalizers([]string{deletionFinalizer})
			now := metav1.Now()
			source.SetDeletionTimestamp(&now)

			syncer := objectSyncer{
				blockSourceDeletion:       true,
				deletionPropagationPolicy: testcase.policy,
				eventObjSide:              syncSideSource,
			}
			ctx := WithEventRecorder(t.Context(), record.NewFakeRecorder(1))

			requeue, err := syncer.handleDeletion(ctx, zap.NewNop().Sugar(), syncSide{object: source}, syncSide{
				client: destinationClient,
				object: destination,
			})
			if err != nil {
				t.Fatalf("handleDeletion returned an error: %v", err)
			}
			if !requeue {
				t.Fatal("handleDeletion did not request a requeue")
			}
			if got == nil || *got != testcase.expected {
				t.Fatalf("expected %q propagation policy, got %v", testcase.expected, got)
			}
		})
	}
}

func TestSyncObjectStatusForward(t *testing.T) {
	log := zap.NewNop().Sugar()

	t.Run("no-op when syncStatusForward is false", func(t *testing.T) {
		source := makeUnstructuredWithStatus("src", "default", map[string]interface{}{"phase": "ready"})
		dest := makeUnstructuredWithStatus("dst", "default", nil)
		destClient := buildFakeClientWithStatus(dest)

		syncer := &objectSyncer{syncStatusForward: false}
		ctx := WithEventRecorder(t.Context(), record.NewFakeRecorder(10))

		err := syncer.syncObjectStatusForward(ctx, log, syncSide{object: source}, syncSide{object: dest, client: destClient})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		got := dest.UnstructuredContent()["status"]
		if got != nil {
			t.Errorf("expected dest status to remain nil, got %v", got)
		}
	})

	t.Run("no-op when dest object is nil", func(t *testing.T) {
		source := makeUnstructuredWithStatus("src", "default", map[string]interface{}{"phase": "ready"})

		syncer := &objectSyncer{syncStatusForward: true}
		ctx := WithEventRecorder(t.Context(), record.NewFakeRecorder(10))

		err := syncer.syncObjectStatusForward(ctx, log, syncSide{object: source}, syncSide{object: nil})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("syncs status from source to dest when they differ", func(t *testing.T) {
		source := makeUnstructuredWithStatus("src", "default", map[string]interface{}{"phase": "ready"})
		dest := makeUnstructuredWithStatus("dst", "default", nil)
		destClient := buildFakeClientWithStatus(dest)

		syncer := &objectSyncer{syncStatusForward: true, eventObjSide: syncSideSource}
		ctx := WithEventRecorder(t.Context(), record.NewFakeRecorder(10))

		err := syncer.syncObjectStatusForward(ctx, log, syncSide{object: source}, syncSide{object: dest, client: destClient})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		phase, _, _ := unstructured.NestedString(dest.Object, "status", "phase")
		if phase != "ready" {
			t.Errorf("expected dest status.phase=ready, got %q", phase)
		}
	})

	t.Run("no-op when status is already equal", func(t *testing.T) {
		status := map[string]interface{}{"phase": "ready"}
		source := makeUnstructuredWithStatus("src", "default", status)
		dest := makeUnstructuredWithStatus("dst", "default", status)
		destClient := buildFakeClientWithStatus(dest)

		syncer := &objectSyncer{syncStatusForward: true}
		ctx := WithEventRecorder(t.Context(), record.NewFakeRecorder(10))

		err := syncer.syncObjectStatusForward(ctx, log, syncSide{object: source}, syncSide{object: dest, client: destClient})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("emits warning and returns nil when dest has no status subresource (404)", func(t *testing.T) {
		source := makeUnstructuredWithStatus("src", "default", map[string]interface{}{"phase": "ready"})
		dest := makeUnstructuredWithStatus("dst", "default", nil)

		// Build a client that does NOT register the status subresource, so Status().Update() → 404.
		destClient := buildFakeClient(dest)

		syncer := &objectSyncer{syncStatusForward: true, eventObjSide: syncSideSource}
		recorder := record.NewFakeRecorder(10)
		ctx := WithEventRecorder(t.Context(), recorder)

		err := syncer.syncObjectStatusForward(ctx, log, syncSide{object: source}, syncSide{object: dest, client: destClient})
		if err != nil {
			t.Fatalf("expected no error on 404, got: %v", err)
		}

		select {
		case event := <-recorder.Events:
			if event == "" {
				t.Error("expected a Warning event to be emitted")
			}
		default:
			t.Error("expected a Warning event to be emitted but channel was empty")
		}
	})
}

// TestSyncObjectContentsForwardStatusRunsEvenOnSpecRequeue verifies the key invariant:
// syncObjectStatusForward is called unconditionally in syncObjectContents, even when
// syncObjectSpec already returned requeue=true due to a pending spec change.
// A future refactor that re-introduces an early return after spec sync would silently
// break status sync for simultaneous spec+status changes.
func TestSyncObjectContentsForwardStatusRunsEvenOnSpecRequeue(t *testing.T) {
	log := zap.NewNop().Sugar()

	// Source: new spec + status set.
	source := &unstructured.Unstructured{}
	source.SetAPIVersion("test.example.com/v1")
	source.SetKind("Widget")
	source.SetName("my-widget")
	source.SetNamespace("default")
	if err := unstructured.SetNestedField(source.Object, "new-value", "spec", "username"); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(source.Object, "ready", "status", "phase"); err != nil {
		t.Fatal(err)
	}

	// Dest: old spec, no status.
	dest := &unstructured.Unstructured{}
	dest.SetAPIVersion("test.example.com/v1")
	dest.SetKind("Widget")
	dest.SetName("my-widget")
	dest.SetNamespace("default")
	if err := unstructured.SetNestedField(dest.Object, "old-value", "spec", "username"); err != nil {
		t.Fatal(err)
	}

	destClient := buildFakeClientWithStatus(dest)

	// Last known source state has old spec — so syncObjectSpec sees a diff and patches, returning requeue=true.
	lastKnown := &unstructured.Unstructured{}
	lastKnown.SetAPIVersion("test.example.com/v1")
	lastKnown.SetKind("Widget")
	lastKnown.SetName("my-widget")
	if err := unstructured.SetNestedField(lastKnown.Object, "old-value", "spec", "username"); err != nil {
		t.Fatal(err)
	}

	syncer := &objectSyncer{
		stateStore:        &fakeStateStore{lastKnown: lastKnown},
		syncStatusForward: true,
		subresources:      []string{"status"},
		destCreator:       func(u *unstructured.Unstructured) (*unstructured.Unstructured, error) { return u.DeepCopy(), nil },
		eventObjSide:      syncSideSource,
	}

	recorder := record.NewFakeRecorder(10)
	ctx := WithEventRecorder(t.Context(), recorder)
	ctx = WithClusterName(ctx, logicalcluster.Name("testcluster"))

	sourceSide := syncSide{object: source, clusterName: "testcluster"}
	destSide := syncSide{object: dest, client: destClient}

	requeue, err := syncer.syncObjectContents(ctx, log, sourceSide, destSide)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !requeue {
		t.Error("expected requeue=true because spec changed, but got false")
	}

	// Even though spec sync returned requeue=true, the dest status must have been updated.
	phase, _, _ := unstructured.NestedString(dest.Object, "status", "phase")
	if phase != "ready" {
		t.Errorf("expected dest status.phase=ready after syncObjectContents, got %q — forward status sync did not run on spec requeue", phase)
	}
}

func TestEnsureDestMetadataPersisted(t *testing.T) {
	log := zap.NewNop().Sugar()

	makeCopy := func() *unstructured.Unstructured {
		obj := &unstructured.Unstructured{}
		obj.SetAPIVersion("test.example.com/v1")
		obj.SetKind("Widget")
		obj.SetName("my-widget")
		obj.SetNamespace("default")
		return obj
	}

	destLabels := map[string]string{
		relatedOwnerLabel:      "testcluster",
		relatedIdentifierLabel: "credentials",
	}
	destAnnotations := map[string]string{
		relatedPrimaryNameAnnotation: "my-primary",
	}

	t.Run("backfills provenance metadata onto a pre-existing copy and is idempotent", func(t *testing.T) {
		// A copy that predates the feature: it exists on the destination but carries none of the
		// provenance labels/annotations the prune relies on.
		dest := makeCopy()
		destClient := buildFakeClient(dest)

		syncer := &objectSyncer{destLabels: destLabels, destAnnotations: destAnnotations}
		destSide := syncSide{object: dest, client: destClient}

		updated, err := syncer.ensureDestMetadataPersisted(t.Context(), log, destSide)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !updated {
			t.Fatal("expected updated=true because the copy was missing the provenance metadata")
		}

		// The change must be persisted to the API, not just to the in-memory object.
		persisted := makeCopy()
		if err := destClient.Get(t.Context(), ctrlruntimeclient.ObjectKeyFromObject(dest), persisted); err != nil {
			t.Fatalf("failed to get persisted object: %v", err)
		}
		for k, v := range destLabels {
			if persisted.GetLabels()[k] != v {
				t.Errorf("expected persisted label %q=%q, got %q", k, v, persisted.GetLabels()[k])
			}
		}
		if persisted.GetAnnotations()[relatedPrimaryNameAnnotation] != "my-primary" {
			t.Errorf("expected persisted annotation, got %q", persisted.GetAnnotations()[relatedPrimaryNameAnnotation])
		}

		// A second call must be a no-op now that the metadata is present.
		updated, err = syncer.ensureDestMetadataPersisted(t.Context(), log, syncSide{object: persisted, client: destClient})
		if err != nil {
			t.Fatalf("unexpected error on second call: %v", err)
		}
		if updated {
			t.Error("expected updated=false on the second call (idempotent), but got true")
		}
	})

	t.Run("no-op when the syncer has no additional destination metadata", func(t *testing.T) {
		dest := makeCopy()
		destClient := buildFakeClient(dest)

		syncer := &objectSyncer{}

		updated, err := syncer.ensureDestMetadataPersisted(t.Context(), log, syncSide{object: dest, client: destClient})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if updated {
			t.Error("expected updated=false when there are no destLabels/destAnnotations")
		}
	})
}
