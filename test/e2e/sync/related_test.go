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
	"errors"
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/api-syncagent/internal/projection"
	"github.com/kcp-dev/api-syncagent/internal/test/diff"
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"
	crds "github.com/kcp-dev/api-syncagent/test/crds/example/v1"
	"github.com/kcp-dev/api-syncagent/test/utils"

	kcpapisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrlruntime "sigs.k8s.io/controller-runtime"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func TestSyncRelatedObjects(t *testing.T) {
	const apiExportName = "kcp.example.com"

	ctrlruntime.SetLogger(logr.Discard())

	testcases := []struct {
		// the name of this testcase
		name string
		// the org workspace everything should happen in
		workspace string
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
				Origin:     syncagentv1alpha1.RelatedResourceOriginService,
				Kind:       "Secret",
				Group:      "",
				Version:    "v1",
				Object: syncagentv1alpha1.RelatedResourceObject{
					RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
						Template: &syncagentv1alpha1.TemplateExpression{
							// same fixed value on both sides
							Template: "my-credentials",
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
				Origin:     syncagentv1alpha1.RelatedResourceOriginKcp,
				Kind:       "Secret",
				Group:      "",
				Version:    "v1",
				Object: syncagentv1alpha1.RelatedResourceObject{
					RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
						Template: &syncagentv1alpha1.TemplateExpression{
							// same fixed value on both sides
							Template: "my-credentials",
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

		{
			name:      "sync referenced Secret up into a new namespace",
			workspace: "sync-referenced-secret-up-namespace",
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
				Origin:     syncagentv1alpha1.RelatedResourceOriginService,
				Kind:       "Secret",
				Group:      "",
				Version:    "v1",
				Object: syncagentv1alpha1.RelatedResourceObject{
					RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
						Template: &syncagentv1alpha1.TemplateExpression{
							Template: "my-credentials",
						},
					},
					Namespace: &syncagentv1alpha1.RelatedResourceObjectSpec{
						Template: &syncagentv1alpha1.TemplateExpression{
							Template: `{{ if eq .Side "kcp" }}new-namespace{{ else }}{{ .Object.metadata.namespace }}{{ end }}`,
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
					Namespace: "new-namespace",
				},
				Data: map[string][]byte{
					"password": []byte("hunter2"),
				},
				Type: corev1.SecretTypeOpaque,
			},
		},

		//////////////////////////////////////////////////////////////////////////////////////////////

		{
			name:      "sync referenced Secret down into a new namespace",
			workspace: "sync-referenced-secret-down-namespace",
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
				Origin:     syncagentv1alpha1.RelatedResourceOriginKcp,
				Kind:       "Secret",
				Group:      "",
				Version:    "v1",
				Object: syncagentv1alpha1.RelatedResourceObject{
					RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
						Template: &syncagentv1alpha1.TemplateExpression{
							Template: "my-credentials",
						},
					},
					Namespace: &syncagentv1alpha1.RelatedResourceObjectSpec{
						Template: &syncagentv1alpha1.TemplateExpression{
							Template: `{{ if eq .Side "kcp" }}{{ .Object.metadata.namespace }}{{ else }}new-namespace{{ end }}`,
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
					Namespace: "new-namespace",
				},
				Data: map[string][]byte{
					"password": []byte("hunter2"),
				},
				Type: corev1.SecretTypeOpaque,
			},
		},

		//////////////////////////////////////////////////////////////////////////////////////////////

		{
			name:      "sync referenced Secret up from a foreign namespace",
			workspace: "sync-referenced-secret-up-foreign-namespace",
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
				Origin:     syncagentv1alpha1.RelatedResourceOriginService,
				Kind:       "Secret",
				Group:      "",
				Version:    "v1",
				Object: syncagentv1alpha1.RelatedResourceObject{
					RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
						Template: &syncagentv1alpha1.TemplateExpression{
							Template: "my-credentials",
						},
					},
					Namespace: &syncagentv1alpha1.RelatedResourceObjectSpec{
						Template: &syncagentv1alpha1.TemplateExpression{
							Template: `{{ if eq .Side "kcp" }}{{ .Object.metadata.namespace }}{{ else }}other-namespace{{ end }}`,
						},
					},
				},
			},
			sourceRelatedObject: corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-credentials",
					Namespace: "other-namespace",
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
				Origin:     syncagentv1alpha1.RelatedResourceOriginService,
				Kind:       "Secret",
				Group:      "",
				Version:    "v1",
				Object: syncagentv1alpha1.RelatedResourceObject{
					RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
						Selector: &syncagentv1alpha1.RelatedResourceObjectSelector{
							LabelSelector: metav1.LabelSelector{
								MatchLabels: map[string]string{
									"find": "me",
								},
							},
							Rewrite: syncagentv1alpha1.RelatedResourceSelectorRewrite{
								Template: &syncagentv1alpha1.TemplateExpression{
									// same fixed name on both sides
									Template: "my-credentials",
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

		//////////////////////////////////////////////////////////////////////////////////////////////

		{
			name:      "use cluster-related fields in label selector",
			workspace: "sync-cluster-fields-selected-secret-up",
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
				Origin:     syncagentv1alpha1.RelatedResourceOriginService,
				Kind:       "Secret",
				Group:      "",
				Version:    "v1",
				Object: syncagentv1alpha1.RelatedResourceObject{
					RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
						Selector: &syncagentv1alpha1.RelatedResourceObjectSelector{
							LabelSelector: metav1.LabelSelector{
								MatchLabels: map[string]string{
									"find": "foo-{{ .ClusterName | len }}",
								},
							},
							Rewrite: syncagentv1alpha1.RelatedResourceSelectorRewrite{
								Template: &syncagentv1alpha1.TemplateExpression{
									// same fixed name on both sides
									Template: "my-credentials",
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
						"find": "foo-16",
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
						"find": "foo-16",
					},
				},
				Data: map[string][]byte{
					"password": []byte("hunter2"),
				},
				Type: corev1.SecretTypeOpaque,
			},
		},

		//////////////////////////////////////////////////////////////////////////////////////////////

		{
			name:      "find Secret based on templated label selector",
			workspace: "sync-templated-selected-secret-up",
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
				Origin:     syncagentv1alpha1.RelatedResourceOriginService,
				Kind:       "Secret",
				Group:      "",
				Version:    "v1",
				Object: syncagentv1alpha1.RelatedResourceObject{
					RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
						Selector: &syncagentv1alpha1.RelatedResourceObjectSelector{
							LabelSelector: metav1.LabelSelector{
								MatchLabels: map[string]string{
									// include some nasty whitespace
									`  {{ list "fi" "nd" | join "-" }} `: `
{{ lower "ME" }}
 `,
								},
							},
							Rewrite: syncagentv1alpha1.RelatedResourceSelectorRewrite{
								Template: &syncagentv1alpha1.TemplateExpression{
									// same fixed name on both sides
									Template: "my-credentials",
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
						"fi-nd": "me",
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
						"fi-nd": "me",
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
			utils.RunAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, apiExportName, "")

			// wait until the API is available
			kcpClusterClient := utils.GetKcpAdminClusterClient(t)

			teamClusterPath := logicalcluster.NewPath("root").Join(testcase.workspace).Join("team-1")
			teamClient := kcpClusterClient.Cluster(teamClusterPath)

			utils.WaitForBoundAPI(t, ctx, teamClient, schema.GroupVersionResource{
				Group:    apiExportName,
				Version:  "v1",
				Resource: "crontabs",
			})

			// create a Crontab object in a team workspace
			t.Log("Creating CronTab in kcp…")

			crontab := utils.ToUnstructured(t, &testcase.mainResource)
			crontab.SetAPIVersion("kcp.example.com/v1")
			crontab.SetKind("CronTab")

			if err := teamClient.Create(ctx, crontab); err != nil {
				t.Fatalf("Failed to create CronTab in kcp: %v", err)
			}

			// fake operator: create a credential Secret
			t.Logf("Creating credential Secret on the %s side…", testcase.relatedConfig.Origin)

			originClient := envtestClient
			destClient := teamClient

			if testcase.relatedConfig.Origin == syncagentv1alpha1.RelatedResourceOriginKcp {
				originClient, destClient = destClient, originClient
			}

			ensureNamespace(t, ctx, originClient, testcase.sourceRelatedObject.Namespace)

			if err := originClient.Create(ctx, &testcase.sourceRelatedObject); err != nil {
				t.Fatalf("Failed to create Secret: %v", err)
			}

			// wait for the agent to do its magic
			t.Log("Wait for Secret to be synced…")
			copySecret := corev1.Secret{}
			err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, false, func(ctx context.Context) (done bool, err error) {
				copyKey := ctrlruntimeclient.ObjectKeyFromObject(&testcase.expectedSyncedRelatedObject)
				return destClient.Get(ctx, copyKey, &copySecret) == nil, nil
			})
			if err != nil {
				t.Fatalf("Failed to wait for Secret to be synced: %v", err)
			}

			if err := compareSecrets(t, copySecret, testcase.expectedSyncedRelatedObject); err != nil {
				t.Fatalf("Synced secret does not match expected Secret:\n%v", err)
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

// TestSyncRelatedMultiObjects is similar to TestSyncRelatedObjects, but here
// we test for cases where a single related resource configuration matches multiple
// Kubernetes objects.
func TestSyncRelatedMultiObjects(t *testing.T) {
	const apiExportName = "kcp.example.com"

	ctrlruntime.SetLogger(logr.Discard())

	testcases := []struct {
		// the name of this testcase
		name string
		// the org workspace everything should happen in
		workspace string
		// the configuration for the related resource
		relatedConfig syncagentv1alpha1.RelatedResourceSpec
		// the primary object created by the user in kcp
		remoteMainResource crds.Backup
		// the primary object created (and potentially mutated) by the agent on the
		// local cluster (we explicitly create it here to simulate that remote and
		// local objects are different)
		localMainResource crds.Backup
		// the original related objects (will automatically be created on either the
		// kcp or service side, depending on the relatedConfig above)
		sourceRelatedObjects []corev1.Secret
		// expectation: this is how the copies of the related objects should look
		// like after the sync has completed
		expectedSyncedRelatedObjects []corev1.Secret
	}{
		{
			name:      "reference that returns a nice, sensible array",
			workspace: "sensible-multi-reference",
			remoteMainResource: crds.Backup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-backup",
					Namespace: "default",
				},
				Spec: crds.BackupSpec{
					Items: []crds.BackupItem{
						{Name: "secret-1"},
						{Name: "secret-2"},
						{Name: "secret-3"},
					},
				},
			},
			localMainResource: crds.Backup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-backup",
					Namespace: "synced-default",
				},
				Spec: crds.BackupSpec{
					Items: []crds.BackupItem{
						{Name: "mutated-secret-1"},
						{Name: "mutated-secret-2"},
						{Name: "mutated-secret-3"},
					},
				},
			},
			relatedConfig: syncagentv1alpha1.RelatedResourceSpec{
				Identifier: "credentials",
				Origin:     syncagentv1alpha1.RelatedResourceOriginService,
				Kind:       "Secret",
				Group:      "",
				Version:    "v1",
				Object: syncagentv1alpha1.RelatedResourceObject{
					RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
						Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
							Path: "spec.items.#.name",
						},
					},
				},
			},
			sourceRelatedObjects: []corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "mutated-secret-1",
						Namespace: "synced-default",
					},
					Data: map[string][]byte{
						"password": []byte("hunter1"),
					},
					Type: corev1.SecretTypeOpaque,
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "mutated-secret-2",
						Namespace: "synced-default",
					},
					Data: map[string][]byte{
						"password": []byte("hunter2"),
					},
					Type: corev1.SecretTypeOpaque,
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "mutated-secret-3",
						Namespace: "synced-default",
					},
					Data: map[string][]byte{
						"password": []byte("hunter3"),
					},
					Type: corev1.SecretTypeOpaque,
				},
			},

			expectedSyncedRelatedObjects: []corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "secret-1",
						Namespace: "default",
					},
					Data: map[string][]byte{
						"password": []byte("hunter1"),
					},
					Type: corev1.SecretTypeOpaque,
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "secret-2",
						Namespace: "default",
					},
					Data: map[string][]byte{
						"password": []byte("hunter2"),
					},
					Type: corev1.SecretTypeOpaque,
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "secret-3",
						Namespace: "default",
					},
					Data: map[string][]byte{
						"password": []byte("hunter3"),
					},
					Type: corev1.SecretTypeOpaque,
				},
			},
		},

		{
			name:      "empty items on either side should be silently skipped",
			workspace: "empty-items-multi-references",
			remoteMainResource: crds.Backup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-backup",
					Namespace: "default",
				},
				Spec: crds.BackupSpec{
					Items: []crds.BackupItem{
						{Name: "secret-1"},
						{Name: ""},
						{Name: "secret-3"},
					},
				},
			},
			localMainResource: crds.Backup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-backup",
					Namespace: "synced-default",
				},
				Spec: crds.BackupSpec{
					Items: []crds.BackupItem{
						{Name: "mutated-secret-1"},
						{Name: "mutated-secret-2"},
						{Name: ""},
					},
				},
			},
			relatedConfig: syncagentv1alpha1.RelatedResourceSpec{
				Identifier: "credentials",
				Origin:     syncagentv1alpha1.RelatedResourceOriginService,
				Kind:       "Secret",
				Group:      "",
				Version:    "v1",
				Object: syncagentv1alpha1.RelatedResourceObject{
					RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
						Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
							Path: "spec.items.#.name",
						},
					},
				},
			},
			sourceRelatedObjects: []corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "mutated-secret-1",
						Namespace: "synced-default",
					},
					Data: map[string][]byte{
						"password": []byte("hunter1"),
					},
					Type: corev1.SecretTypeOpaque,
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "mutated-secret-2",
						Namespace: "synced-default",
					},
					Data: map[string][]byte{
						"password": []byte("hunter2"),
					},
					Type: corev1.SecretTypeOpaque,
				},
			},

			expectedSyncedRelatedObjects: []corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "secret-1",
						Namespace: "default",
					},
					Data: map[string][]byte{
						"password": []byte("hunter1"),
					},
					Type: corev1.SecretTypeOpaque,
				},
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
				"test/crds/backup.yaml",
			})

			// publish Backups
			t.Logf("Publishing CRDs…")
			prBackups := &syncagentv1alpha1.PublishedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "publish-backups",
				},
				Spec: syncagentv1alpha1.PublishedResourceSpec{
					Resource: syncagentv1alpha1.SourceResourceDescriptor{
						APIGroup: "eksempel.no",
						Version:  "v1",
						Kind:     "Backup",
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

			if err := envtestClient.Create(ctx, prBackups); err != nil {
				t.Fatalf("Failed to create PublishedResource: %v", err)
			}

			// pre-create the synced Backup because it's easier to let the agent deal with updating,
			// rather than us here having to implement to update/patch logic.
			t.Log("Creating synced Backup copy locally…")

			ensureNamespace(t, ctx, envtestClient, testcase.localMainResource.Namespace)

			localBackup := utils.ToUnstructured(t, &testcase.localMainResource)
			localBackup.SetAPIVersion("eksempel.no/v1")
			localBackup.SetKind("Backup")

			if err := envtestClient.Create(ctx, localBackup); err != nil {
				t.Fatalf("Failed to create local Backup: %v", err)
			}

			// fake operator: create credential Secrets
			kcpClusterClient := utils.GetKcpAdminClusterClient(t)

			teamClusterPath := logicalcluster.NewPath("root").Join(testcase.workspace).Join("team-1")
			teamClient := kcpClusterClient.Cluster(teamClusterPath)

			originClient := envtestClient
			destClient := teamClient

			if testcase.relatedConfig.Origin == syncagentv1alpha1.RelatedResourceOriginKcp {
				originClient, destClient = destClient, originClient
			}

			for _, relatedObject := range testcase.sourceRelatedObjects {
				t.Logf("Creating credential Secret on the %s side…", testcase.relatedConfig.Origin)

				ensureNamespace(t, ctx, originClient, relatedObject.Namespace)

				if err := originClient.Create(ctx, &relatedObject); err != nil {
					t.Fatalf("Failed to create Secret %s: %v", relatedObject.Name, err)
				}
			}

			// start the agent in the background to update the APIExport with the Backups API
			utils.RunAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, apiExportName, "")

			// wait until the API is available
			utils.WaitForBoundAPI(t, ctx, teamClient, schema.GroupVersionResource{
				Group:    apiExportName,
				Version:  "v1",
				Resource: "backups",
			})

			// create a Backup object in a team workspace
			t.Log("Creating Backup in kcp…")

			remoteBackup := utils.ToUnstructured(t, &testcase.remoteMainResource)
			remoteBackup.SetAPIVersion("kcp.example.com/v1")
			remoteBackup.SetKind("Backup")

			if err := teamClient.Create(ctx, remoteBackup); err != nil {
				t.Fatalf("Failed to create Backup in kcp: %v", err)
			}

			// wait for the agent to do its magic
			t.Log("Wait for Secrets to be synced…")
			checkSecret := func(ctx context.Context, expected corev1.Secret) error {
				copySecret := &corev1.Secret{}
				if err := destClient.Get(ctx, ctrlruntimeclient.ObjectKeyFromObject(&expected), copySecret); err != nil {
					return fmt.Errorf("failed to get copy of Secret %v: %w", ctrlruntimeclient.ObjectKeyFromObject(&expected), err)
				}

				if err := compareSecrets(t, *copySecret, expected); err != nil {
					return fmt.Errorf("synced secret does not match expected Secret:\n%w", err)
				}

				return nil
			}

			err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 30*time.Second, false, func(ctx context.Context) (done bool, err error) {
				var errs []string

				for _, expectedObj := range testcase.expectedSyncedRelatedObjects {
					if err := checkSecret(ctx, expectedObj); err != nil {
						errs = append(errs, fmt.Sprintf("invalid Secret %s: %v", expectedObj.Name, err))
					}
				}

				if len(errs) > 0 {
					t.Logf("Sync has not completed yet:\n%s", strings.Join(errs, "\n"))
					return false, nil
				}

				return true, nil
			})
			if err != nil {
				t.Fatalf("Failed to wait for Secrets to be synced: %v", err)
			}
		})
	}
}

