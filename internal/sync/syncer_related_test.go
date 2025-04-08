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
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"
	"go.uber.org/zap"

	dummyv1alpha1 "github.com/kcp-dev/api-syncagent/internal/sync/apis/dummy/v1alpha1"
	"github.com/kcp-dev/api-syncagent/internal/test/diff"
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/kontext"
)

func newPublishedResources(relatedResources []syncagentv1alpha1.RelatedResourceSpec) *syncagentv1alpha1.PublishedResource {
	return &syncagentv1alpha1.PublishedResource{
		Spec: syncagentv1alpha1.PublishedResourceSpec{
			Resource: syncagentv1alpha1.SourceResourceDescriptor{
				APIGroup: dummyv1alpha1.GroupName,
				Version:  dummyv1alpha1.GroupVersion,
				Kind:     "NamespacedThing",
			},
			Projection: &syncagentv1alpha1.ResourceProjection{
				Kind: "RemoteThing",
			},
			Naming: &syncagentv1alpha1.ResourceNaming{
				Name: "$remoteClusterName-$remoteName",
			},
			Related: relatedResources,
		},
	}
}
func TestSyncerProcessingRelatedResources(t *testing.T) {
	const stateNamespace = "kcp-system"

	type testcase struct {
		name                        string
		remoteAPIGroup              string
		localCRD                    *apiextensionsv1.CustomResourceDefinition
		pubRes                      *syncagentv1alpha1.PublishedResource
		remoteRelatedSecret         *unstructured.Unstructured
		localRelatedSecret          *unstructured.Unstructured
		remoteObject                *unstructured.Unstructured
		localObject                 *unstructured.Unstructured
		existingState               string
		performRequeues             bool
		expectedRemoteRelatedSecret *unstructured.Unstructured
		expectedLocalRelatedSecret  *unstructured.Unstructured
		expectedState               string
	}

	clusterName := logicalcluster.Name("testcluster")

	testcases := []testcase{
		{
			name:           "optional related resource of kcp origin does not exist in the source",
			remoteAPIGroup: "remote.example.corp",
			localCRD:       loadCRD("things"),
			pubRes: newPublishedResources([]syncagentv1alpha1.RelatedResourceSpec{
				{
					Identifier: "optional-secret",
					Origin:     "kcp",
					Kind:       "Secret",
					Reference: syncagentv1alpha1.RelatedResourceReference{
						Name: syncagentv1alpha1.ResourceLocator{
							Path: "metadata.name",
							Regex: &syncagentv1alpha1.RegexResourceLocator{
								Replacement: "optional-credentials",
							},
						},
					},
				},
			}),
			performRequeues:     true,
			remoteRelatedSecret: nil,
			localRelatedSecret:  nil,
			remoteObject: newUnstructured(&dummyv1alpha1.NamespacedThing{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-test-thing",
					Namespace: stateNamespace,
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}, withGroupKind("remote.example.corp", "RemoteThing")),
			localObject: newUnstructured(&dummyv1alpha1.NamespacedThing{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testcluster-my-test-thing",
					Namespace: stateNamespace,
					Labels: map[string]string{
						agentNameLabel:            "textor-the-doctor",
						remoteObjectClusterLabel:  "testcluster",
						remoteObjectNameHashLabel: "c346c8ceb5d104cc783d09b95e8ea7032c190948",
					},
					Annotations: map[string]string{
						remoteObjectNameAnnotation:      "my-test-thing",
						remoteObjectNamespaceAnnotation: stateNamespace,
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}),
			existingState:               "",
			expectedRemoteRelatedSecret: nil,
			expectedLocalRelatedSecret:  nil,
			expectedState:               "",
		},
		{
			name:           "mandatory related resource of kcp origin exists in the source side",
			remoteAPIGroup: "remote.example.corp",
			localCRD:       loadCRD("things"),
			pubRes: newPublishedResources([]syncagentv1alpha1.RelatedResourceSpec{
				{
					Identifier: "mandatory-credentials",
					Origin:     "kcp",
					Kind:       "Secret",
					Reference: syncagentv1alpha1.RelatedResourceReference{
						Name: syncagentv1alpha1.ResourceLocator{
							Path: "metadata.name",
							Regex: &syncagentv1alpha1.RegexResourceLocator{
								Replacement: "mandatory-credentials",
							},
						},
					},
				},
			}),
			performRequeues: true,
			remoteRelatedSecret: newUnstructured(&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mandatory-credentials",
					Namespace: stateNamespace,
					Labels: map[string]string{
						"hello": "world",
					},
				},
				Data: map[string][]byte{
					"password": []byte("hunter2"),
				},
			}),
			localRelatedSecret: newUnstructured(&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mandatory-credentials",
					Namespace: stateNamespace,
					Labels: map[string]string{
						"hello": "world",
					},
				},
				Data: map[string][]byte{
					"password": []byte("hunter2"),
				},
			}),
			remoteObject: newUnstructured(&dummyv1alpha1.NamespacedThing{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-test-thing",
					Namespace: stateNamespace,
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}, withGroupKind("remote.example.corp", "RemoteThing")),
			localObject: newUnstructured(&dummyv1alpha1.NamespacedThing{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testcluster-my-test-thing",
					Namespace: stateNamespace,
					Labels: map[string]string{
						agentNameLabel:            "textor-the-doctor",
						remoteObjectClusterLabel:  "testcluster",
						remoteObjectNameHashLabel: "c346c8ceb5d104cc783d09b95e8ea7032c190948",
					},
					Annotations: map[string]string{
						remoteObjectNameAnnotation:      "my-test-thing",
						remoteObjectNamespaceAnnotation: stateNamespace,
					},
				},
				Spec: dummyv1alpha1.ThingSpec{
					Username: "Colonel Mustard",
				},
			}),
			existingState: "",
			expectedRemoteRelatedSecret: newUnstructured(&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mandatory-credentials",
					Namespace: stateNamespace,
					Labels: map[string]string{
						"hello": "world",
					},
					Finalizers: []string{
						deletionFinalizer,
					},
				},
				Data: map[string][]byte{
					"password": []byte("hunter2"),
				},
			}),
			expectedLocalRelatedSecret: newUnstructured(&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mandatory-credentials",
					Namespace: stateNamespace,
					Labels: map[string]string{
						"hello": "world",
					},
				},
				Data: map[string][]byte{
					"password": []byte("hunter2"),
				},
			}),
			expectedState: `{"apiVersion":"v1","data":{"password":"aHVudGVyMg=="},"kind":"Secret","metadata":{"labels":{"hello":"world"},"name":"mandatory-credentials","namespace":"kcp-system"}}`,
		},
	}

	for _, testcase := range testcases {
		t.Run(testcase.name, func(t *testing.T) {
			localClient := buildFakeClient(testcase.localObject, testcase.localRelatedSecret)
			remoteClient := buildFakeClient(testcase.remoteObject, testcase.remoteRelatedSecret)

			syncer, err := NewResourceSyncer(
				// zap.Must(zap.NewDevelopment()).Sugar(),
				zap.NewNop().Sugar(),
				localClient,
				remoteClient,
				testcase.pubRes,
				testcase.localCRD,
				testcase.remoteAPIGroup,
				nil,
				stateNamespace,
				"textor-the-doctor",
			)
			if err != nil {
				t.Fatalf("Failed to create syncer: %v", err)
			}

			localCtx := context.Background()
			remoteCtx := kontext.WithCluster(localCtx, clusterName)
			ctx := NewContext(localCtx, remoteCtx)

			// setup a custom state backend that we can prime
			var backend *kubernetesBackend
			syncer.newObjectStateStore = func(primaryObject, stateCluster syncSide) ObjectStateStore {
				// .Process() is called multiple times, but we want the state to persist between reconciles.
				if backend == nil {
					backend = newKubernetesBackend(stateNamespace, primaryObject, stateCluster)
					if testcase.existingState != "" {
						if err := backend.Put(testcase.remoteObject, clusterName, []byte(testcase.existingState)); err != nil {
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

					if err = remoteClient.Get(remoteCtx, ctrlruntimeclient.ObjectKeyFromObject(target), target); err != nil {
						// it's possible for the processing to have deleted the remote object,
						// so a NotFound is valid here
						if apierrors.IsNotFound(err) {
							break
						}

						t.Fatalf("Failed to get updated remote object: %v", err)
					}
				}
			} else {
				_, err = syncer.Process(ctx, testcase.remoteObject)
			}

			finalRemoteObject, getErr := getFinalObjectVersion(remoteCtx, remoteClient, testcase.remoteRelatedSecret, testcase.expectedRemoteRelatedSecret)
			if getErr != nil {
				t.Fatalf("Failed to get final remote object: %v", getErr)
			}

			finalLocalObject, getErr := getFinalObjectVersion(localCtx, localClient, testcase.localRelatedSecret, testcase.expectedLocalRelatedSecret)
			if getErr != nil {
				t.Fatalf("Failed to get final local object: %v", getErr)
			}

			if err != nil {
				t.Fatalf("Processing failed: %v", err)
			}

			assertObjectsEqual(t, "local", testcase.expectedLocalRelatedSecret, finalLocalObject)
			assertObjectsEqual(t, "remote", testcase.expectedRemoteRelatedSecret, finalRemoteObject)

			if testcase.expectedState != "" {
				if backend == nil {
					t.Fatal("Cannot check object state, state store was never instantiated.")
				}

				finalState, err := backend.Get(testcase.expectedRemoteRelatedSecret, clusterName)
				if err != nil {
					t.Fatalf("Failed to get final state: %v", err)
				} else if !bytes.Equal(finalState, []byte(testcase.expectedState)) {
					t.Fatalf("States do not match:\n%s", diff.StringDiff(testcase.expectedState, string(finalState)))
				}
			}

		})
	}
}
