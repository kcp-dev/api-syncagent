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
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"
	"go.uber.org/zap"

	"github.com/kcp-dev/api-syncagent/internal/mutation"
	dummyv1alpha1 "github.com/kcp-dev/api-syncagent/internal/sync/apis/dummy/v1alpha1"
	"github.com/kcp-dev/api-syncagent/internal/test/diff"
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/tools/record"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	fakectrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newFakeClientBuilder() *fakectrlruntimeclient.ClientBuilder {
	scheme := runtime.NewScheme()

	utilruntime.Must(metav1.AddMetaToScheme(scheme))
	utilruntime.Must(corev1.AddToScheme(scheme))

	mapper := meta.NewDefaultRESTMapper(nil)
	for gvk := range scheme.AllKnownTypes() {
		mapper.Add(gvk, meta.RESTScopeNamespace)
	}

	return fakectrlruntimeclient.NewClientBuilder().WithScheme(scheme).WithRESTMapper(mapper)
}

func buildFakeClient(objs ...*unstructured.Unstructured) ctrlruntimeclient.Client {
	builder := newFakeClientBuilder()
	for i, obj := range objs {
		if obj != nil {
			builder.WithObjects(objs[i])
		}
	}

	return builder.Build()
}

func buildFakeClientWithStatus(objs ...*unstructured.Unstructured) ctrlruntimeclient.Client {
	builder := newFakeClientBuilder()
	for i, obj := range objs {
		if obj != nil {
			builder.WithObjects(objs[i])
			builder.WithStatusSubresource(objs[i])
		}
	}

	return builder.Build()
}

func loadCRD(filename string) *apiextensionsv1.CustomResourceDefinition {
	f, err := os.Open(fmt.Sprintf("crd/%s_%s.yaml", dummyv1alpha1.GroupName, filename))
	if err != nil {
		panic(err)
	}
	defer f.Close()

	decoder := yamlutil.NewYAMLOrJSONDecoder(f, 1024)
	crd := &apiextensionsv1.CustomResourceDefinition{}

	err = decoder.Decode(crd)
	if err != nil {
		panic(err)
	}

	return crd
}

func withKind(kind string) func(*unstructured.Unstructured) {
	return func(u *unstructured.Unstructured) {
		u.SetKind(kind)
	}
}

func withGroupKind(group string, kind string) func(*unstructured.Unstructured) {
	return func(u *unstructured.Unstructured) {
		gvk := u.GetObjectKind().GroupVersionKind()
		gvk.Group = group
		gvk.Kind = kind
		u.SetGroupVersionKind(gvk)
	}
}

func newUnstructured(obj runtime.Object, modifiers ...func(*unstructured.Unstructured)) *unstructured.Unstructured {
	data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		panic(err)
	}

	gvks, _, err := testScheme.ObjectKinds(obj)
	if err != nil {
		panic(err)
	}
	if len(gvks) != 1 {
		panic(fmt.Sprintf("expected exactly 1 possible GVK for object, but got %d", len(gvks)))
	}

	unstructuredObj := &unstructured.Unstructured{Object: data}
	unstructuredObj.SetGroupVersionKind(gvks[0])

	for _, modifier := range modifiers {
		modifier(unstructuredObj)
	}

	return unstructuredObj
}

