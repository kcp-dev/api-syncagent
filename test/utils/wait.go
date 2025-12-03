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
	"slices"
	"testing"
	"time"

	kcpapisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"

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

func WaitForBoundAPI(t *testing.T, ctx context.Context, client ctrlruntimeclient.Client, gvr schema.GroupVersionResource) {
	t.Helper()

	t.Log("Waiting for API to be bound in kcp…")
	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 1*time.Minute, false, func(ctx context.Context) (bool, error) {
		apiBindings := &kcpapisv1alpha1.APIBindingList{}
		err := client.List(ctx, apiBindings)
		if err != nil {
			return false, err
		}

		for _, binding := range apiBindings.Items {
			if bindingHasGVR(binding, gvr) {
				return true, nil
			}
		}

		return false, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for API %v to become available: %v", gvr, err)
	}
}

func bindingHasGVR(binding kcpapisv1alpha1.APIBinding, gvr schema.GroupVersionResource) bool {
	for _, bound := range binding.Status.BoundResources {
		if bound.Group == gvr.Group && bound.Resource == gvr.Resource && slices.Contains(bound.StorageVersions, gvr.Version) {
			return true
		}
	}

	return false
}
