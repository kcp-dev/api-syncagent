//go:build e2e

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
	"context"
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/api-syncagent/internal/test/diff"
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"
	"github.com/kcp-dev/api-syncagent/test/crds"
	"github.com/kcp-dev/api-syncagent/test/utils"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrlruntime "sigs.k8s.io/controller-runtime"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/kontext"
)

func TestSyncRelatedObjects(t *testing.T) {
	const apiExportName = "kcp.example.com"

	ctrlruntime.SetLogger(logr.Discard())

	testcases := []struct {
		// the name of this testcase
		name string
		//the org workspace everything should happen in
		workspace logicalcluster.Name
		// the configuration for the related resource
		relatedConfig syncagentv1alpha1.RelatedResourceSpec
		// the primary object created by the user in kcp
		mainResource crds.Crontab
		// the original related object (will automatically be created on either the
		// kcp or service side, depending on the relatedConfig above)
		sourceRelatedObject corev1.Secret

		// expectation: this is how the copy of the related object should look
		// like after the sync has completed
		expectedSyncedRelatedObject corev1.Secret
		// expectation: how the original primary object should have been updated
		// (not the primary object's copy, but the source)
		//
		// not yet implemented
		// expectedUpdatedMainObject crds.Crontab
	}{
		{
			name:      "sync referenced Secret up from service cluster to kcp",
			workspace: "sync-referenced-secret-up",
			mainResource: crds.Crontab{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-crontab",
					Namespace: "default",
				},
				Spec: crds.CrontabSpec{
					CronSpec: "* * *",
					Image:    "ubuntu:latest",
				},
			},
			relatedConfig: syncagentv1alpha1.RelatedResourceSpec{
				Identifier: "credentials",
				Origin:     "service",
				Kind:       "Secret",
				Object: syncagentv1alpha1.RelatedResourceObject{
					RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
						Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
							Path: "metadata.name", // irrelevant
							Regex: &syncagentv1alpha1.RegularExpression{
								Replacement: "my-credentials",
							},
						},
					},
				},
			},
			sourceRelatedObject: corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-credentials",
					Namespace: "synced-default",
				},
				Data: map[string][]byte{
					"password": []byte("hunter2"),
				},
				Type: corev1.SecretTypeOpaque,
			},

			expectedSyncedRelatedObject: corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-credentials",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"password": []byte("hunter2"),
				},
				Type: corev1.SecretTypeOpaque,
			},
		},

		//////////////////////////////////////////////////////////////////////////////////////////////

		{
			name:      "sync referenced Secret down from kcp to the service cluster",
			workspace: "sync-referenced-secret-down",
			mainResource: crds.Crontab{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-crontab",
					Namespace: "default",
				},
				Spec: crds.CrontabSpec{
					CronSpec: "* * *",
					Image:    "ubuntu:latest",
				},
			},
			relatedConfig: syncagentv1alpha1.RelatedResourceSpec{
				Identifier: "credentials",
				Origin:     "kcp",
				Kind:       "Secret",
				Object: syncagentv1alpha1.RelatedResourceObject{
					RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
						Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
							Path: "metadata.name", // irrelevant
							Regex: &syncagentv1alpha1.RegularExpression{
								Replacement: "my-credentials",
							},
						},
					},
				},
			},
			sourceRelatedObject: corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-credentials",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"password": []byte("hunter2"),
				},
				Type: corev1.SecretTypeOpaque,
			},

			expectedSyncedRelatedObject: corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-credentials",
					Namespace: "synced-default",
				},
				Data: map[string][]byte{
					"password": []byte("hunter2"),
				},
				Type: corev1.SecretTypeOpaque,
			},
		},

		//////////////////////////////////////////////////////////////////////////////////////////////

		// {
		// 	name:      "sync referenced Secret up into a new namespace",
		// 	workspace: "sync-referenced-secret-up-namespace",
		// 	mainResource: crds.Crontab{
		// 		ObjectMeta: metav1.ObjectMeta{
		// 			Name:      "my-crontab",
		// 			Namespace: "default",
		// 		},
		// 		Spec: crds.CrontabSpec{
		// 			CronSpec: "* * *",
		// 			Image:    "ubuntu:latest",
		// 		},
		// 	},
		// 	relatedConfig: syncagentv1alpha1.RelatedResourceSpec{
		// 		Identifier: "credentials",
		// 		Origin:     "service",
		// 		Kind:       "Secret",
		// 		Object: syncagentv1alpha1.RelatedResourceObject{
		// 			RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
		// 				Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
		// 					Path: "metadata.name", // irrelevant
		// 					Regex: &syncagentv1alpha1.RegularExpression{
		// 						Replacement: "my-credentials",
		// 					},
		// 				},
		// 			},
		// 		},
		// 		Destination: syncagentv1alpha1.RelatedResourceDestination{
		// 			RelatedResourceDestinationSpec: syncagentv1alpha1.RelatedResourceDestinationSpec{
		// 				Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
		// 					Path: "metadata.name", // irrelevant
		// 					Regex: &syncagentv1alpha1.RegularExpression{
		// 						Replacement: "my-credentials",
		// 					},
		// 				},
		// 			},
		// 			Namespace: &syncagentv1alpha1.RelatedResourceDestinationSpec{
		// 				Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
		// 					Path: "metadata.name", // irrelevant
		// 					Regex: &syncagentv1alpha1.RegularExpression{
		// 						Replacement: "new-namespace",
		// 					},
		// 				},
		// 			},
		// 		},
		// 	},
		// 	sourceRelatedObject: corev1.Secret{
		// 		ObjectMeta: metav1.ObjectMeta{
		// 			Name:      "my-credentials",
		// 			Namespace: "synced-default",
		// 		},
		// 		Data: map[string][]byte{
		// 			"password": []byte("hunter2"),
		// 		},
		// 		Type: corev1.SecretTypeOpaque,
		// 	},

		// 	expectedSyncedRelatedObject: corev1.Secret{
		// 		ObjectMeta: metav1.ObjectMeta{
		// 			Name:      "my-credentials",
		// 			Namespace: "new-namespace",
		// 		},
		// 		Data: map[string][]byte{
		// 			"password": []byte("hunter2"),
		// 		},
		// 		Type: corev1.SecretTypeOpaque,
		// 	},
		// },

		// //////////////////////////////////////////////////////////////////////////////////////////////

		// {
		// 	name:      "sync referenced Secret down into a new namespace",
		// 	workspace: "sync-referenced-secret-down-namespace",
		// 	mainResource: crds.Crontab{
		// 		ObjectMeta: metav1.ObjectMeta{
		// 			Name:      "my-crontab",
		// 			Namespace: "default",
		// 		},
		// 		Spec: crds.CrontabSpec{
		// 			CronSpec: "* * *",
		// 			Image:    "ubuntu:latest",
		// 		},
		// 	},
		// 	relatedConfig: syncagentv1alpha1.RelatedResourceSpec{
		// 		Identifier: "credentials",
		// 		Origin:     "kcp",
		// 		Kind:       "Secret",
		// 		Object: syncagentv1alpha1.RelatedResourceObject{
		// 			RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
		// 				Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
		// 					Path: "metadata.name", // irrelevant
		// 					Regex: &syncagentv1alpha1.RegularExpression{
		// 						Replacement: "my-credentials",
		// 					},
		// 				},
		// 			},
		// 		},
		// 		Destination: syncagentv1alpha1.RelatedResourceDestination{
		// 			RelatedResourceDestinationSpec: syncagentv1alpha1.RelatedResourceDestinationSpec{
		// 				Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
		// 					Path: "metadata.name", // irrelevant
		// 					Regex: &syncagentv1alpha1.RegularExpression{
		// 						Replacement: "my-credentials",
		// 					},
		// 				},
		// 			},
		// 			Namespace: &syncagentv1alpha1.RelatedResourceDestinationSpec{
		// 				Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
		// 					Path: "metadata.name", // irrelevant
		// 					Regex: &syncagentv1alpha1.RegularExpression{
		// 						Replacement: "new-namespace",
		// 					},
		// 				},
		// 			},
		// 		},
		// 	},
		// 	sourceRelatedObject: corev1.Secret{
		// 		ObjectMeta: metav1.ObjectMeta{
		// 			Name:      "my-credentials",
		// 			Namespace: "default",
		// 		},
		// 		Data: map[string][]byte{
		// 			"password": []byte("hunter2"),
		// 		},
		// 		Type: corev1.SecretTypeOpaque,
		// 	},

		// 	expectedSyncedRelatedObject: corev1.Secret{
		// 		ObjectMeta: metav1.ObjectMeta{
		// 			Name:      "my-credentials",
		// 			Namespace: "new-namespace",
		// 		},
		// 		Data: map[string][]byte{
		// 			"password": []byte("hunter2"),
		// 		},
		// 		Type: corev1.SecretTypeOpaque,
		// 	},
		// },

		// //////////////////////////////////////////////////////////////////////////////////////////////

		// {
		// 	name:      "sync referenced Secret up from a foreign namespace",
		// 	workspace: "sync-referenced-secret-up-foreign-namespace",
		// 	mainResource: crds.Crontab{
		// 		ObjectMeta: metav1.ObjectMeta{
		// 			Name:      "my-crontab",
		// 			Namespace: "default",
		// 		},
		// 		Spec: crds.CrontabSpec{
		// 			CronSpec: "* * *",
		// 			Image:    "ubuntu:latest",
		// 		},
		// 	},
		// 	relatedConfig: syncagentv1alpha1.RelatedResourceSpec{
		// 		Identifier: "credentials",
		// 		Origin:     "service",
		// 		Kind:       "Secret",
		// 		Object: syncagentv1alpha1.RelatedResourceObject{
		// 			RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
		// 				Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
		// 					Path: "metadata.name", // irrelevant
		// 					Regex: &syncagentv1alpha1.RegularExpression{
		// 						Replacement: "my-credentials",
		// 					},
		// 				},
		// 			},
		// 			Namespace: &syncagentv1alpha1.RelatedResourceObjectSpec{
		// 				Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
		// 					Path: "metadata.name", // irrelevant
		// 					Regex: &syncagentv1alpha1.RegularExpression{
		// 						Replacement: "other-namespace",
		// 					},
		// 				},
		// 			},
		// 		},
		// 		Destination: syncagentv1alpha1.RelatedResourceDestination{
		// 			RelatedResourceDestinationSpec: syncagentv1alpha1.RelatedResourceDestinationSpec{
		// 				Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
		// 					Path: "metadata.name", // irrelevant
		// 					Regex: &syncagentv1alpha1.RegularExpression{
		// 						Replacement: "my-credentials",
		// 					},
		// 				},
		// 			},
		// 		},
		// 	},
		// 	sourceRelatedObject: corev1.Secret{
		// 		ObjectMeta: metav1.ObjectMeta{
		// 			Name:      "my-credentials",
		// 			Namespace: "other-namespace",
		// 		},
		// 		Data: map[string][]byte{
		// 			"password": []byte("hunter2"),
		// 		},
		// 		Type: corev1.SecretTypeOpaque,
		// 	},

		// 	expectedSyncedRelatedObject: corev1.Secret{
		// 		ObjectMeta: metav1.ObjectMeta{
		// 			Name:      "my-credentials",
		// 			Namespace: "default",
		// 		},
		// 		Data: map[string][]byte{
		// 			"password": []byte("hunter2"),
		// 		},
		// 		Type: corev1.SecretTypeOpaque,
		// 	},
		// },

		//////////////////////////////////////////////////////////////////////////////////////////////

		{
			name:      "find Secret based on label selector",
			workspace: "sync-selected-secret-up",
			mainResource: crds.Crontab{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-crontab",
					Namespace: "default",
				},
				Spec: crds.CrontabSpec{
					CronSpec: "* * *",
					Image:    "ubuntu:latest",
				},
			},
			relatedConfig: syncagentv1alpha1.RelatedResourceSpec{
				Identifier: "credentials",
				Origin:     "service",
				Kind:       "Secret",
				Object: syncagentv1alpha1.RelatedResourceObject{
					RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
						Selector: &syncagentv1alpha1.RelatedResourceObjectSelector{
							LabelSelector: metav1.LabelSelector{
								MatchLabels: map[string]string{
									"find": "me",
								},
							},
							Rewrite: syncagentv1alpha1.RelatedResourceSelectorRewrite{
								// TODO: Use template instead of regex once that is implemented.
								Regex: &syncagentv1alpha1.RegularExpression{
									Replacement: "my-credentials",
								},
							},
						},
					},
				},
			},
			sourceRelatedObject: corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "unknown-name",
					Namespace: "synced-default",
					Labels: map[string]string{
						"find": "me",
					},
				},
				Data: map[string][]byte{
					"password": []byte("hunter2"),
				},
				Type: corev1.SecretTypeOpaque,
			},

			expectedSyncedRelatedObject: corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-credentials",
					Namespace: "default",
					Labels: map[string]string{
						"find": "me",
					},
				},
				Data: map[string][]byte{
					"password": []byte("hunter2"),
				},
				Type: corev1.SecretTypeOpaque,
			},
		},
	}

	for _, testcase := range testcases {
		t.Run(testcase.name, func(t *testing.T) {
			ctx := t.Context()

			// setup a test environment in kcp
			orgKubconfig := utils.CreateOrganization(t, ctx, testcase.workspace, apiExportName)

			// start a service cluster
			envtestKubeconfig, envtestClient, _ := utils.RunEnvtest(t, []string{
				"test/crds/crontab.yaml",
			})

			// publish Crontabs and Backups
			t.Logf("Publishing CRDs…")
			prCrontabs := &syncagentv1alpha1.PublishedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "publish-crontabs",
				},
				Spec: syncagentv1alpha1.PublishedResourceSpec{
					Resource: syncagentv1alpha1.SourceResourceDescriptor{
						APIGroup: "example.com",
						Version:  "v1",
						Kind:     "CronTab",
					},
					// These rules make finding the local object easier, but should not be used in production.
					Naming: &syncagentv1alpha1.ResourceNaming{
						Name:      "{{ .Object.metadata.name }}",
						Namespace: "synced-{{ .Object.metadata.namespace }}",
					},
					Projection: &syncagentv1alpha1.ResourceProjection{
						Group: "kcp.example.com",
					},
					Related: []syncagentv1alpha1.RelatedResourceSpec{testcase.relatedConfig},
				},
			}

			if err := envtestClient.Create(ctx, prCrontabs); err != nil {
				t.Fatalf("Failed to create PublishedResource: %v", err)
			}

			// start the agent in the background to update the APIExport with the CronTabs API
			utils.RunAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, apiExportName)

			// wait until the API is available
			teamCtx := kontext.WithCluster(ctx, logicalcluster.Name(fmt.Sprintf("root:%s:team-1", testcase.workspace)))
			kcpClient := utils.GetKcpAdminClusterClient(t)
			utils.WaitForBoundAPI(t, teamCtx, kcpClient, schema.GroupVersionResource{
				Group:    apiExportName,
				Version:  "v1",
				Resource: "crontabs",
			})

			// create a Crontab object in a team workspace
			t.Log("Creating CronTab in kcp…")

			crontab := utils.ToUnstructured(t, &testcase.mainResource)
			crontab.SetAPIVersion("kcp.example.com/v1")
			crontab.SetKind("CronTab")

			if err := kcpClient.Create(teamCtx, crontab); err != nil {
				t.Fatalf("Failed to create CronTab in kcp: %v", err)
			}

			// fake operator: create a credential Secret
			t.Logf("Creating credential Secret on the %s side…", testcase.relatedConfig.Origin)

			originClient := envtestClient
			originContext := ctx
			destClient := kcpClient
			destContext := teamCtx

			if testcase.relatedConfig.Origin == "kcp" {
				originClient, destClient = destClient, originClient
				originContext, destContext = destContext, originContext
			}

			ensureNamespace(t, originContext, originClient, testcase.sourceRelatedObject.Namespace)

			if err := originClient.Create(originContext, &testcase.sourceRelatedObject); err != nil {
				t.Fatalf("Failed to create Secret: %v", err)
			}

			// wait for the agent to do its magic
			t.Log("Wait for Secret to be synced…")
			copySecret := &corev1.Secret{}
			err := wait.PollUntilContextTimeout(destContext, 500*time.Millisecond, 30*time.Second, false, func(ctx context.Context) (done bool, err error) {
				copyKey := ctrlruntimeclient.ObjectKeyFromObject(&testcase.expectedSyncedRelatedObject)
				return destClient.Get(ctx, copyKey, copySecret) == nil, nil
			})
			if err != nil {
				t.Fatalf("Failed to wait for Secret to be synced: %v", err)
			}

			// ensure the secret in kcp does not have any sync-related metadata
			maps.DeleteFunc(copySecret.Labels, func(k, v string) bool {
				return strings.HasPrefix(k, "claimed.internal.apis.kcp.io/")
			})

			delete(copySecret.Annotations, "kcp.io/cluster")
			if len(copySecret.Annotations) == 0 {
				copySecret.Annotations = nil
			}

			orig := testcase.expectedSyncedRelatedObject
			copySecret.CreationTimestamp = orig.CreationTimestamp
			copySecret.Generation = orig.Generation
			copySecret.ResourceVersion = orig.ResourceVersion
			copySecret.ManagedFields = orig.ManagedFields
			copySecret.UID = orig.UID

			if changes := diff.ObjectDiff(orig, copySecret); changes != "" {
				t.Errorf("Synced secret does not match expected Secret:\n%s", changes)
			}
		})
	}
}

func ensureNamespace(t *testing.T, ctx context.Context, client ctrlruntimeclient.Client, name string) {
	namespace := &corev1.Namespace{}
	namespace.Name = name

	if err := client.Create(ctx, namespace); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			t.Fatalf("Failed to create namespace %s in kcp: %v", name, err)
		}
	}
}
