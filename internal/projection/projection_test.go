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

	"github.com/google/go-cmp/cmp"

	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	kcpapisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func TestProjectCRD(t *testing.T) {
	crdSingleVersion := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "things.example.com",
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "example.com",
			Scope: apiextensionsv1.ClusterScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "things",
				Singular: "thing",
				Kind:     "Thing",
				ListKind: "ThingList",
			},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:    "v1",
					Served:  true,
					Storage: true,
					Subresources: &apiextensionsv1.CustomResourceSubresources{
						Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
					},
				},
			},
		},
	}

	crdMultiVersions := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "things.example.com",
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "example.com",
			Scope: apiextensionsv1.ClusterScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "things",
				Singular: "thing",
				Kind:     "Thing",
				ListKind: "ThingList",
			},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:    "v1beta1",
					Served:  false,
					Storage: false,
					Subresources: &apiextensionsv1.CustomResourceSubresources{
						Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
					},
				},
				{
					Name:    "v1",
					Served:  true,
					Storage: false,
					Subresources: &apiextensionsv1.CustomResourceSubresources{
						Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
					},
				},
				{
					Name:    "v2alpha1",
					Served:  true,
					Storage: false,
					Subresources: &apiextensionsv1.CustomResourceSubresources{
						Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
					},
				},
				{
					Name:    "v2",
					Served:  true,
					Storage: true,
					Subresources: &apiextensionsv1.CustomResourceSubresources{
						Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
					},
				},
				{
					Name:    "v3",
					Served:  true,
					Storage: false,
				},
			},
		},
	}

	patchCRD := func(base *apiextensionsv1.CustomResourceDefinition, patch func(*apiextensionsv1.CustomResourceDefinition)) *apiextensionsv1.CustomResourceDefinition {
		crd := base.DeepCopy()
		patch(crd)

		return crd
	}

	testcases := []struct {
		name      string
		crd       *apiextensionsv1.CustomResourceDefinition
		pubRes    syncagentv1alpha1.PublishedResourceSpec
		expected  *apiextensionsv1.CustomResourceDefinition
		expectErr bool
	}{
		{
			name:     "no projection on a single-version CRD",
			crd:      crdSingleVersion,
			pubRes:   syncagentv1alpha1.PublishedResourceSpec{},
			expected: crdSingleVersion,
		},
		{
			name:     "no projection on a multi-version CRD",
			crd:      crdMultiVersions,
			pubRes:   syncagentv1alpha1.PublishedResourceSpec{},
			expected: crdMultiVersions,
		},
		{
			name: "select a single version (deprecated)",
			crd:  crdMultiVersions,
			pubRes: syncagentv1alpha1.PublishedResourceSpec{
				Resource: syncagentv1alpha1.SourceResourceDescriptor{
					Version: "v3",
				},
			},
			expected: patchCRD(crdMultiVersions, func(crd *apiextensionsv1.CustomResourceDefinition) {
				crd.Spec.Versions = []apiextensionsv1.CustomResourceDefinitionVersion{{
					Name:    "v3",
					Served:  true,
					Storage: true, // should be flipped to true by the projection
				}}
			}),
		},
		{
			name: "select a single version (modern)",
			crd:  crdMultiVersions,
			pubRes: syncagentv1alpha1.PublishedResourceSpec{
				Resource: syncagentv1alpha1.SourceResourceDescriptor{
					Versions: []string{"v3"},
				},
			},
			expected: patchCRD(crdMultiVersions, func(crd *apiextensionsv1.CustomResourceDefinition) {
				crd.Spec.Versions = []apiextensionsv1.CustomResourceDefinitionVersion{{
					Name:    "v3",
					Served:  true,
					Storage: true,
				}}
			}),
		},
		{
			name: "select a subset of versions",
			crd:  crdMultiVersions,
			pubRes: syncagentv1alpha1.PublishedResourceSpec{
				Resource: syncagentv1alpha1.SourceResourceDescriptor{
					Versions: []string{"v3", "v1"}, // note the order here
				},
			},
			expected: patchCRD(crdMultiVersions, func(crd *apiextensionsv1.CustomResourceDefinition) {
				// Resulting CRD versions must be properly sorted regardless of the source rules.

				crd.Spec.Versions = []apiextensionsv1.CustomResourceDefinitionVersion{{
					Name:    "v1",
					Served:  true,
					Storage: false,
					Subresources: &apiextensionsv1.CustomResourceSubresources{
						Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
					},
				}, {
					Name:    "v3",
					Served:  true,
					Storage: true, // should be flipped to true by the projection
				}}
			}),
		},
		{
			name: "error: select a non-existing version (deprecated)",
			crd:  crdMultiVersions,
			pubRes: syncagentv1alpha1.PublishedResourceSpec{
				Resource: syncagentv1alpha1.SourceResourceDescriptor{
					Version: "v4",
				},
			},
			expectErr: true,
		},
		{
			name: "error: select a non-existing version (modern)",
			crd:  crdMultiVersions,
			pubRes: syncagentv1alpha1.PublishedResourceSpec{
				Resource: syncagentv1alpha1.SourceResourceDescriptor{
					Versions: []string{"v4"},
				},
			},
			expectErr: true,
		},
		{
			name: "error: select no served version",
			crd:  crdMultiVersions,
			pubRes: syncagentv1alpha1.PublishedResourceSpec{
				Resource: syncagentv1alpha1.SourceResourceDescriptor{
					Versions: []string{"v1beta1"},
				},
			},
			expectErr: true,
		},
		{
			name: "auto-determine storage version",
			crd:  crdMultiVersions,
			pubRes: syncagentv1alpha1.PublishedResourceSpec{
				Resource: syncagentv1alpha1.SourceResourceDescriptor{
					Versions: []string{"v2", "v1"},
				},
			},
			expected: patchCRD(crdMultiVersions, func(crd *apiextensionsv1.CustomResourceDefinition) {
				crd.Spec.Versions = []apiextensionsv1.CustomResourceDefinitionVersion{{
					Name:    "v1",
					Served:  true,
					Storage: false,
					Subresources: &apiextensionsv1.CustomResourceSubresources{
						Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
					},
				}, {
					Name:    "v2",
					Served:  true,
					Storage: true,
					Subresources: &apiextensionsv1.CustomResourceSubresources{
						Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
					},
				}}
			}),
		},
		{
			name: "project single version (deprecated)",
			crd:  crdMultiVersions,
			pubRes: syncagentv1alpha1.PublishedResourceSpec{
				Resource: syncagentv1alpha1.SourceResourceDescriptor{
					Versions: []string{"v3"},
				},
				Projection: &syncagentv1alpha1.ResourceProjection{
					Version: "v6",
				},
			},
			expected: patchCRD(crdMultiVersions, func(crd *apiextensionsv1.CustomResourceDefinition) {
				crd.Spec.Versions = []apiextensionsv1.CustomResourceDefinitionVersion{{
					Name:    "v6",
					Served:  true,
					Storage: true,
				}}
			}),
		},
		{
			name: "project single version (modern)",
			crd:  crdMultiVersions,
			pubRes: syncagentv1alpha1.PublishedResourceSpec{
				Resource: syncagentv1alpha1.SourceResourceDescriptor{
					Versions: []string{"v3"},
				},
				Projection: &syncagentv1alpha1.ResourceProjection{
					Versions: map[string]string{
						"v3": "v6",
					},
				},
			},
			expected: patchCRD(crdMultiVersions, func(crd *apiextensionsv1.CustomResourceDefinition) {
				crd.Spec.Versions = []apiextensionsv1.CustomResourceDefinitionVersion{{
					Name:    "v6",
					Served:  true,
					Storage: true,
				}}
			}),
		},
		{
			name: "error: project multiple versions to the same version",
			crd:  crdMultiVersions,
			pubRes: syncagentv1alpha1.PublishedResourceSpec{
				Resource: syncagentv1alpha1.SourceResourceDescriptor{
					Versions: []string{"v3", "v1"},
				},
				Projection: &syncagentv1alpha1.ResourceProjection{
					Versions: map[string]string{
						"v3": "v6",
						"v1": "v6",
					},
				},
			},
			expectErr: true,
		},
		{
			name: "project multiple versions",
			crd:  crdMultiVersions,
			pubRes: syncagentv1alpha1.PublishedResourceSpec{
				Resource: syncagentv1alpha1.SourceResourceDescriptor{
					Versions: []string{"v3", "v1"},
				},
				Projection: &syncagentv1alpha1.ResourceProjection{
					Versions: map[string]string{
						"v3": "v6",
						"v1": "v7",
					},
				},
			},
			expected: patchCRD(crdMultiVersions, func(crd *apiextensionsv1.CustomResourceDefinition) {
				crd.Spec.Versions = []apiextensionsv1.CustomResourceDefinitionVersion{{
					Name:    "v6",
					Served:  true,
					Storage: true,
				}, {
					Name:    "v7",
					Served:  true,
					Storage: false,
					Subresources: &apiextensionsv1.CustomResourceSubresources{
						Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
					},
				}}
			}),
		},
		{
			name: "project API group",
			crd:  crdMultiVersions,
			pubRes: syncagentv1alpha1.PublishedResourceSpec{
				Projection: &syncagentv1alpha1.ResourceProjection{
					Group: "new.example.com",
				},
			},
			expected: patchCRD(crdMultiVersions, func(crd *apiextensionsv1.CustomResourceDefinition) {
				crd.Spec.Group = "new.example.com"
				crd.Name = "things.new.example.com"
			}),
		},
		{
			name: "project kind (auto-pluralizes)",
			crd:  crdMultiVersions,
			pubRes: syncagentv1alpha1.PublishedResourceSpec{
				Projection: &syncagentv1alpha1.ResourceProjection{
					Kind: "NewThing",
				},
			},
			expected: patchCRD(crdMultiVersions, func(crd *apiextensionsv1.CustomResourceDefinition) {
				crd.Spec.Names.Kind = "NewThing"
				crd.Spec.Names.ListKind = "NewThingList"
				crd.Spec.Names.Singular = "newthing"
				crd.Spec.Names.Plural = "newthings"
				crd.Name = "newthings.example.com"
			}),
		},
		{
			name: "project kind (explicit plural projection)",
			crd:  crdMultiVersions,
			pubRes: syncagentv1alpha1.PublishedResourceSpec{
				Projection: &syncagentv1alpha1.ResourceProjection{
					Kind:   "Foot",
					Plural: "feet",
				},
			},
			expected: patchCRD(crdMultiVersions, func(crd *apiextensionsv1.CustomResourceDefinition) {
				crd.Spec.Names.Kind = "Foot"
				crd.Spec.Names.ListKind = "FootList"
				crd.Spec.Names.Singular = "foot"
				crd.Spec.Names.Plural = "feet"
				crd.Name = "feet.example.com"
			}),
		},
		{
			name: "project CRD properties",
			crd:  crdMultiVersions,
			pubRes: syncagentv1alpha1.PublishedResourceSpec{
				Projection: &syncagentv1alpha1.ResourceProjection{
					Scope:      syncagentv1alpha1.NamespaceScoped,
					ShortNames: []string{"shorty"},
					Categories: []string{"e2e"},
				},
			},
			expected: patchCRD(crdMultiVersions, func(crd *apiextensionsv1.CustomResourceDefinition) {
				crd.Spec.Scope = apiextensionsv1.NamespaceScoped
				crd.Spec.Names.ShortNames = []string{"shorty"}
				crd.Spec.Names.Categories = []string{"e2e"}
			}),
		},
		{
			name: "ensure new name takes both new group and new plural into account",
			crd:  crdMultiVersions,
			pubRes: syncagentv1alpha1.PublishedResourceSpec{
				Projection: &syncagentv1alpha1.ResourceProjection{
					Group:  "new.example.com",
					Plural: "feet",
				},
			},
			expected: patchCRD(crdMultiVersions, func(crd *apiextensionsv1.CustomResourceDefinition) {
				crd.Spec.Group = "new.example.com"
				crd.Spec.Names.Plural = "feet"
				crd.Name = "feet.new.example.com"
			}),
		},
	}

	for _, testcase := range testcases {
		t.Run(testcase.name, func(t *testing.T) {
			pr := &syncagentv1alpha1.PublishedResource{
				Spec: testcase.pubRes,
			}

			projectedCRD, err := ProjectCRD(testcase.crd, pr)
			if err != nil {
				if !testcase.expectErr {
					t.Fatalf("Unexpected error: %v", err)
				}

				return
			} else if testcase.expectErr {
				t.Fatalf("Expected an error, but got a CRD instead:\n\n%+v", projectedCRD)
			}

			compareSchemalessCRDs(t, testcase.expected, projectedCRD)
		})
	}
}