func TestSyncNonStandardRelatedResources(t *testing.T) {
	const apiExportName = "kcp.example.com"

	ctrlruntime.SetLogger(logr.Discard())

	testcases := []struct {
		// the name of this testcase
		name string
		// the org workspace everything should happen in
		workspace string
		// the configuration for the related resource
		relatedConfig syncagentv1alpha1.RelatedResourceSpec
		// the primary object created by the user in kcp
		mainResource crds.Crontab
		// the original related object (will automatically be created on either the
		// kcp or service side, depending on the relatedConfig above)
		sourceRelatedObject ctrlruntimeclient.Object
		// expectation: this is how the copy of the related object should look
		// like after the sync has completed
		expectedSyncedRelatedObject ctrlruntimeclient.Object
	}{
		{
			name:      "turn a related ConfigMap into a Secret",
			workspace: "sync-related-configmap-to-secret",
			mainResource: crds.Crontab{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kcp.example.com/v1",
					Kind:       "CronTab",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-crontab",
					Namespace: "default",
				},
				Spec: crds.CrontabSpec{},
			},
			relatedConfig: syncagentv1alpha1.RelatedResourceSpec{
				Identifier: "credentials",
				Origin:     syncagentv1alpha1.RelatedResourceOriginService,
				Resource:   "configmaps",
				Group:      "",
				Version:    "v1",
				Projection: &syncagentv1alpha1.RelatedResourceProjection{
					Resource: "secrets",
				},
				Object: syncagentv1alpha1.RelatedResourceObject{
					RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
						Template: &syncagentv1alpha1.TemplateExpression{
							// same fixed value on both sides
							Template: "my-credentials",
						},
					},
				},
			},
			sourceRelatedObject: &corev1.ConfigMap{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "ConfigMap",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-credentials",
					Namespace: "synced-default",
				},
				Data: map[string]string{
					"password": "aHVudGVyMg==",
				},
			},
			expectedSyncedRelatedObject: &corev1.Secret{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "Secret",
				},
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
			name:      "use a resource from the same APIExport as a related resource",
			workspace: "sync-related-same-apiexport",
			mainResource: crds.Crontab{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kcp.example.com/v1",
					Kind:       "CronTab",
				},
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
				Identifier: "the-backup",
				Origin:     syncagentv1alpha1.RelatedResourceOriginService,
				Resource:   "backups",
				Group:      "eksempel.no",
				Version:    "v1",
				Projection: &syncagentv1alpha1.RelatedResourceProjection{
					Group: "kcp.example.com",
				},
				Object: syncagentv1alpha1.RelatedResourceObject{
					RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
						Template: &syncagentv1alpha1.TemplateExpression{
							// same fixed value on both sides
							Template: "my-backup",
						},
					},
				},
			},
			sourceRelatedObject: &crds.Backup{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "eksempel.no/v1",
					Kind:       "Backup",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-backup",
					Namespace: "synced-default",
				},
				Spec: crds.BackupSpec{},
			},
			expectedSyncedRelatedObject: &crds.Backup{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kcp.example.com/v1",
					Kind:       "Backup",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-backup",
					Namespace: "default",
				},
				Spec: crds.BackupSpec{},
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
				"test/crds/backup.yaml",
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

			// backups are published to be used as related resources only
			prBackups := &syncagentv1alpha1.PublishedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "publish-backups",
				},
				Spec: syncagentv1alpha1.PublishedResourceSpec{
					Resource: syncagentv1alpha1.SourceResourceDescriptor{
						APIGroup: "eksempel.no",
						Version:  "v1",
						Kind:     "Backup",
					},
					Projection: &syncagentv1alpha1.ResourceProjection{
						Group: "kcp.example.com",
					},
					Synchronization: &syncagentv1alpha1.SynchronizationSpec{
						Enabled: false,
					},
				},
			}

			if err := envtestClient.Create(ctx, prBackups); err != nil {
				t.Fatalf("Failed to create PublishedResource: %v", err)
			}

			// t.Logf("waiting...")
			// time.Sleep(20 * time.Second)

			// start the agent in the background to update the APIExport with the CronTabs API
			utils.RunAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, apiExportName, "")

			// wait until the API is available
			kcpClusterClient := utils.GetKcpAdminClusterClient(t)

			teamClusterPath := logicalcluster.NewPath("root").Join(testcase.workspace).Join("team-1")
			teamClient := kcpClusterClient.Cluster(teamClusterPath)

			utils.WaitForBoundAPI(t, ctx, teamClient, schema.GroupVersionResource{
				Group:    apiExportName,
				Version:  "v1",
				Resource: "crontabs",
			})

			// create a Crontab object in a team workspace
			t.Log("Creating CronTab in kcp…")

			crontab := utils.ToUnstructured(t, &testcase.mainResource)
			if err := teamClient.Create(ctx, crontab); err != nil {
				t.Fatalf("Failed to create CronTab in kcp: %v", err)
			}

			// fake operator: create a related object as a result of processing the main object
			t.Logf("Creating original related object on the %s side…", testcase.relatedConfig.Origin)

			originClient := envtestClient
			destClient := teamClient

			if testcase.relatedConfig.Origin == syncagentv1alpha1.RelatedResourceOriginKcp {
				originClient, destClient = destClient, originClient
			}

			ensureNamespace(t, ctx, originClient, testcase.sourceRelatedObject.GetNamespace())

			relatedObject := utils.ToUnstructured(t, &testcase.sourceRelatedObject)
			if err := originClient.Create(ctx, relatedObject); err != nil {
				t.Fatalf("Failed to create related object: %v", err)
			}

			// wait for the agent to do its magic
			t.Log("Wait for related object to be synced…")
			projectedGVR := projection.RelatedResourceProjectedGVR(&testcase.relatedConfig)
			projectedGVK, err := destClient.RESTMapper().KindFor(projectedGVR)
			if err != nil {
				t.Fatalf("Failed to resolve projected GVR %v: %v", projectedGVR, err)
			}

			copiedRelatedObject := &unstructured.Unstructured{}
			copiedRelatedObject.SetAPIVersion(projectedGVK.GroupVersion().String())
			copiedRelatedObject.SetKind(projectedGVK.Kind)

			err = wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, false, func(ctx context.Context) (done bool, err error) {
				copyKey := ctrlruntimeclient.ObjectKeyFromObject(testcase.expectedSyncedRelatedObject)
				return destClient.Get(ctx, copyKey, copiedRelatedObject) == nil, nil
			})
			if err != nil {
				t.Fatalf("Failed to wait for related object to be synced: %v", err)
			}

			if err := compareUnstructured(copiedRelatedObject, toUnstructured(t, testcase.expectedSyncedRelatedObject)); err != nil {
				t.Fatalf("Synced copy does not match expected object:\n%v", err)
			}
		})
	}
}

