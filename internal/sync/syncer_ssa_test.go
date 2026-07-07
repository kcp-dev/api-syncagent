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

	"github.com/kcp-dev/api-syncagent/internal/mutation"
	dummyv1alpha1 "github.com/kcp-dev/api-syncagent/internal/sync/apis/dummy/v1alpha1"
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// TestSyncerServerSideApplyCreate verifies that the SSA code path successfully
// creates a brand-new local object from a remote one. This is a smoke test for
// the WithServerSideApply() option; the actual field-manager-aware merge
// behaviour is provided by the kube-apiserver in real clusters (and is the
// reason this option fixes Crossplane's duplicate-composite-resource issue).
func TestSyncerServerSideApplyCreate(t *testing.T) {
	const stateNamespace = "kcp-system"
	clusterName := logicalcluster.Name("testcluster")

	pubRes := &syncagentv1alpha1.PublishedResource{
		Spec: syncagentv1alpha1.PublishedResourceSpec{
			Resource: syncagentv1alpha1.SourceResourceDescriptor{
				APIGroup: dummyv1alpha1.GroupName,
				Version:  dummyv1alpha1.GroupVersion,
				Kind:     "Thing",
			},
			Projection: &syncagentv1alpha1.ResourceProjection{
				Group: "remote.example.corp",
				Kind:  "RemoteThing",
			},
			Naming: &syncagentv1alpha1.ResourceNaming{
				Name: "$remoteClusterName-$remoteName",
			},
		},
	}

	remoteObject := newUnstructured(&dummyv1alpha1.Thing{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-test-thing",
		},
		Spec: dummyv1alpha1.ThingSpec{
			Username: "Colonel Mustard",
		},
	}, withGroupKind("remote.example.corp", "RemoteThing"))

	localClient := buildFakeClientWithStatus()
	remoteClient := buildFakeClientWithStatus(remoteObject)

	syncer, err := NewResourceSyncer(
		zap.NewNop().Sugar(),
		localClient,
		remoteClient,
		pubRes,
		loadCRD("things"),
		func(*syncagentv1alpha1.ResourceMutationSpec) (mutation.Mutator, error) { return nil, nil },
		stateNamespace,
		"textor-the-doctor",
		WithServerSideApply(),
	)
	if err != nil {
		t.Fatalf("Failed to create syncer: %v", err)
	}

	ctx := t.Context()
	ctx = WithClusterName(ctx, clusterName)
	ctx = WithEventRecorder(ctx, record.NewFakeRecorder(99))

	// We loop a couple of times because the finalizer-on-source step still
	// happens once before SSA can place the destination object.
	for i := 0; i < 5; i++ {
		requeue, err := syncer.Process(ctx, remoteObject)
		if err != nil {
			t.Fatalf("Process failed on iteration %d: %v", i, err)
		}
		if !requeue {
			break
		}
		if err := remoteClient.Get(ctx, ctrlruntimeclient.ObjectKeyFromObject(remoteObject), remoteObject); err != nil {
			t.Fatalf("Failed to re-fetch remote object on iteration %d: %v", i, err)
		}
	}

	// The local object should now exist and carry the agent label + remote-name annotation.
	localKey := ctrlruntimeclient.ObjectKey{Name: "testcluster-my-test-thing"}
	got := newUnstructured(&dummyv1alpha1.Thing{})
	if err := localClient.Get(ctx, localKey, got); err != nil {
		t.Fatalf("Local object was not created: %v", err)
	}

	if got.GetLabels()[agentNameLabel] != "textor-the-doctor" {
		t.Errorf("Expected agent label %q on local object, got labels %v", "textor-the-doctor", got.GetLabels())
	}
	if got.GetAnnotations()[remoteObjectNameAnnotation] != "my-test-thing" {
		t.Errorf("Expected remote-name annotation, got annotations %v", got.GetAnnotations())
	}

	username, found, err := nestedString(got.Object, "spec", "username")
	if err != nil || !found || username != "Colonel Mustard" {
		t.Errorf("Expected spec.username=Colonel Mustard on local object, got %q (found=%v, err=%v)", username, found, err)
	}
}

