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
	"testing"

	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestDeletionPropagationPolicy(t *testing.T) {
	testcases := []struct {
		name     string
		policy   string
		expected metav1.DeletionPropagation
	}{
		{
			name:     "foreground annotation",
			policy:   deletionPropagationForeground,
			expected: metav1.DeletePropagationForeground,
		},
		{
			name:     "orphan annotation",
			policy:   deletionPropagationOrphan,
			expected: metav1.DeletePropagationOrphan,
		},
		{
			name:     "background annotation",
			policy:   deletionPropagationBackground,
			expected: metav1.DeletePropagationBackground,
		},
		{
			name:     "missing annotation defaults to background",
			expected: metav1.DeletePropagationBackground,
		},
		{
			name:     "unknown annotation defaults to background",
			policy:   "unknown",
			expected: metav1.DeletePropagationBackground,
		},
	}

	for _, testcase := range testcases {
		t.Run(testcase.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{}
			if testcase.policy != "" {
				obj.SetAnnotations(map[string]string{deletionPropagationPolicyAnnotation: testcase.policy})
			}

			got := deletionPropagationPolicy(obj)
			if got != testcase.expected {
				t.Fatalf("expected %q, got %q", testcase.expected, got)
			}
		})
	}
}

func TestRelatedDeletionPropagationPolicy(t *testing.T) {
	obj := &unstructured.Unstructured{}
	obj.SetAnnotations(map[string]string{
		deletionPropagationPolicyAnnotation: deletionPropagationForeground,
	})

	if got := relatedDeletionPropagationPolicy(
		syncagentv1alpha1.RelatedResourceOriginKcp,
		obj,
	); got != metav1.DeletePropagationForeground {
		t.Fatalf("expected kcp-origin related object to use foreground, got %q", got)
	}

	if got := relatedDeletionPropagationPolicy(
		syncagentv1alpha1.RelatedResourceOriginService,
		obj,
	); got != metav1.DeletePropagationBackground {
		t.Fatalf("expected service-origin related object to use background, got %q", got)
	}
}