func TestSyncNonStandardRelatedResourcesMultipleAPIExports(t *testing.T) {
	const (
		initechAPIExportName  = "initech.example.com"
		initroidAPIExportName = "initroid.example.com"
	)

	ctrlruntime.SetLogger(logr.Discard())

	testcases := []struct {
		// the name of this testcase
		name string
		// the org workspacePrefix prefix everything should happen in; this test will create
		// two organizations with one APIExport each
		workspacePrefix string
		// the configuration for the related resource
		relatedConfig syncagentv1alpha1.RelatedResourceSpec
		// the primary object created by the user in kcp
		mainResource crds.Crontab
		// the original related object (will automatically be created on either the
		// kcp or service side, depending on the relatedConfig above)
		sourceRelatedObject ctrlruntimeclient.Object
		// expectation: this is how the copy of the related object should look
		// like after the sync has completed
		expectedSyncedRelatedObject ctrlruntimeclient.Object
	}{
		{
			name:            "use a resource from another APIExport as a related resource",
			workspacePrefix: "sync-related-foreign-apiexport",
			mainResource: crds.Crontab{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "initech.example.com/v1",
					Kind:       "CronTab",
				},
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
				Identifier: "the-backup",
				Origin:     syncagentv1alpha1.RelatedResourceOriginService,
				Resource:   "backups",
				Group:      "eksempel.no",
				Version:    "v1",
				Projection: &syncagentv1alpha1.RelatedResourceProjection{
					Group: initroidAPIExportName,
				},
				Object: syncagentv1alpha1.RelatedResourceObject{
					RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
						Template: &syncagentv1alpha1.TemplateExpression{
							// same fixed value on both sides
							Template: "my-backup",
						},
					},
				},
			},
			sourceRelatedObject: &crds.Backup{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "eksempel.no/v1",
					Kind:       "Backup",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-backup",
					Namespace: "synced-default",
				},
				Spec: crds.BackupSpec{},
			},
			expectedSyncedRelatedObject: &crds.Backup{
				TypeMeta: metav1.TypeMeta{
					APIVersion: initroidAPIExportName + "/v1",
					Kind:       "Backup",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-backup",
					Namespace: "default",
				},
				Spec: crds.BackupSpec{},
			},
		},
	}

	for _, testcase := range testcases {
		t.Run(testcase.name, func(t *testing.T) {
			ctx := t.Context()

			// setup a test environment in kcp
			initechOrgWorkspace := testcase.workspacePrefix + "-initech"
			initroidOrgWorkspace := testcase.workspacePrefix + "-initroid"

			initechOrgKubconfig := utils.CreateOrganization(t, ctx, initechOrgWorkspace, initechAPIExportName)
			initroidOrgKubconfig := utils.CreateOrganization(t, ctx, initroidOrgWorkspace, initroidAPIExportName)

			// In this testcase, since we are referring to another APIExport, we need to get its
			// identity hash first and update the relatedConfig.
			kcpClusterClient := utils.GetKcpAdminClusterClient(t)
			initroidClient := kcpClusterClient.Cluster(logicalcluster.NewPath("root").Join(initroidOrgWorkspace))

			initroidAPIExport := &kcpapisv1alpha1.APIExport{}
			err := initroidClient.Get(ctx, types.NamespacedName{Name: initroidAPIExportName}, initroidAPIExport)
			if err != nil {
				t.Fatalf("Failed to find Initroid APIExport: %v", err)
			}

			// In this testcase, initech wants to consume initroid APIs, so we must bind to their APIExport, too.
			initechOrg := logicalcluster.NewPath("root").Join(initechOrgWorkspace)

			for _, team := range []string{"team-1", "team-2"} {
				utils.BindToAPIExport(t, ctx, kcpClusterClient.Cluster(initechOrg.Join(team)), initroidAPIExport)
			}

			// start a service cluster
			envtestKubeconfig, envtestClient, _ := utils.RunEnvtest(t, []string{
				"test/crds/crontab.yaml",
				"test/crds/backup.yaml",
			})

			t.Logf("Detected identity hash %q", initroidAPIExport.Status.IdentityHash)
			testcase.relatedConfig.IdentityHash = initroidAPIExport.Status.IdentityHash

			// publish Crontabs and Backups
			t.Logf("Publishing CRDs…")
			prCrontabs := &syncagentv1alpha1.PublishedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "publish-crontabs",
					Labels: map[string]string{
						"agent": "initech",
					},
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
						Group: initechAPIExportName,
					},
					Related: []syncagentv1alpha1.RelatedResourceSpec{testcase.relatedConfig},
				},
			}

			if err := envtestClient.Create(ctx, prCrontabs); err != nil {
				t.Fatalf("Failed to create PublishedResource: %v", err)
			}

			// backups are published to be used as related resources only
			prBackups := &syncagentv1alpha1.PublishedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "publish-backups",
					Labels: map[string]string{
						"agent": "initroid",
					},
				},
				Spec: syncagentv1alpha1.PublishedResourceSpec{
					Resource: syncagentv1alpha1.SourceResourceDescriptor{
						APIGroup: "eksempel.no",
						Version:  "v1",
						Kind:     "Backup",
					},
					Projection: &syncagentv1alpha1.ResourceProjection{
						Group: initroidAPIExportName,
					},
					Synchronization: &syncagentv1alpha1.SynchronizationSpec{
						Enabled: false,
					},
				},
			}

			if err := envtestClient.Create(ctx, prBackups); err != nil {
				t.Fatalf("Failed to create PublishedResource: %v", err)
			}

			// start the agents in the background to update the APIExports
			utils.RunAgent(ctx, t, "initech", initechOrgKubconfig, envtestKubeconfig, initechAPIExportName, "agent=initech")
			utils.RunAgent(ctx, t, "initroid", initroidOrgKubconfig, envtestKubeconfig, initroidAPIExportName, "agent=initroid")

			// wait until the APIs are available
			for orgWs, gvr := range map[string]schema.GroupVersionResource{
				initechOrgWorkspace:  {Group: initechAPIExportName, Version: "v1", Resource: "crontabs"},
				initroidOrgWorkspace: {Group: initroidAPIExportName, Version: "v1", Resource: "backups"},
			} {
				teamClusterPath := logicalcluster.NewPath("root").Join(orgWs).Join("team-1")
				teamClient := kcpClusterClient.Cluster(teamClusterPath)

				utils.WaitForBoundAPI(t, ctx, teamClient, gvr)
			}

			// Since we are claiming resources from other APIExports, the default accepted claims
			// the test utils provisioned are not enough anymore and we need to accept the new, actual
			// list of claims. This has to happen after the agent had time to configure the expected
			// claims on the APIExport.
			initechClient := kcpClusterClient.Cluster(logicalcluster.NewPath("root").Join(initechOrgWorkspace))

			initechAPIExport := &kcpapisv1alpha1.APIExport{}
			err = initechClient.Get(ctx, types.NamespacedName{Name: initechAPIExportName}, initechAPIExport)
			if err != nil {
				t.Fatalf("Failed to find Initech APIExport: %v", err)
			}

			for _, team := range []string{"team-1", "team-2"} {
				utils.AcceptAllPermissionClaims(t, ctx, kcpClusterClient.Cluster(initechOrg.Join(team)), initechAPIExport)
			}

			// create a Crontab object in a team workspace
			t.Log("Creating CronTab in kcp…")

			initechTeamClusterPath := logicalcluster.NewPath("root").Join(initechOrgWorkspace).Join("team-1")
			initechTeamClient := kcpClusterClient.Cluster(initechTeamClusterPath)

			crontab := utils.ToUnstructured(t, &testcase.mainResource)
			if err := initechTeamClient.Create(ctx, crontab); err != nil {
				t.Fatalf("Failed to create CronTab in kcp: %v", err)
			}

			// fake operator: create a related object as a result of processing the main object
			t.Logf("Creating original related object on the %s side…", testcase.relatedConfig.Origin)

			originClient := envtestClient
			destClient := initechTeamClient

			if testcase.relatedConfig.Origin == syncagentv1alpha1.RelatedResourceOriginKcp {
				originClient, destClient = destClient, originClient
			}

			ensureNamespace(t, ctx, originClient, testcase.sourceRelatedObject.GetNamespace())

			relatedObject := utils.ToUnstructured(t, &testcase.sourceRelatedObject)
			if err := originClient.Create(ctx, relatedObject); err != nil {
				t.Fatalf("Failed to create related object: %v", err)
			}

			// wait for the agents to do their magic
			t.Log("Wait for related object to be synced…")
			projectedGVR := projection.RelatedResourceProjectedGVR(&testcase.relatedConfig)
			projectedGVK, err := destClient.RESTMapper().KindFor(projectedGVR)
			if err != nil {
				t.Fatalf("Failed to resolve projected GVR %v: %v", projectedGVR, err)
			}

			copiedRelatedObject := &unstructured.Unstructured{}
			copiedRelatedObject.SetAPIVersion(projectedGVK.GroupVersion().String())
			copiedRelatedObject.SetKind(projectedGVK.Kind)

			err = wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, false, func(ctx context.Context) (done bool, err error) {
				copyKey := ctrlruntimeclient.ObjectKeyFromObject(testcase.expectedSyncedRelatedObject)
				return destClient.Get(ctx, copyKey, copiedRelatedObject) == nil, nil
			})
			if err != nil {
				t.Fatalf("Failed to wait for related object to be synced: %v", err)
			}

			if err := compareUnstructured(copiedRelatedObject, toUnstructured(t, testcase.expectedSyncedRelatedObject)); err != nil {
				t.Fatalf("Synced copy does not match expected object:\n%v", err)
			}
		})
	}
}