// nestedString is a tiny helper to avoid pulling unstructured.NestedString into
// every test that needs to assert on a single field.
func nestedString(obj map[string]any, fields ...string) (string, bool, error) {
	current := any(obj)
	for _, f := range fields {
		m, ok := current.(map[string]any)
		if !ok {
			return "", false, nil
		}
		v, ok := m[f]
		if !ok {
			return "", false, nil
		}
		current = v
	}
	s, ok := current.(string)
	return s, ok, nil
}

// TestObjectSyncerFieldManager verifies the field-manager string used for
// Server-Side Apply. It is intentionally stable across restarts so that
// SSA-tracked field ownership is preserved between reconciles. Multi-agent
// setups (the same API synced into multiple kcp's from one service cluster)
// must each get a distinct field manager, otherwise they would overwrite each
// other's fields.
func TestObjectSyncerFieldManager(t *testing.T) {
	testcases := []struct {
		name      string
		agentName string
		expected  string
	}{
		{
			name:      "no agent name uses the bare base",
			agentName: "",
			expected:  "api-syncagent",
		},
		{
			name:      "agent name is appended after a slash",
			agentName: "textor-the-doctor",
			expected:  "api-syncagent/textor-the-doctor",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			s := &objectSyncer{agentName: tc.agentName}
			if got := s.fieldManager(); got != tc.expected {
				t.Errorf("fieldManager() = %q, want %q", got, tc.expected)
			}
		})
	}
}

