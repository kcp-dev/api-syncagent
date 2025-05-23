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

package sync

import (
	"testing"

	dummyv1alpha1 "github.com/kcp-dev/api-syncagent/internal/sync/apis/dummy/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestStateStoreBasics(t *testing.T) {
	primaryObject := newUnstructured(&dummyv1alpha1.Thing{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-test-thing",
		},
		Spec: dummyv1alpha1.ThingSpec{
			Username: "Miss Scarlet",
		},
	}, withKind("RemoteThing"))

	serviceClusterClient := buildFakeClient()
	ctx := t.Context()
	stateNamespace := "kcp-system"

	primaryObjectSide := syncSide{
		object: primaryObject,
	}

	stateSide := syncSide{
		ctx:    ctx,
		client: serviceClusterClient,
	}

	storeCreator := newKubernetesStateStoreCreator(stateNamespace)
	store := storeCreator(primaryObjectSide, stateSide)

	///////////////////////////////////////
	// get nil from empty store

	result, err := store.Get(syncSide{object: primaryObject})
	if err != nil {
		t.Fatalf("Failed to get primary object from empty cache: %v", err)
	}
	if result != nil {
		t.Fatalf("Should not have been able to find a state, but got: %+v\n", result)
	}

	///////////////////////////////////////
	// store a first object

	firstObject := newUnstructured(&dummyv1alpha1.Thing{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-test-thing",
		},
		Spec: dummyv1alpha1.ThingSpec{
			Username: "Miss Scarlet",
		},
	}, withKind("RemoteThing"))

	err = store.Put(firstObject, "", nil)
	if err != nil {
		t.Fatalf("Failed to store object in empty cache: %v", err)
	}

	secrets := corev1.SecretList{}
	if err := serviceClusterClient.List(ctx, &secrets); err != nil {
		t.Fatalf("Failed to list secrets: %v", err)
	}
	if len(secrets.Items) != 1 {
		t.Fatalf("Expected exactly 1 state Secret, got %d.", len(secrets.Items))
	}

	///////////////////////////////////////
	// retrieve the stored object

	result, err = store.Get(syncSide{object: firstObject})
	if err != nil {
		t.Fatalf("Failed to get stored object from cache: %v", err)
	}
	if result == nil {
		t.Fatal("Could not retrieve stored object.")
	}

	assertObjectsEqual(t, "RemoteThing", firstObject, result)

	///////////////////////////////////////
	// retrieve another object

	secondObject := newUnstructured(&dummyv1alpha1.Thing{
		ObjectMeta: metav1.ObjectMeta{
			Name: "another-object",
		},
	}, withKind("RemoteThing"))

	result, err = store.Get(syncSide{object: secondObject})
	if err != nil {
		t.Fatalf("Failed to get second object from cache: %v", err)
	}
	if result != nil {
		t.Fatalf("Should not have been able to find a state for an second object, but got: %+v\n", result)
	}

	///////////////////////////////////////
	// store a 2nd object

	err = store.Put(secondObject, "", nil)
	if err != nil {
		t.Fatalf("Failed to store second object in cache: %v", err)
	}

	result, err = store.Get(syncSide{object: secondObject})
	if err != nil {
		t.Fatalf("Failed to get second object from cache: %v", err)
	}

	assertObjectsEqual(t, "RemoteThing", secondObject, result)

	///////////////////////////////////////
	// retrieve the first, ensure it's not overwritten

	result, err = store.Get(syncSide{object: firstObject})
	if err != nil {
		t.Fatalf("Failed to get first object from cache again: %v", err)
	}

	assertObjectsEqual(t, "RemoteThing", firstObject, result)

	///////////////////////////////////////
	// strip subresources

	thirdObject := newUnstructured(&dummyv1alpha1.ThingWithStatusSubresource{
		ObjectMeta: metav1.ObjectMeta{
			Name: "subresourced",
		},
		Spec: dummyv1alpha1.ThingSpec{
			Username: "Jerry",
		},
		Status: dummyv1alpha1.ThingStatus{
			CurrentVersion: "latest",
		},
	}, withKind("RemoteThing"))

	err = store.Put(thirdObject, "", nil)
	if err != nil {
		t.Fatalf("Failed to store third object in cache: %v", err)
	}

	///////////////////////////////////////
	// ensure status is kept

	result, err = store.Get(syncSide{object: thirdObject})
	if err != nil {
		t.Fatalf("Failed to get third object from cache again: %v", err)
	}

	assertObjectsEqual(t, "RemoteThing", thirdObject, result)

	///////////////////////////////////////
	// overwrite, but this time strip subresource

	err = store.Put(thirdObject, "", []string{"status"})
	if err != nil {
		t.Fatalf("Failed to store third object in cache: %v", err)
	}

	///////////////////////////////////////
	// ensure status is gone

	result, err = store.Get(syncSide{object: thirdObject})
	if err != nil {
		t.Fatalf("Failed to get third object from cache again: %v", err)
	}

	delete(thirdObject.Object, "status")
	assertObjectsEqual(t, "RemoteThing", thirdObject, result)
}
