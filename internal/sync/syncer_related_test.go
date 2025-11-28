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
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestResolveRelatedResourceObjects(t *testing.T) {
	// in kcp
	primaryObject := newUnstructured(&dummyv1alpha1.Thing{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-test-thing",
		},
		Spec: dummyv1alpha1.ThingSpec{
			Username: "original-value",
			Kink:     "taxreturns",
		},
	}, withKind("RemoteThing"))

	// on the service cluster
	primaryObjectCopy := newUnstructured(&dummyv1alpha1.Thing{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-test-thing",
		},
		Spec: dummyv1alpha1.ThingSpec{
			Username: "mutated-value",
			Kink:     "",
		},
	})

	// Create a secret that can be found by using a good reference, so we can ensure that references
	// do indeed work; all other subtests here ensure that reference support can deal with broken refs.
	dummySecret := newUnstructured(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "dummy-namespace",
			Name:      "mutated-value",
		},
	})

	kcpClient := buildFakeClient(primaryObject)
	serviceClusterClient := buildFakeClient(primaryObjectCopy, dummySecret)

	// Now we configure origin/dest as if we're syncing a Secret up from the service cluster to kcp,
	// i.e. origin=service.

	originSide := syncSide{
		client: serviceClusterClient,
		object: primaryObjectCopy,
	}

	destSide := syncSide{
		client: kcpClient,
		object: primaryObject,
		// Since this is a just a regular kube client, we do not need to set clusterName/clusterPath.
	}

	testcases := []struct {
		name            string
		objectSpec      syncagentv1alpha1.RelatedResourceObject
		expectedSecrets int
	}{
		{
			name: "valid reference to an existing object",
			objectSpec: syncagentv1alpha1.RelatedResourceObject{
				RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
					Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
						Path: "spec.username",
					},
				},
				Namespace: &syncagentv1alpha1.RelatedResourceObjectSpec{
					Template: &syncagentv1alpha1.TemplateExpression{
						Template: "dummy-namespace",
					},
				},
			},
			expectedSecrets: 1,
		},
		{
			name: "valid template to an existing object",
			objectSpec: syncagentv1alpha1.RelatedResourceObject{
				RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
					Template: &syncagentv1alpha1.TemplateExpression{
						Template: "{{ .Object.spec.username }}",
					},
				},
				Namespace: &syncagentv1alpha1.RelatedResourceObjectSpec{
					Template: &syncagentv1alpha1.TemplateExpression{
						Template: "dummy-namespace",
					},
				},
			},
			expectedSecrets: 1,
		},
		{
			name: "valid reference but target object doesn't exist [yet?]",
			objectSpec: syncagentv1alpha1.RelatedResourceObject{
				RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
					Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
						Path: "spec.username",
					},
				},
				Namespace: &syncagentv1alpha1.RelatedResourceObjectSpec{
					Template: &syncagentv1alpha1.TemplateExpression{
						Template: "nonexisting-namespace",
					},
				},
			},
			expectedSecrets: 0,
		},
		{
			name: "valid template but target object doesn't exist [yet?]",
			objectSpec: syncagentv1alpha1.RelatedResourceObject{
				RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
					Template: &syncagentv1alpha1.TemplateExpression{
						Template: "{{ .Object.spec.username }}",
					},
				},
				Namespace: &syncagentv1alpha1.RelatedResourceObjectSpec{
					Template: &syncagentv1alpha1.TemplateExpression{
						Template: "nonexisting-namespace",
					},
				},
			},
			expectedSecrets: 0,
		},
		{
			name: "valid reference to an empty field",
			objectSpec: syncagentv1alpha1.RelatedResourceObject{
				RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
					Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
						Path: "spec.kink",
					},
				},
				Namespace: &syncagentv1alpha1.RelatedResourceObjectSpec{
					Template: &syncagentv1alpha1.TemplateExpression{
						Template: "dummy-namespace",
					},
				},
			},
			expectedSecrets: 0,
		},
		{
			name: "valid template to an empty field",
			objectSpec: syncagentv1alpha1.RelatedResourceObject{
				RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
					Template: &syncagentv1alpha1.TemplateExpression{
						Template: "{{ .Object.spec.kink }}",
					},
				},
				Namespace: &syncagentv1alpha1.RelatedResourceObjectSpec{
					Template: &syncagentv1alpha1.TemplateExpression{
						Template: "dummy-namespace",
					},
				},
			},
			expectedSecrets: 0,
		},
		{
			name: "referring to an omitempty field",
			objectSpec: syncagentv1alpha1.RelatedResourceObject{
				RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
					Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
						Path: "spec.address",
					},
				},
				Namespace: &syncagentv1alpha1.RelatedResourceObjectSpec{
					Template: &syncagentv1alpha1.TemplateExpression{
						Template: "dummy-namespace",
					},
				},
			},
			expectedSecrets: 0,
		},
		{
			name: "templating an omitempty field",
			objectSpec: syncagentv1alpha1.RelatedResourceObject{
				RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
					Template: &syncagentv1alpha1.TemplateExpression{
						Template: "{{ .Object.spec.address }}",
					},
				},
				Namespace: &syncagentv1alpha1.RelatedResourceObjectSpec{
					Template: &syncagentv1alpha1.TemplateExpression{
						Template: "dummy-namespace",
					},
				},
			},
			expectedSecrets: 0,
		},
	}

	for _, testcase := range testcases {
		t.Run(testcase.name, func(t *testing.T) {
			pubRes := syncagentv1alpha1.RelatedResourceSpec{
				Identifier: "test",
				Origin:     syncagentv1alpha1.RelatedResourceOriginService,
				Kind:       "Secret",
				Object:     testcase.objectSpec,
			}

			foundObjects, err := resolveRelatedResourceObjects(t.Context(), originSide, destSide, pubRes)
			if err != nil {
				t.Fatalf("Failed to resolve related objects: %v", err)
			}
			if len(foundObjects) != testcase.expectedSecrets {
				t.Fatalf("Expected %d related object (Secret) to be found, but found %d.", testcase.expectedSecrets, len(foundObjects))
			}
		})
	}
}
