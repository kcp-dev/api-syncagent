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

package projection

import (
	"testing"

	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestPublishedResourceSourceGVK(t *testing.T) {
	const (
		apiGroup = "testgroup"
		version  = "v1"
		kind     = "test"
	)

	pubRes := syncagentv1alpha1.PublishedResource{
		Spec: syncagentv1alpha1.PublishedResourceSpec{
			Resource: syncagentv1alpha1.SourceResourceDescriptor{
				APIGroup: apiGroup,
				Version:  version,
				Kind:     kind,
			},
		},
	}

	gk := PublishedResourceSourceGK(&pubRes)

	if gk.Group != apiGroup {
		t.Errorf("Expected API group to be %q, but got %q.", apiGroup, gk.Group)
	}

	if gk.Kind != kind {
		t.Errorf("Expected kind to be %q, but got %q.", kind, gk.Kind)
	}
}

func TestPublishedResourceProjectedGVK(t *testing.T) {
	const (
		apiGroup = "testgroup"
		version  = "v1"
		kind     = "test"
	)

	pubRes := &syncagentv1alpha1.PublishedResource{
		Spec: syncagentv1alpha1.PublishedResourceSpec{
			Resource: syncagentv1alpha1.SourceResourceDescriptor{
				APIGroup: apiGroup,
				Version:  version,
				Kind:     kind,
			},
		},
	}

	crd := &apiextensionsv1.CustomResourceDefinition{
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: apiGroup,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Kind: kind,
			},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:    version,
					Served:  true,
					Storage: true,
				},
			},
		},
	}

	testcases := []struct {
		name       string
		projection *syncagentv1alpha1.ResourceProjection
		expected   schema.GroupVersionKind
	}{
		{
			name:       "no projection",
			projection: nil,
			expected:   schema.GroupVersionKind{Group: apiGroup, Version: version, Kind: kind},
		},
		{
			name:       "override version",
			projection: &syncagentv1alpha1.ResourceProjection{Version: "v2"},
			expected:   schema.GroupVersionKind{Group: apiGroup, Version: "v2", Kind: kind},
		},
		{
			name:       "override kind",
			projection: &syncagentv1alpha1.ResourceProjection{Kind: "dummy"},
			expected:   schema.GroupVersionKind{Group: apiGroup, Version: version, Kind: "dummy"},
		},
		{
			name:       "override both",
			projection: &syncagentv1alpha1.ResourceProjection{Version: "v2", Kind: "dummy"},
			expected:   schema.GroupVersionKind{Group: apiGroup, Version: "v2", Kind: "dummy"},
		},
		{
			name:       "override group",
			projection: &syncagentv1alpha1.ResourceProjection{Group: "projected.com"},
			expected:   schema.GroupVersionKind{Group: "projected.com", Version: version, Kind: kind},
		},
	}

	for _, testcase := range testcases {
		t.Run(testcase.name, func(t *testing.T) {
			pr := pubRes.DeepCopy()
			pr.Spec.Projection = testcase.projection

			gvk, err := PublishedResourceProjectedGVK(crd, pr)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if gvk.Group != testcase.expected.Group {
				t.Errorf("Expected API group to be %q, but got %q.", testcase.expected.Group, gvk.Group)
			}

			if gvk.Version != testcase.expected.Version {
				t.Errorf("Expected version to be %q, but got %q.", testcase.expected.Version, gvk.Version)
			}

			if gvk.Kind != testcase.expected.Kind {
				t.Errorf("Expected kind to be %q, but got %q.", testcase.expected.Kind, gvk.Kind)
			}
		})
	}
}