func compareSchemalessCRDs(t *testing.T, expected, actual *apiextensionsv1.CustomResourceDefinition) {
	if expected.Name != actual.Name {
		t.Errorf("Expected CRD to be named %q, got %q.", expected.Name, actual.Name)
	}

	if diff := cmp.Diff(expected.Spec.Names, actual.Spec.Names); diff != "" {
		t.Errorf("Actual CRD names do not match expectations:\n\n%s", diff)
	}

	for i, v := range actual.Spec.Versions {
		v.Schema = nil
		actual.Spec.Versions[i] = v
	}

	if diff := cmp.Diff(expected.Spec.Versions, actual.Spec.Versions); diff != "" {
		t.Errorf("Actual CRD versions do not match expectations:\n\n%s", diff)
	}
}

func TestProjectConversions(t *testing.T) {
	testcases := []struct {
		name        string
		conversions []kcpapisv1alpha1.APIVersionConversion
		projection  *syncagentv1alpha1.ResourceProjection
		expected    []kcpapisv1alpha1.APIVersionConversion
		expectErr   bool
	}{
		{
			name: "no projections at all",
			conversions: []kcpapisv1alpha1.APIVersionConversion{{
				From:  "v1",
				To:    "v2",
				Rules: []kcpapisv1alpha1.APIConversionRule{},
			}},
			expected: []kcpapisv1alpha1.APIVersionConversion{{
				From:  "v1",
				To:    "v2",
				Rules: []kcpapisv1alpha1.APIConversionRule{},
			}},
		},
		{
			name: "project versions",
			conversions: []kcpapisv1alpha1.APIVersionConversion{{
				From:  "v1",
				To:    "v2",
				Rules: []kcpapisv1alpha1.APIConversionRule{{Field: "a"}},
			}, {
				From:  "v2",
				To:    "v3",
				Rules: []kcpapisv1alpha1.APIConversionRule{{Field: "b"}},
			}, {
				From:  "v3",
				To:    "v1",
				Rules: []kcpapisv1alpha1.APIConversionRule{{Field: "c"}},
			}, {
				From:  "v4",
				To:    "v2",
				Rules: []kcpapisv1alpha1.APIConversionRule{{Field: "d"}},
			}},
			projection: &syncagentv1alpha1.ResourceProjection{
				Versions: map[string]string{
					"v1": "v4",
					"v2": "v1",
					"v3": "v3", // project to itself
					"v4": "v2",
				},
			},
			expected: []kcpapisv1alpha1.APIVersionConversion{{
				From:  "v4",
				To:    "v1",
				Rules: []kcpapisv1alpha1.APIConversionRule{{Field: "a"}},
			}, {
				From:  "v1",
				To:    "v3",
				Rules: []kcpapisv1alpha1.APIConversionRule{{Field: "b"}},
			}, {
				From:  "v3",
				To:    "v4",
				Rules: []kcpapisv1alpha1.APIConversionRule{{Field: "c"}},
			}, {
				From:  "v2",
				To:    "v1",
				Rules: []kcpapisv1alpha1.APIConversionRule{{Field: "d"}},
			}},
		},
	}

	for _, testcase := range testcases {
		t.Run(testcase.name, func(t *testing.T) {
			pr := &syncagentv1alpha1.PublishedResource{
				Spec: syncagentv1alpha1.PublishedResourceSpec{
					Conversions: testcase.conversions,
					Projection:  testcase.projection,
				},
			}

			rules, err := ProjectConversionRules(pr)
			if err != nil {
				if !testcase.expectErr {
					t.Fatalf("Unexpected error: %v", err)
				}

				return
			} else if testcase.expectErr {
				t.Fatalf("Expected an error, but got rules instead:\n\n%+v", rules)
			}

			if diff := cmp.Diff(testcase.expected, rules); diff != "" {
				t.Fatalf("Result does not match expectations:\n\n%s", diff)
			}
		})
	}
}