// TestSyncerServerSideApplyFieldOwner verifies that the syncer issues a
// Server-Side Apply call with the expected FieldOwner and ForceOwnership
// options and that the resulting object carries managedFields owned by the
// syncer's stable field manager. This guards against regressions where
// either the on-wire SSA configuration drifts, or where future refactors
// silently fall back to a non-SSA write path (which is what historically
// caused Crossplane's duplicate-composite-resource issue).
func TestSyncerServerSideApplyFieldOwner(t *testing.T) {
	const (
		stateNamespace = "kcp-system"
		agentName      = "textor-the-doctor"
		wantFM         = "api-syncagent/textor-the-doctor"
	)
	clusterName := logicalcluster.Name("testcluster")

	pubRes := &syncagentv1alpha1.PublishedResource{
		Spec: syncagentv1alpha1.PublishedResourceSpec{
			Resource: syncagentv1alpha1.SourceResourceDescriptor{
				APIGroup: dummyv1alpha1.GroupName,
				Version:  dummyv1alpha1.GroupVersion,
				Kind:     "Thing",
			},
			Projection: &syncagentv1alpha1.ResourceProjection{
				Group: "remote.example.corp",
				Kind:  "RemoteThing",
			},
			Naming: &syncagentv1alpha1.ResourceNaming{
				Name: "$remoteClusterName-$remoteName",
			},
		},
	}

	remoteObject := newUnstructured(&dummyv1alpha1.Thing{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-test-thing",
		},
		Spec: dummyv1alpha1.ThingSpec{
			Username: "Colonel Mustard",
		},
	}, withGroupKind("remote.example.corp", "RemoteThing"))

	// Intercept Apply calls on the local (destination) client so we can
	// snapshot the ApplyOptions that the syncer used.
	type capture struct {
		fieldOwner string
		force      bool
	}
	var captures []capture

	// Build the destination fake client with managedFields tracking enabled.
	// The default builder strips managedFields on read; with this option the
	// fake client preserves them, so we can assert on the entries written by
	// the syncer's Apply call.
	underlying := newFakeClientBuilder().WithReturnManagedFields().Build()
	localClient := interceptor.NewClient(underlying, interceptor.Funcs{
		Apply: func(ctx context.Context, c ctrlruntimeclient.WithWatch, obj runtime.ApplyConfiguration, opts ...ctrlruntimeclient.ApplyOption) error {
			ao := &ctrlruntimeclient.ApplyOptions{}
			ao.ApplyOptions(opts)

			cp := capture{
				fieldOwner: ao.FieldManager,
			}
			if ao.Force != nil {
				cp.force = *ao.Force
			}
			captures = append(captures, cp)

			return c.Apply(ctx, obj, opts...)
		},
	})
	remoteClient := buildFakeClientWithStatus(remoteObject)

	syncer, err := NewResourceSyncer(
		zap.NewNop().Sugar(),
		localClient,
		remoteClient,
		pubRes,
		loadCRD("things"),
		func(*syncagentv1alpha1.ResourceMutationSpec) (mutation.Mutator, error) { return nil, nil },
		stateNamespace,
		agentName,
		WithServerSideApply(),
	)
	if err != nil {
		t.Fatalf("Failed to create syncer: %v", err)
	}

	ctx := t.Context()
	ctx = WithClusterName(ctx, clusterName)
	ctx = WithEventRecorder(ctx, record.NewFakeRecorder(99))

	for i := 0; i < 5; i++ {
		requeue, err := syncer.Process(ctx, remoteObject)
		if err != nil {
			t.Fatalf("Process failed on iteration %d: %v", i, err)
		}
		if !requeue {
			break
		}
		if err := remoteClient.Get(ctx, ctrlruntimeclient.ObjectKeyFromObject(remoteObject), remoteObject); err != nil {
			t.Fatalf("Failed to re-fetch remote object on iteration %d: %v", i, err)
		}
	}

	// 1. Assert on the on-wire SSA configuration: every Apply call must use the
	//    syncer's stable field manager and must set ForceOwnership=true.
	if len(captures) == 0 {
		t.Fatalf("Expected at least one Apply call on the destination client; none were captured")
	}
	for i, c := range captures {
		if c.fieldOwner != wantFM {
			t.Errorf("Apply call %d used FieldOwner %q, want %q", i, c.fieldOwner, wantFM)
		}
		if !c.force {
			t.Errorf("Apply call %d did not set ForceOwnership=true (captured force=%v)", i, c.force)
		}
	}

	// 2. Assert on the resulting object: managedFields must contain an entry
	//    owned by the syncer with Operation=Apply. This is what actually
	//    causes the kube-apiserver to preserve fields owned by other
	//    controllers (e.g. Crossplane's spec.resourceRef writes).
	localKey := ctrlruntimeclient.ObjectKey{Name: "testcluster-my-test-thing"}
	got := newUnstructured(&dummyv1alpha1.Thing{})
	if err := localClient.Get(ctx, localKey, got); err != nil {
		t.Fatalf("Local object was not created: %v", err)
	}

	mfs := got.GetManagedFields()
	if len(mfs) == 0 {
		t.Fatalf("Expected managedFields on the destination object, got none")
	}

	var syncerEntry *metav1.ManagedFieldsEntry
	for i := range mfs {
		if mfs[i].Manager == wantFM {
			syncerEntry = &mfs[i]
			break
		}
	}
	if syncerEntry == nil {
		var managers []string
		for _, e := range mfs {
			managers = append(managers, e.Manager)
		}
		t.Fatalf("Expected a managedFields entry owned by %q, found managers: %v", wantFM, managers)
	}

	if syncerEntry.Operation != metav1.ManagedFieldsOperationApply {
		t.Errorf("Expected managedFields entry operation %q, got %q", metav1.ManagedFieldsOperationApply, syncerEntry.Operation)
	}
	if syncerEntry.FieldsV1 == nil || len(syncerEntry.FieldsV1.GetRawBytes()) == 0 {
		t.Errorf("Expected managedFields entry %q to declare owned fields (FieldsV1 was empty)", wantFM)
	}
}