func TestSyncerProcessingSingleResourceWithoutStatus(t *testing.T) {
	type testcase struct {
		name                 string
		localCRD             *apiextensionsv1.CustomResourceDefinition
		pubRes               *syncagentv1alpha1.PublishedResource
		remoteObject         *unstructured.Unstructured
		localObject          *unstructured.Unstructured
		existingState        string
		performRequeues      bool
		expectedRemoteObject *unstructured.Unstructured
		expectedLocalObject  *unstructured.Unstructured
		expectedState        string
		customVerification   func(t *testing.T, requeue bool, processErr error, finalRemoteObject *unstructured.Unstructured, finalLocalObject *unstructured.Unstructured, testcase testcase)
	}

	clusterName := logicalcluster.Name("testcluster")

	remoteThingPR := &syncagentv1alpha1.PublishedResource{
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
			// include explicit naming rules to be independent of possible changes to the defaults
			Naming: &syncagentv1alpha1.ResourceNaming{
				Name: "$remoteClusterName-$remoteName", // Things are Cluster-scoped
			},
		},
	}

	testcases := []testcase{

		/////////////////////////////////////////////////////////////////////////////////

		{
			name:            "everything is already in perfect shape, nothing to do",
			localCRD:        loadCRD("thingwithstatussubresources"),
			pubRes:          remoteThingPR,
			performRequeues: true,

			remoteObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-test-thing",
					Finalizers: []string{
						deletionFinalizer,
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}, withGroupKind("remote.example.corp", "RemoteThing")),
			localObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testcluster-my-test-thing",
					Labels: map[string]string{
						agentNameLabel:            "textor-the-doctor",
						remoteObjectClusterLabel:  "testcluster",
						remoteObjectNameHashLabel: "c346c8ceb5d104cc783d09b95e8ea7032c190948",
					},
					Annotations: map[string]string{
						remoteObjectNameAnnotation: "my-test-thing",
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}),
			existingState: `{"apiVersion":"remote.example.corp/v1alpha1","kind":"RemoteThing","metadata":{"name":"my-test-thing"},"spec":{"kink":"","username":"Colonel Mustard"}}`,

			expectedRemoteObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-test-thing",
					Finalizers: []string{
						deletionFinalizer,
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}, withGroupKind("remote.example.corp", "RemoteThing")),
			expectedLocalObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testcluster-my-test-thing",
					Labels: map[string]string{
						agentNameLabel:            "textor-the-doctor",
						remoteObjectClusterLabel:  "testcluster",
						remoteObjectNameHashLabel: "c346c8ceb5d104cc783d09b95e8ea7032c190948",
					},
					Annotations: map[string]string{
						remoteObjectNameAnnotation: "my-test-thing",
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}),
			expectedState: `{"apiVersion":"remote.example.corp/v1alpha1","kind":"RemoteThing","metadata":{"name":"my-test-thing"},"spec":{"kink":"","username":"Colonel Mustard"}}`,
		},

		/////////////////////////////////////////////////////////////////////////////////

		{
			name:            "a new remote object is created",
			localCRD:        loadCRD("things"),
			pubRes:          remoteThingPR,
			performRequeues: true,

			remoteObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-test-thing",
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}, withGroupKind("remote.example.corp", "RemoteThing")),
			localObject:   nil,
			existingState: "",

			expectedRemoteObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-test-thing",
					Finalizers: []string{
						deletionFinalizer,
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}, withGroupKind("remote.example.corp", "RemoteThing")),
			expectedLocalObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testcluster-my-test-thing",
					Labels: map[string]string{
						agentNameLabel:            "textor-the-doctor",
						remoteObjectClusterLabel:  "testcluster",
						remoteObjectNameHashLabel: "c346c8ceb5d104cc783d09b95e8ea7032c190948",
					},
					Annotations: map[string]string{
						remoteObjectNameAnnotation: "my-test-thing",
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}),
			expectedState: `{"apiVersion":"remote.example.corp/v1alpha1","kind":"RemoteThing","metadata":{"name":"my-test-thing"},"spec":{"kink":"","username":"Colonel Mustard"}}`,
		},

		/////////////////////////////////////////////////////////////////////////////////

		{
			name:            "a new remote object is created that maps to an existing local one, which should be adopted",
			localCRD:        loadCRD("things"),
			pubRes:          remoteThingPR,
			performRequeues: true,

			remoteObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-test-thing",
					Finalizers: []string{
						deletionFinalizer,
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}, withGroupKind("remote.example.corp", "RemoteThing")),
			localObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testcluster-my-test-thing",
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}),
			existingState: "",

			expectedRemoteObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-test-thing",
					Finalizers: []string{
						deletionFinalizer,
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}, withGroupKind("remote.example.corp", "RemoteThing")),
			expectedLocalObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testcluster-my-test-thing",
					Labels: map[string]string{
						agentNameLabel:            "textor-the-doctor",
						remoteObjectClusterLabel:  "testcluster",
						remoteObjectNameHashLabel: "c346c8ceb5d104cc783d09b95e8ea7032c190948",
					},
					Annotations: map[string]string{
						remoteObjectNameAnnotation: "my-test-thing",
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}),
			expectedState: `{"apiVersion":"remote.example.corp/v1alpha1","kind":"RemoteThing","metadata":{"name":"my-test-thing"},"spec":{"kink":"","username":"Colonel Mustard"}}`,
		},

		/////////////////////////////////////////////////////////////////////////////////

		{
			name:            "changes to the spec should be copied to the local object",
			localCRD:        loadCRD("things"),
			pubRes:          remoteThingPR,
			performRequeues: true,

			remoteObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-test-thing",
					Finalizers: []string{
						deletionFinalizer,
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Miss Scarlet",
				},
			}, withGroupKind("remote.example.corp", "RemoteThing")),
			localObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testcluster-my-test-thing",
					Labels: map[string]string{
						agentNameLabel:            "textor-the-doctor",
						remoteObjectClusterLabel:  "testcluster",
						remoteObjectNameHashLabel: "c346c8ceb5d104cc783d09b95e8ea7032c190948",
					},
					Annotations: map[string]string{
						remoteObjectNameAnnotation: "my-test-thing",
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}),
			existingState: `{"apiVersion":"remote.example.corp/v1alpha1","kind":"RemoteThing","metadata":{"name":"my-test-thing"},"spec":{"kink":"","username":"Colonel Mustard"}}`,

			expectedRemoteObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-test-thing",
					Finalizers: []string{
						deletionFinalizer,
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Miss Scarlet",
				},
			}, withGroupKind("remote.example.corp", "RemoteThing")),
			expectedLocalObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testcluster-my-test-thing",
					Labels: map[string]string{
						agentNameLabel:            "textor-the-doctor",
						remoteObjectClusterLabel:  "testcluster",
						remoteObjectNameHashLabel: "c346c8ceb5d104cc783d09b95e8ea7032c190948",
					},
					Annotations: map[string]string{
						remoteObjectNameAnnotation: "my-test-thing",
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Miss Scarlet",
				},
			}),
			expectedState: `{"apiVersion":"remote.example.corp/v1alpha1","kind":"RemoteThing","metadata":{"name":"my-test-thing"},"spec":{"kink":"","username":"Miss Scarlet"}}`,
		},

		/////////////////////////////////////////////////////////////////////////////////

		{
			name:            "a broken last-state should be fixed automatically",
			localCRD:        loadCRD("things"),
			pubRes:          remoteThingPR,
			performRequeues: true,

			remoteObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-test-thing",
					Finalizers: []string{
						deletionFinalizer,
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}, withGroupKind("remote.example.corp", "RemoteThing")),
			localObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testcluster-my-test-thing",
					Labels: map[string]string{
						agentNameLabel:            "textor-the-doctor",
						remoteObjectClusterLabel:  "testcluster",
						remoteObjectNameHashLabel: "c346c8ceb5d104cc783d09b95e8ea7032c190948",
					},
					Annotations: map[string]string{
						remoteObjectNameAnnotation: "my-test-thing",
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}),
			existingState: "oh-my, this-is-not-json!",

			expectedRemoteObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-test-thing",
					Finalizers: []string{
						deletionFinalizer,
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}, withGroupKind("remote.example.corp", "RemoteThing")),
			expectedLocalObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testcluster-my-test-thing",
					Labels: map[string]string{
						agentNameLabel:            "textor-the-doctor",
						remoteObjectClusterLabel:  "testcluster",
						remoteObjectNameHashLabel: "c346c8ceb5d104cc783d09b95e8ea7032c190948",
					},
					Annotations: map[string]string{
						remoteObjectNameAnnotation: "my-test-thing",
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}),
			expectedState: `{"apiVersion":"remote.example.corp/v1alpha1","kind":"RemoteThing","metadata":{"name":"my-test-thing"},"spec":{"kink":"","username":"Colonel Mustard"}}`,
		},

		/////////////////////////////////////////////////////////////////////////////////

		{
			name:            "labels and annotations should be copied to the local object, except for syncagent-relevant fields",
			localCRD:        loadCRD("things"),
			pubRes:          remoteThingPR,
			performRequeues: true,

			remoteObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-test-thing",
					Finalizers: []string{
						deletionFinalizer,
					},
					Annotations: map[string]string{
						"existing-annotation": "new-annotation-value",
						"new-annotation":      "hei-verden",
					},
					Labels: map[string]string{
						remoteObjectClusterLabel: "this-should-be-ignored",
						"existing-label":         "new-label-value",
						"new-label":              "hello-world",
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}, withGroupKind("remote.example.corp", "RemoteThing")),
			localObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testcluster-my-test-thing",
					Annotations: map[string]string{
						remoteObjectNameAnnotation: "my-test-thing",
						"existing-annotation":      "annotation-value",
					},
					Labels: map[string]string{
						agentNameLabel:            "textor-the-doctor",
						remoteObjectClusterLabel:  "testcluster",
						remoteObjectNameHashLabel: "c346c8ceb5d104cc783d09b95e8ea7032c190948",
						"existing-label":          "label-value",
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}),
			existingState: `{"apiVersion":"remote.example.corp/v1alpha1","kind":"RemoteThing","metadata":{"annotations":{"existing-annotation":"annotation-value"},"labels":{"existing-label":"label-value"},"name":"my-test-thing"},"spec":{"kink":"","username":"Colonel Mustard"}}`,

			expectedRemoteObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-test-thing",
					Finalizers: []string{
						deletionFinalizer,
					},
					// syncer does not strip remote objects of bad metadata, so it remains
					Annotations: map[string]string{
						"existing-annotation": "new-annotation-value",
						"new-annotation":      "hei-verden",
					},
					Labels: map[string]string{
						remoteObjectClusterLabel: "this-should-be-ignored",
						"existing-label":         "new-label-value",
						"new-label":              "hello-world",
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}, withGroupKind("remote.example.corp", "RemoteThing")),
			expectedLocalObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testcluster-my-test-thing",
					Annotations: map[string]string{
						remoteObjectNameAnnotation: "my-test-thing",
						"existing-annotation":      "new-annotation-value",
						"new-annotation":           "hei-verden",
					},
					Labels: map[string]string{
						agentNameLabel:            "textor-the-doctor",
						remoteObjectClusterLabel:  "testcluster",
						remoteObjectNameHashLabel: "c346c8ceb5d104cc783d09b95e8ea7032c190948",
						"existing-label":          "new-label-value",
						"new-label":               "hello-world",
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}),
			// last state annotation is "space optimized" and so does not include the ignored labels and annotations
			expectedState: `{"apiVersion":"remote.example.corp/v1alpha1","kind":"RemoteThing","metadata":{"annotations":{"existing-annotation":"new-annotation-value","new-annotation":"hei-verden"},"labels":{"existing-label":"new-label-value","new-label":"hello-world"},"name":"my-test-thing"},"spec":{"kink":"","username":"Colonel Mustard"}}`,
		},

		/////////////////////////////////////////////////////////////////////////////////

		{
			name:            "missing syncagent-related annotations should be patched on the destination object",
			localCRD:        loadCRD("things"),
			pubRes:          remoteThingPR,
			performRequeues: true,

			remoteObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-test-thing",
					Finalizers: []string{
						deletionFinalizer,
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}, withGroupKind("remote.example.corp", "RemoteThing")),
			localObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testcluster-my-test-thing",
					Labels: map[string]string{
						agentNameLabel:            "textor-the-doctor",
						remoteObjectClusterLabel:  "testcluster",
						remoteObjectNameHashLabel: "c346c8ceb5d104cc783d09b95e8ea7032c190948",
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}),
			existingState: `{"apiVersion":"remote.example.corp/v1alpha1","kind":"RemoteThing","metadata":{"name":"my-test-thing"},"spec":{"kink":"","username":"Colonel Mustard"}}`,

			expectedRemoteObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-test-thing",
					Finalizers: []string{
						deletionFinalizer,
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}, withGroupKind("remote.example.corp", "RemoteThing")),
			expectedLocalObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testcluster-my-test-thing",
					Annotations: map[string]string{
						remoteObjectNameAnnotation: "my-test-thing",
					},
					Labels: map[string]string{
						agentNameLabel:            "textor-the-doctor",
						remoteObjectClusterLabel:  "testcluster",
						remoteObjectNameHashLabel: "c346c8ceb5d104cc783d09b95e8ea7032c190948",
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}),
			// last state annotation is "space optimized" and so does not include the ignored labels and annotations
			expectedState: `{"apiVersion":"remote.example.corp/v1alpha1","kind":"RemoteThing","metadata":{"name":"my-test-thing"},"spec":{"kink":"","username":"Colonel Mustard"}}`,
		},

		/////////////////////////////////////////////////////////////////////////////////

		{
			name:            "local updates should be based on the last-state annotation and ignore defaulted fields",
			localCRD:        loadCRD("things"),
			pubRes:          remoteThingPR,
			performRequeues: true,

			remoteObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-test-thing",
					Finalizers: []string{
						deletionFinalizer,
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Miss Scarlet",
				},
			}, withGroupKind("remote.example.corp", "RemoteThing")),
			localObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testcluster-my-test-thing",
					Labels: map[string]string{
						agentNameLabel:            "textor-the-doctor",
						remoteObjectClusterLabel:  "testcluster",
						remoteObjectNameHashLabel: "c346c8ceb5d104cc783d09b95e8ea7032c190948",
					},
					Annotations: map[string]string{
						remoteObjectNameAnnotation: "my-test-thing",
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
					Address:  "Hotdogstr. 13", // we assume this field was set by a local controller/webhook, unrelated to the Sync Agent
				},
			}),
			existingState: `{"apiVersion":"remote.example.corp/v1alpha1","kind":"RemoteThing","metadata":{"name":"my-test-thing"},"spec":{"kink":"","username":"Colonel Mustard"}}`,

			expectedRemoteObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-test-thing",
					Finalizers: []string{
						deletionFinalizer,
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Miss Scarlet",
				},
			}, withGroupKind("remote.example.corp", "RemoteThing")),
			expectedLocalObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testcluster-my-test-thing",
					Labels: map[string]string{
						agentNameLabel:            "textor-the-doctor",
						remoteObjectClusterLabel:  "testcluster",
						remoteObjectNameHashLabel: "c346c8ceb5d104cc783d09b95e8ea7032c190948",
					},
					Annotations: map[string]string{
						remoteObjectNameAnnotation: "my-test-thing",
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Miss Scarlet",
					// note how this is not wiped because it's not part of the last-known annotation
					Address: "Hotdogstr. 13",
				},
			}),
			expectedState: `{"apiVersion":"remote.example.corp/v1alpha1","kind":"RemoteThing","metadata":{"name":"my-test-thing"},"spec":{"kink":"","username":"Miss Scarlet"}}`,
		},

		/////////////////////////////////////////////////////////////////////////////////

		{
			name:     "remote deletions should be mirrored to the local object",
			localCRD: loadCRD("things"),
			pubRes:   remoteThingPR,

			// nothing will actually release the finalizer on the local object, so we cannot
			// requeue ad infinitum
			performRequeues: false,

			remoteObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-test-thing",
					Finalizers: []string{
						deletionFinalizer,
					},
					DeletionTimestamp: &nonEmptyTime, // here we trigger the deletion process
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}, withGroupKind("remote.example.corp", "RemoteThing")),
			localObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testcluster-my-test-thing",
					Finalizers: []string{
						"prevent-instant-deletion-in-tests",
					},
					Labels: map[string]string{
						agentNameLabel:            "textor-the-doctor",
						remoteObjectClusterLabel:  "testcluster",
						remoteObjectNameHashLabel: "c346c8ceb5d104cc783d09b95e8ea7032c190948",
					},
					Annotations: map[string]string{
						remoteObjectNameAnnotation: "my-test-thing",
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}),
			existingState: `{"apiVersion":"remote.example.corp/v1alpha1","kind":"RemoteThing","metadata":{"name":"my-test-thing"},"spec":{"kink":"","username":"Colonel Mustard"}}`,

			expectedRemoteObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-test-thing",
					Finalizers: []string{
						deletionFinalizer,
					},
					DeletionTimestamp: &nonEmptyTime,
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}, withGroupKind("remote.example.corp", "RemoteThing")),
			expectedLocalObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testcluster-my-test-thing",
					Finalizers: []string{
						"prevent-instant-deletion-in-tests",
					},
					DeletionTimestamp: &nonEmptyTime,
					Labels: map[string]string{
						agentNameLabel:            "textor-the-doctor",
						remoteObjectClusterLabel:  "testcluster",
						remoteObjectNameHashLabel: "c346c8ceb5d104cc783d09b95e8ea7032c190948",
					},
					Annotations: map[string]string{
						remoteObjectNameAnnotation: "my-test-thing",
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}),
			expectedState: `{"apiVersion":"remote.example.corp/v1alpha1","kind":"RemoteThing","metadata":{"name":"my-test-thing"},"spec":{"kink":"","username":"Colonel Mustard"}}`,
		},

		/////////////////////////////////////////////////////////////////////////////////

		{
			name:            "when the local object is gone, release the remote one",
			localCRD:        loadCRD("things"),
			pubRes:          remoteThingPR,
			performRequeues: false,

			remoteObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-test-thing",
					Finalizers: []string{
						deletionFinalizer,
						"prevent-object-from-disappearing",
					},
					DeletionTimestamp: &nonEmptyTime,
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}, withGroupKind("remote.example.corp", "RemoteThing")),
			localObject:   nil, // local object has been cleaned up
			existingState: "",

			expectedRemoteObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-test-thing",
					Finalizers: []string{
						"prevent-object-from-disappearing",
					},
					DeletionTimestamp: &nonEmptyTime,
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}, withGroupKind("remote.example.corp", "RemoteThing")),
			expectedLocalObject: nil,
			expectedState:       "",
		},

		/////////////////////////////////////////////////////////////////////////////////

		{
			name:            "do not attempt to update a local object that is in deletion",
			localCRD:        loadCRD("things"),
			pubRes:          remoteThingPR,
			performRequeues: true,

			remoteObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-test-thing",
					Finalizers: []string{
						deletionFinalizer,
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Miss Scarlet",
				},
			}, withGroupKind("remote.example.corp", "RemoteThing")),
			localObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testcluster-my-test-thing",
					Finalizers: []string{
						"prevent-instant-deletion-in-tests",
					},
					DeletionTimestamp: &nonEmptyTime,
					Labels: map[string]string{
						agentNameLabel:            "textor-the-doctor",
						remoteObjectClusterLabel:  "testcluster",
						remoteObjectNameHashLabel: "c346c8ceb5d104cc783d09b95e8ea7032c190948",
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}),
			existingState: `{"apiVersion":"remote.example.corp/v1alpha1","kind":"RemoteThing","metadata":{"name":"my-test-thing"},"spec":{"kink":"","username":"Colonel Mustard"}}`,

			expectedRemoteObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-test-thing",
					Finalizers: []string{
						deletionFinalizer,
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Miss Scarlet",
				},
			}, withGroupKind("remote.example.corp", "RemoteThing")),
			expectedLocalObject: newUnstructured(&dummyv1alpha1.Thing{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testcluster-my-test-thing",
					Finalizers: []string{
						"prevent-instant-deletion-in-tests",
					},
					DeletionTimestamp: &nonEmptyTime,
					Labels: map[string]string{
						agentNameLabel:            "textor-the-doctor",
						remoteObjectClusterLabel:  "testcluster",
						remoteObjectNameHashLabel: "c346c8ceb5d104cc783d09b95e8ea7032c190948",
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					// name is not updated
					Username: "Colonel Mustard",
				},
			}),
			expectedState: `{"apiVersion":"remote.example.corp/v1alpha1","kind":"RemoteThing","metadata":{"name":"my-test-thing"},"spec":{"kink":"","username":"Colonel Mustard"}}`,
		},
	}

	const stateNamespace = "kcp-system"

	for _, testcase := range testcases {
		t.Run(testcase.name, func(t *testing.T) {
			localClient := buildFakeClient(testcase.localObject)
			remoteClient := buildFakeClient(testcase.remoteObject)

			syncer, err := NewResourceSyncer(
				// zap.Must(zap.NewDevelopment()).Sugar(),
				zap.NewNop().Sugar(),
				localClient,
				remoteClient,
				testcase.pubRes,
				testcase.localCRD,
				func(rms *syncagentv1alpha1.ResourceMutationSpec) (mutation.Mutator, error) {
					return nil, nil
				},
				stateNamespace,
				"textor-the-doctor",
			)
			if err != nil {
				t.Fatalf("Failed to create syncer: %v", err)
			}

			ctx := t.Context()
			ctx = WithClusterName(ctx, clusterName)
			ctx = WithEventRecorder(ctx, record.NewFakeRecorder(99))

			// setup a custom state backend that we can prime
			var backend *kubernetesBackend
			syncer.newObjectStateStore = func(primaryObject, stateCluster syncSide) ObjectStateStore {
				// .Process() is called multiple times, but we want the state to persist between reconciles.
				if backend == nil {
					backend = newKubernetesBackend(stateNamespace, primaryObject, stateCluster)
					if testcase.existingState != "" {
						if err := backend.Put(ctx, testcase.remoteObject, clusterName, []byte(testcase.existingState)); err != nil {
							t.Fatalf("Failed to prime state store: %v", err)
						}
					}
				}

				return &objectStateStore{
					backend: backend,
				}
			}

			var requeue bool

			if testcase.performRequeues {
				target := testcase.remoteObject.DeepCopy()

				for i := 0; true; i++ {
					if i > 20 {
						t.Fatalf("Detected potential infinite loop, stopping after %d requeues.", i)
					}

					requeue, err = syncer.Process(ctx, target)
					if err != nil {
						break
					}

					if !requeue {
						break
					}

					if err = remoteClient.Get(ctx, ctrlruntimeclient.ObjectKeyFromObject(target), target); err != nil {
						// it's possible for the processing to have deleted the remote object,
						// so a NotFound is valid here
						if apierrors.IsNotFound(err) {
							break
						}

						t.Fatalf("Failed to get updated remote object: %v", err)
					}
				}
			} else {
				requeue, err = syncer.Process(ctx, testcase.remoteObject)
			}

			finalRemoteObject, getErr := getFinalObjectVersion(ctx, remoteClient, testcase.remoteObject, testcase.expectedRemoteObject)
			if getErr != nil {
				t.Fatalf("Failed to get final remote object: %v", getErr)
			}

			finalLocalObject, getErr := getFinalObjectVersion(ctx, localClient, testcase.localObject, testcase.expectedLocalObject)
			if getErr != nil {
				t.Fatalf("Failed to get final local object: %v", getErr)
			}

			if testcase.customVerification != nil {
				testcase.customVerification(t, requeue, err, finalRemoteObject, finalLocalObject, testcase)
			} else {
				if err != nil {
					t.Fatalf("Processing failed: %v", err)
				}

				assertObjectsEqual(t, "local", testcase.expectedLocalObject, finalLocalObject)
				assertObjectsEqual(t, "remote", testcase.expectedRemoteObject, finalRemoteObject)

				if testcase.expectedState != "" {
					if backend == nil {
						t.Fatal("Cannot check object state, state store was never instantiated.")
					}

					finalState, err := backend.Get(ctx, testcase.expectedRemoteObject, clusterName)
					if err != nil {
						t.Fatalf("Failed to get final state: %v", err)
					} else if !bytes.Equal(finalState, []byte(testcase.expectedState)) {
						t.Fatalf("States do not match:\n%s", diff.StringDiff(testcase.expectedState, string(finalState)))
					}
				}
			}
		})
	}
}

