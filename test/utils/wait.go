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

package utils

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func WaitForObject(t *testing.T, ctx context.Context, client ctrlruntimeclient.Client, obj ctrlruntimeclient.Object, key types.NamespacedName) {
	t.Helper()
	t.Logf("Waiting for %T to exist…", obj)

	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 3*time.Minute, false, func(ctx context.Context) (done bool, err error) {
		err = client.Get(ctx, key, obj)
		return err == nil, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for %T to exist: %v", obj, err)
	}

	t.Logf("%T is ready.", obj)
}

func WaitForBoundAPI(t *testing.T, ctx context.Context, client ctrlruntimeclient.Client, gvk schema.GroupVersionKind) {
	t.Helper()

	t.Logf("Waiting for API %s/%s to be bound in kcp…", gvk.Group, gvk.Kind)

	// Wait for actual resource availability instead of checking the APIBinding, because this is more
	// reliable, especially on slower CI environments.
	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 1*time.Minute, false, func(ctx context.Context) (bool, error) {
		// Try to list resources of this type - if the resource isn't ready, this will fail
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk)

		return client.List(ctx, list) == nil, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for API %v to become available: %v", gvk, err)
	}
}