func toUnstructured(t *testing.T, obj ctrlruntimeclient.Object) *unstructured.Unstructured {
	data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		t.Fatalf("Failed to encode object as unstructured: %v", err)
	}

	unstructuredObj := &unstructured.Unstructured{Object: data}
	unstructuredObj.SetGroupVersionKind(obj.GetObjectKind().GroupVersionKind())

	return unstructuredObj
}

func compareSecrets(t *testing.T, actual, expected corev1.Secret) error {
	return compareUnstructured(toUnstructured(t, &actual), toUnstructured(t, &expected))
}

func compareUnstructured(actual, expected *unstructured.Unstructured) error {
	// ensure the secret in kcp does not have any sync-related metadata
	labels := actual.GetLabels()
	maps.DeleteFunc(labels, func(k, v string) bool {
		return strings.HasPrefix(k, "claimed.internal.apis.kcp.io/")
	})
	if len(labels) == 0 {
		labels = nil
	}
	actual.SetLabels(labels)

	annotations := actual.GetAnnotations()
	delete(annotations, "kcp.io/cluster")
	if len(annotations) == 0 {
		annotations = nil
	}
	actual.SetAnnotations(annotations)

	// doing a.SetCreation(b.GetCreation()) doesn't work due to nil values in metav1.Time...
	actual.SetCreationTimestamp(metav1.Time{})
	expected.SetCreationTimestamp(metav1.Time{})

	actual.SetGeneration(expected.GetGeneration())
	actual.SetResourceVersion(expected.GetResourceVersion())
	actual.SetManagedFields(expected.GetManagedFields())
	actual.SetUID(expected.GetUID())

	if changes := diff.ObjectDiff(expected, actual); changes != "" {
		return errors.New(changes)
	}

	return nil
}