func TestSyncerProcessingSingleResourceWithStatus(t *testing.T) {
	type testcase struct {
		name                 string
		localCRD             *apiextensionsv1.CustomResourceDefinition
		pubRes               *syncagentv1alpha1.PublishedResource
		remoteObject         *unstructured.Unstructured
		localObject          *unstructured.Unstructured
		existingState        string
		performRequeues      bool
		expectedRemoteObject *unstructured.Unstructured
		expectedLocalObject  *unstructured.Unstructured
		expectedState        string
		customVerification   func(t *testing.T, requeue bool, processErr error, finalRemoteObject *unstructured.Unstructured, finalLocalObject *unstructured.Unstructured, testcase testcase)
	}

	clusterName := logicalcluster.Name("testcluster")

	remoteThingPR := &syncagentv1alpha1.PublishedResource{
		Spec: syncagentv1alpha1.PublishedResourceSpec{
			Resource: syncagentv1alpha1.SourceResourceDescriptor{
				APIGroup: dummyv1alpha1.GroupName,
				Version:  dummyv1alpha1.GroupVersion,
				Kind:     "ThingWithStatusSubresource",
			},
			Projection: &syncagentv1alpha1.ResourceProjection{
				Kind: "RemoteThing",
			},
			// include explicit naming rules to be independent of possible changes to the defaults
			Naming: &syncagentv1alpha1.ResourceNaming{
				Name: "$remoteClusterName-$remoteName", // Things are Cluster-scoped
			},
		},
	}

	testcases := []testcase{

		/////////////////////////////////////////////////////////////////////////////////

		{
			name:            "everything is already in perfect shape, nothing to do",
			localCRD:        loadCRD("thingwithstatussubresources"),
			pubRes:          remoteThingPR,
			performRequeues: true,

			remoteObject: newUnstructured(&dummyv1alpha1.ThingWithStatusSubresource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-test-thing",
					Finalizers: []string{
						deletionFinalizer,
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
				Status: dummyv1alpha1.ThingStatus{
					CurrentVersion: "v1",
				},
			}, withGroupKind("remote.example.corp", "RemoteThing")),
			localObject: newUnstructured(&dummyv1alpha1.ThingWithStatusSubresource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testcluster-my-test-thing",
					Labels: map[string]string{
						agentNameLabel:            "textor-the-doctor",
						remoteObjectClusterLabel:  "testcluster",
						remoteObjectNameHashLabel: "c346c8ceb5d104cc783d09b95e8ea7032c190948",
					},
					Annotations: map[string]string{
						remoteObjectNameAnnotation: "my-test-thing",
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
				Status: dummyv1alpha1.ThingStatus{
					CurrentVersion: "v1",
				},
			}),
			existingState: `{"apiVersion":"remote.example.corp/v1alpha1","kind":"RemoteThing","metadata":{"name":"my-test-thing"},"spec":{"kink":"","username":"Colonel Mustard"}}`,

			expectedRemoteObject: newUnstructured(&dummyv1alpha1.ThingWithStatusSubresource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-test-thing",
					Finalizers: []string{
						deletionFinalizer,
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
				Status: dummyv1alpha1.ThingStatus{
					CurrentVersion: "v1",
				},
			}, withGroupKind("remote.example.corp", "RemoteThing")),
			expectedLocalObject: newUnstructured(&dummyv1alpha1.ThingWithStatusSubresource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testcluster-my-test-thing",
					Labels: map[string]string{
						agentNameLabel:            "textor-the-doctor",
						remoteObjectClusterLabel:  "testcluster",
						remoteObjectNameHashLabel: "c346c8ceb5d104cc783d09b95e8ea7032c190948",
					},
					Annotations: map[string]string{
						remoteObjectNameAnnotation: "my-test-thing",
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
				Status: dummyv1alpha1.ThingStatus{
					CurrentVersion: "v1",
				},
			}),
			expectedState: `{"apiVersion":"remote.example.corp/v1alpha1","kind":"RemoteThing","metadata":{"name":"my-test-thing"},"spec":{"kink":"","username":"Colonel Mustard"}}`,
		},

		/////////////////////////////////////////////////////////////////////////////////

		{
			name:            "spec is in-sync, but status is not yet updated in remote object",
			localCRD:        loadCRD("thingwithstatussubresources"),
			pubRes:          remoteThingPR,
			performRequeues: true,

			remoteObject: newUnstructured(&dummyv1alpha1.ThingWithStatusSubresource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-test-thing",
					Finalizers: []string{
						deletionFinalizer,
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}, withGroupKind("remote.example.corp", "RemoteThing")),
			localObject: newUnstructured(&dummyv1alpha1.ThingWithStatusSubresource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testcluster-my-test-thing",
					Labels: map[string]string{
						agentNameLabel:            "textor-the-doctor",
						remoteObjectClusterLabel:  "testcluster",
						remoteObjectNameHashLabel: "c346c8ceb5d104cc783d09b95e8ea7032c190948",
					},
					Annotations: map[string]string{
						remoteObjectNameAnnotation: "my-test-thing",
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
				Status: dummyv1alpha1.ThingStatus{
					CurrentVersion: "v1",
				},
			}),
			existingState: `{"apiVersion":"remote.example.corp/v1alpha1","kind":"RemoteThing","metadata":{"name":"my-test-thing"},"spec":{"kink":"","username":"Colonel Mustard"}}`,

			expectedRemoteObject: newUnstructured(&dummyv1alpha1.ThingWithStatusSubresource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-test-thing",
					Finalizers: []string{
						deletionFinalizer,
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
				Status: dummyv1alpha1.ThingStatus{
					CurrentVersion: "v1",
				},
			}, withGroupKind("remote.example.corp", "RemoteThing")),
			expectedLocalObject: newUnstructured(&dummyv1alpha1.ThingWithStatusSubresource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testcluster-my-test-thing",
					Labels: map[string]string{
						agentNameLabel:            "textor-the-doctor",
						remoteObjectClusterLabel:  "testcluster",
						remoteObjectNameHashLabel: "c346c8ceb5d104cc783d09b95e8ea7032c190948",
					},
					Annotations: map[string]string{
						remoteObjectNameAnnotation: "my-test-thing",
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
				Status: dummyv1alpha1.ThingStatus{
					CurrentVersion: "v1",
				},
			}),
			expectedState: `{"apiVersion":"remote.example.corp/v1alpha1","kind":"RemoteThing","metadata":{"name":"my-test-thing"},"spec":{"kink":"","username":"Colonel Mustard"}}`,
		},
	}

	const stateNamespace = "kcp-system"

	for _, testcase := range testcases {
		t.Run(testcase.name, func(t *testing.T) {
			localClient := buildFakeClientWithStatus(testcase.localObject)
			remoteClient := buildFakeClientWithStatus(testcase.remoteObject)

			syncer, err := NewResourceSyncer(
				// zap.Must(zap.NewDevelopment()).Sugar(),
				zap.NewNop().Sugar(),
				localClient,
				remoteClient,
				testcase.pubRes,
				testcase.localCRD,
				func(rms *syncagentv1alpha1.ResourceMutationSpec) (mutation.Mutator, error) {
					return nil, nil
				},
				stateNamespace,
				"textor-the-doctor",
			)
			if err != nil {
				t.Fatalf("Failed to create syncer: %v", err)
			}

			ctx := t.Context()
			ctx = WithClusterName(ctx, clusterName)
			ctx = WithEventRecorder(ctx, record.NewFakeRecorder(99))

			// setup a custom state backend that we can prime
			var backend *kubernetesBackend
			syncer.newObjectStateStore = func(primaryObject, stateCluster syncSide) ObjectStateStore {
				// .Process() is called multiple times, but we want the state to persist between reconciles.
				if backend == nil {
					backend = newKubernetesBackend(stateNamespace, primaryObject, stateCluster)
					if testcase.existingState != "" {
						if err := backend.Put(ctx, testcase.remoteObject, clusterName, []byte(testcase.existingState)); err != nil {
							t.Fatalf("Failed to prime state store: %v", err)
						}
					}
				}

				return &objectStateStore{
					backend: backend,
				}
			}

			var requeue bool

			if testcase.performRequeues {
				target := testcase.remoteObject.DeepCopy()

				for i := 0; true; i++ {
					if i > 20 {
						t.Fatalf("Detected potential infinite loop, stopping after %d requeues.", i)
					}

					requeue, err = syncer.Process(ctx, target)
					if err != nil {
						break
					}

					if !requeue {
						break
					}

					if err = remoteClient.Get(ctx, ctrlruntimeclient.ObjectKeyFromObject(target), target); err != nil {
						// it's possible for the processing to have deleted the remote object,
						// so a NotFound is valid here
						if apierrors.IsNotFound(err) {
							break
						}

						t.Fatalf("Failed to get updated remote object: %v", err)
					}
				}
			} else {
				requeue, err = syncer.Process(ctx, testcase.remoteObject)
			}

			finalRemoteObject, getErr := getFinalObjectVersion(ctx, remoteClient, testcase.remoteObject, testcase.expectedRemoteObject)
			if getErr != nil {
				t.Fatalf("Failed to get final remote object: %v", getErr)
			}

			finalLocalObject, getErr := getFinalObjectVersion(ctx, localClient, testcase.localObject, testcase.expectedLocalObject)
			if getErr != nil {
				t.Fatalf("Failed to get final local object: %v", getErr)
			}

			if testcase.customVerification != nil {
				testcase.customVerification(t, requeue, err, finalRemoteObject, finalLocalObject, testcase)
			} else {
				if err != nil {
					t.Fatalf("Processing failed: %v", err)
				}

				assertObjectsEqual(t, "local", testcase.expectedLocalObject, finalLocalObject)
				assertObjectsEqual(t, "remote", testcase.expectedRemoteObject, finalRemoteObject)

				if testcase.expectedState != "" {
					if backend == nil {
						t.Fatal("Cannot check object state, state store was never instantiated.")
					}

					finalState, err := backend.Get(ctx, testcase.expectedRemoteObject, clusterName)
					if err != nil {
						t.Fatalf("Failed to get final state: %v", err)
					} else if !bytes.Equal(finalState, []byte(testcase.expectedState)) {
						t.Fatalf("States do not match:\n%s", diff.StringDiff(testcase.expectedState, string(finalState)))
					}
				}
			}
		})
	}
}

func assertObjectsEqual(t *testing.T, kind string, expected, actual *unstructured.Unstructured) {
	if expected == nil {
		if actual != nil {
			t.Errorf("Expected %s object to not exist anymore, but does:\n%s", kind, diff.ObjectDiff(expected, actual))
		}

		return
	}

	if actual == nil {
		t.Errorf("Expected %s object to exist, but does not anymore:\n%s", kind, diff.ObjectDiff(expected, actual))
		return
	}

	// ignore runtime metadata
	expected.SetResourceVersion("")
	actual.SetResourceVersion("")

	expected.SetCreationTimestamp(nonEmptyTime)
	actual.SetCreationTimestamp(nonEmptyTime)

	// comparing deletion timestamps just means checking _if_ the timestamp is set, not what time it actually is
	if expected.GetDeletionTimestamp() != nil {
		expected.SetDeletionTimestamp(&nonEmptyTime)
	}

	if actual.GetDeletionTimestamp() != nil {
		actual.SetDeletionTimestamp(&nonEmptyTime)
	}

	if !diff.SemanticallyEqual(expected, actual) {
		t.Errorf("%s object does not match expectation:\n%s", kind, diff.ObjectDiff(expected, actual))
	}
}

func getFinalObjectVersion(ctx context.Context, client ctrlruntimeclient.Client, candidates ...*unstructured.Unstructured) (*unstructured.Unstructured, error) {
	var baseObject *unstructured.Unstructured

	for i, candidate := range candidates {
		if candidate != nil {
			baseObject = candidates[i]
			break
		}
	}

	// not all tests involve a local or remote object
	if baseObject == nil {
		return nil, nil
	}

	obj := baseObject.DeepCopy()
	if err := client.Get(ctx, ctrlruntimeclient.ObjectKeyFromObject(baseObject), obj); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}

		return nil, err
	}

	return obj, nil
}
