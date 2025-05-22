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

package apiexport

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"

	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"
	"github.com/kcp-dev/api-syncagent/test/utils"

	kcpapisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrlruntime "sigs.k8s.io/controller-runtime"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func TestPermissionsClaims(t *testing.T) {
	const (
		apiExportName = "kcp.example.com"
	)

	ctx := context.Background()
	ctrlruntime.SetLogger(logr.Discard())

	// setup a test environment in kcp
	orgKubconfig := utils.CreateOrganization(t, ctx, "apiexport-no-pclaims-by-default", apiExportName)

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
		},
	}

	if err := envtestClient.Create(ctx, prCrontabs); err != nil {
		t.Fatalf("Failed to create PublishedResource: %v", err)
	}

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
		},
	}

	if err := envtestClient.Create(ctx, prBackups); err != nil {
		t.Fatalf("Failed to create PublishedResource: %v", err)
	}

	// let the agent do its thing
	utils.RunAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, apiExportName)

	// wait for the APIExport to be updated
	t.Logf("Waiting for APIExport to be updated…")
	orgClient := utils.GetClient(t, orgKubconfig)
	apiExportKey := types.NamespacedName{Name: apiExportName}

	apiExport := &kcpapisv1alpha1.APIExport{}
	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 1*time.Minute, false, func(ctx context.Context) (done bool, err error) {
		err = orgClient.Get(ctx, apiExportKey, apiExport)
		if err != nil {
			return false, err
		}

		return len(apiExport.Spec.LatestResourceSchemas) == 2, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for APIExport to be updated: %v", err)
	}

	if claims := apiExport.Spec.PermissionClaims; len(claims) > 0 {
		t.Fatalf("APIExport should have no permissions claims, but has %v", claims)
	}

	// let's configure some related resources

	// refresh the objects
	if err := envtestClient.Get(ctx, ctrlruntimeclient.ObjectKeyFromObject(prCrontabs), prCrontabs); err != nil {
		t.Fatalf("Failed to get PublishedResource: %v", err)
	}

	if err := envtestClient.Get(ctx, ctrlruntimeclient.ObjectKeyFromObject(prBackups), prBackups); err != nil {
		t.Fatalf("Failed to get PublishedResource: %v", err)
	}

	t.Logf("Configuring related resources…")
	prBackups.Spec.Related = []syncagentv1alpha1.RelatedResourceSpec{
		{
			Identifier: "super-secret",
			Origin:     "kcp",
			Kind:       "Secret",
			Object: syncagentv1alpha1.RelatedResourceObject{
				RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
					Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
						Path: "spec.test.name",
					},
				},
				Namespace: &syncagentv1alpha1.RelatedResourceObjectSpec{
					Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
						Path: "spec.test.namespace",
					},
				},
			},
		},
		{
			Identifier: "other-super-secret",
			Origin:     "service",
			Kind:       "Secret",
			Object: syncagentv1alpha1.RelatedResourceObject{
				RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
					Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
						Path: "spec.otherTest.name",
					},
				},
				Namespace: &syncagentv1alpha1.RelatedResourceObjectSpec{
					Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
						Path: "spec.otherTest.namespace",
					},
				},
			},
		},
	}

	prCrontabs.Spec.Related = []syncagentv1alpha1.RelatedResourceSpec{
		{
			Identifier: "config",
			Origin:     "kcp",
			Kind:       "ConfigMap",
			Object: syncagentv1alpha1.RelatedResourceObject{
				RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
					Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
						Path: "spec.secretTest.name",
					},
				},
				Namespace: &syncagentv1alpha1.RelatedResourceObjectSpec{
					Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
						Path: "spec.secretTest.namespace",
					},
				},
			},
		},
	}

	if err := envtestClient.Update(ctx, prCrontabs); err != nil {
		t.Fatalf("Failed to update PublishedResource: %v", err)
	}

	if err := envtestClient.Update(ctx, prBackups); err != nil {
		t.Fatalf("Failed to update PublishedResource: %v", err)
	}

	// wait for the permission claims to be updated; note that since we have related resources at all,
	// the agent will also claim namespaces (since both ConfigMaps and Secrets are always namespaced).

	t.Logf("Wait for the claims to be updated…")
	apiExport = &kcpapisv1alpha1.APIExport{}
	err = wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 1*time.Minute, false, func(ctx context.Context) (done bool, err error) {
		err = orgClient.Get(ctx, apiExportKey, apiExport)
		if err != nil {
			return false, err
		}

		return len(apiExport.Spec.PermissionClaims) == 3, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for APIExport to be updated: %v", err)
	}

	expectedClaims := []kcpapisv1alpha1.PermissionClaim{
		{
			GroupResource: kcpapisv1alpha1.GroupResource{
				Group:    "",
				Resource: "configmaps",
			},
			All: true,
		},
		{
			GroupResource: kcpapisv1alpha1.GroupResource{
				Group:    "",
				Resource: "namespaces",
			},
			All: true,
		},
		{
			GroupResource: kcpapisv1alpha1.GroupResource{
				Group:    "",
				Resource: "secrets",
			},
			All: true,
		},
	}

	// Do not use cmp.Equal() because the Equal() func on PermissionClaims does not check all fields.
	if !equality.Semantic.DeepEqual(expectedClaims, apiExport.Spec.PermissionClaims) {
		t.Fatalf("Expected permission claims %+v, but got %+v.", expectedClaims, apiExport.Spec.PermissionClaims)
	}
}

func TestExistingPermissionsClaimsAreKept(t *testing.T) {
	const (
		apiExportName = "kcp.example.com"
	)

	ctx := context.Background()
	ctrlruntime.SetLogger(logr.Discard())

	// setup a test environment in kcp
	orgKubconfig := utils.CreateOrganization(t, ctx, "apiexport-pclaims-are-kept", apiExportName)

	// start a service cluster
	envtestKubeconfig, envtestClient, _ := utils.RunEnvtest(t, []string{
		"test/crds/crontab.yaml",
	})

	// set a random claim that is supposed to survive
	orgClient := utils.GetClient(t, orgKubconfig)
	apiExportKey := types.NamespacedName{Name: apiExportName}

	apiExport := &kcpapisv1alpha1.APIExport{}
	if err := orgClient.Get(ctx, apiExportKey, apiExport); err != nil {
		t.Fatalf("Failed to get APIExport: %v", err)
	}

	apiExport.Spec.PermissionClaims = []kcpapisv1alpha1.PermissionClaim{
		{
			GroupResource: kcpapisv1alpha1.GroupResource{
				Group:    "",
				Resource: "configmaps",
			},
			All: true,
		},
	}

	if err := orgClient.Update(ctx, apiExport); err != nil {
		t.Fatalf("Failed to update APIExport: %v", err)
	}

	// publish Crontabs
	t.Logf("Publishing CRD…")
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
			Related: []syncagentv1alpha1.RelatedResourceSpec{
				{
					Identifier: "super-secret",
					Origin:     "kcp",
					Kind:       "Secret",
					Object: syncagentv1alpha1.RelatedResourceObject{
						RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
							Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
								Path: "spec.test.name",
							},
						},
						Namespace: &syncagentv1alpha1.RelatedResourceObjectSpec{
							Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
								Path: "spec.test.namespace",
							},
						},
					},
				},
			},
		},
	}

	if err := envtestClient.Create(ctx, prCrontabs); err != nil {
		t.Fatalf("Failed to create PublishedResource: %v", err)
	}

	// let the agent do its thing
	utils.RunAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, apiExportName)

	// wait for the APIExport to be updated
	expectedClaims := []kcpapisv1alpha1.PermissionClaim{
		{
			GroupResource: kcpapisv1alpha1.GroupResource{
				Group:    "",
				Resource: "configmaps",
			},
			All: true,
		},
		{
			GroupResource: kcpapisv1alpha1.GroupResource{
				Group:    "",
				Resource: "namespaces",
			},
			All: true,
		},
		{
			GroupResource: kcpapisv1alpha1.GroupResource{
				Group:    "",
				Resource: "secrets",
			},
			All: true,
		},
	}

	t.Logf("Waiting for APIExport to be updated…")
	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 1*time.Minute, false, func(ctx context.Context) (done bool, err error) {
		err = orgClient.Get(ctx, apiExportKey, apiExport)
		if err != nil {
			return false, err
		}

		// Do not use cmp.Equal() because the Equal() func on PermissionClaims does not check all fields.
		return equality.Semantic.DeepEqual(expectedClaims, apiExport.Spec.PermissionClaims), nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for APIExport to be updated: %v", err)
	}
}

func TestSchemasAreMerged(t *testing.T) {
	const (
		apiExportName = "kcp.example.com"
	)

	ctx := context.Background()
	ctrlruntime.SetLogger(logr.Discard())

	// setup a test environment in kcp
	orgKubconfig := utils.CreateOrganization(t, ctx, "apiexport-schemas-are-updated", apiExportName)

	// start a service cluster
	envtestKubeconfig, envtestClient, _ := utils.RunEnvtest(t, []string{
		"test/crds/crontab.yaml",
	})

	// set a random resource schema that is supposed to survive
	orgClient := utils.GetClient(t, orgKubconfig)
	apiExportKey := types.NamespacedName{Name: apiExportName}

	apiExport := &kcpapisv1alpha1.APIExport{}
	if err := orgClient.Get(ctx, apiExportKey, apiExport); err != nil {
		t.Fatalf("Failed to get APIExport: %v", err)
	}

	foreignSchemaName := "v1.fakes.example.com"
	managedSchemaSuffix := ".crontabs.example.com"
	oldManagedSchemaName := "v0" + managedSchemaSuffix
	apiExport.Spec.LatestResourceSchemas = []string{
		foreignSchemaName,    // this is supposed to survive unchanged
		oldManagedSchemaName, // this is supposed to be updated
	}

	if err := orgClient.Update(ctx, apiExport); err != nil {
		t.Fatalf("Failed to update APIExport: %v", err)
	}

	// publish Crontabs
	t.Logf("Publishing CRD…")
	prCrontabs := &syncagentv1alpha1.PublishedResource{
		ObjectMeta: metav1.ObjectMeta{
			Name: "publish-crontabs",
		},
		Spec: syncagentv1alpha1.PublishedResourceSpec{
			Resource: syncagentv1alpha1.SourceResourceDescriptor{
				APIGroup: "example.com",
				Versions: []string{"v1"},
				Kind:     "CronTab",
			},
			Related: []syncagentv1alpha1.RelatedResourceSpec{
				{
					Identifier: "super-secret",
					Origin:     "kcp",
					Kind:       "Secret",
					Object: syncagentv1alpha1.RelatedResourceObject{
						RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
							Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
								Path: "spec.test.name",
							},
						},
						Namespace: &syncagentv1alpha1.RelatedResourceObjectSpec{
							Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
								Path: "spec.test.namespace",
							},
						},
					},
				},
			},
		},
	}

	if err := envtestClient.Create(ctx, prCrontabs); err != nil {
		t.Fatalf("Failed to create PublishedResource: %v", err)
	}

	// let the agent do its thing
	utils.RunAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, apiExportName)

	// wait for the APIExport to be updated
	t.Logf("Waiting for APIExport to be updated…")
	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 1*time.Minute, false, func(ctx context.Context) (done bool, err error) {
		err = orgClient.Get(ctx, apiExportKey, apiExport)
		if err != nil {
			return false, err
		}

		for _, schema := range apiExport.Spec.LatestResourceSchemas {
			if strings.HasSuffix(schema, managedSchemaSuffix) && schema != oldManagedSchemaName {
				return true, nil
			}
		}

		return false, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for APIExport to be updated: %v", err)
	}

	// check if the foreign schema is still present
	if !slices.Contains(apiExport.Spec.LatestResourceSchemas, foreignSchemaName) {
		t.Fatalf("Expected APIExport to still contain %s, but is %v instead.", foreignSchemaName, apiExport.Spec.LatestResourceSchemas)
	}

	// sanity check
	if len(apiExport.Spec.LatestResourceSchemas) != 2 {
		t.Fatalf("Expected 2 schemas, but APIExport has %v instead.", apiExport.Spec.LatestResourceSchemas)
	}
}

func TestSchemaIsKeptWhenDeletingPublishedResource(t *testing.T) {
	const (
		apiExportName = "kcp.example.com"
	)

	ctx := context.Background()
	ctrlruntime.SetLogger(logr.Discard())

	// setup a test environment in kcp
	orgKubconfig := utils.CreateOrganization(t, ctx, "apiexport-schema-is-kept", apiExportName)

	// start a service cluster
	envtestKubeconfig, envtestClient, _ := utils.RunEnvtest(t, []string{
		"test/crds/backup.yaml",
		"test/crds/crontab.yaml",
	})

	// publish Crontabs
	t.Logf("Publishing Crontab CRD…")
	crontabsSchemaSuffix := ".crontabs.example.com"
	prCrontabs := &syncagentv1alpha1.PublishedResource{
		ObjectMeta: metav1.ObjectMeta{
			Name: "publish-crontabs",
		},
		Spec: syncagentv1alpha1.PublishedResourceSpec{
			Resource: syncagentv1alpha1.SourceResourceDescriptor{
				APIGroup: "example.com",
				Versions: []string{"v1"},
				Kind:     "CronTab",
			},
			Related: []syncagentv1alpha1.RelatedResourceSpec{
				{
					Identifier: "super-secret",
					Origin:     "kcp",
					Kind:       "Secret",
					Object: syncagentv1alpha1.RelatedResourceObject{
						RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
							Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
								Path: "spec.test.name",
							},
						},
						Namespace: &syncagentv1alpha1.RelatedResourceObjectSpec{
							Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
								Path: "spec.test.namespace",
							},
						},
					},
				},
			},
		},
	}

	if err := envtestClient.Create(ctx, prCrontabs); err != nil {
		t.Fatalf("Failed to create PublishedResource: %v", err)
	}

	// let the agent do its thing
	utils.RunAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, apiExportName)

	// wait for the APIExport to be contain the new ARS
	t.Logf("Waiting for APIExport to be updated…")

	orgClient := utils.GetClient(t, orgKubconfig)
	apiExport := &kcpapisv1alpha1.APIExport{}
	apiExportKey := types.NamespacedName{Name: apiExportName}

	var crontabsSchemaName string
	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 1*time.Minute, false, func(ctx context.Context) (done bool, err error) {
		err = orgClient.Get(ctx, apiExportKey, apiExport)
		if err != nil {
			return false, err
		}

		for _, schema := range apiExport.Spec.LatestResourceSchemas {
			if strings.HasSuffix(schema, crontabsSchemaSuffix) {
				crontabsSchemaName = schema
				return true, nil
			}
		}

		return false, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for APIExport to be updated: %v", err)
	}

	// Now delete the PublishedResource
	t.Logf("Unpublishing Crontab CRD…")
	if err := envtestClient.Delete(ctx, prCrontabs); err != nil {
		t.Fatalf("Failed to delete PublishedResource: %v", err)
	}

	// force a reconcile by creating another, different PR
	t.Logf("Publishing Backup CRD…")
	backupsSchemaSuffix := ".backups.eksempel.no"
	prBackups := &syncagentv1alpha1.PublishedResource{
		ObjectMeta: metav1.ObjectMeta{
			Name: "publish-backups",
		},
		Spec: syncagentv1alpha1.PublishedResourceSpec{
			Resource: syncagentv1alpha1.SourceResourceDescriptor{
				APIGroup: "eksempel.no",
				Versions: []string{"v1"},
				Kind:     "Backup",
			},
		},
	}

	if err := envtestClient.Create(ctx, prBackups); err != nil {
		t.Fatalf("Failed to create PublishedResource: %v", err)
	}

	// wait for the APIExport to be contain the new ARS
	t.Logf("Waiting for APIExport to be updated…")
	err = wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 1*time.Minute, false, func(ctx context.Context) (done bool, err error) {
		err = orgClient.Get(ctx, apiExportKey, apiExport)
		if err != nil {
			return false, err
		}

		for _, schema := range apiExport.Spec.LatestResourceSchemas {
			if strings.HasSuffix(schema, backupsSchemaSuffix) {
				return true, nil
			}
		}

		return false, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for APIExport to be updated: %v", err)
	}

	// check that crontabs are still present
	if !slices.Contains(apiExport.Spec.LatestResourceSchemas, crontabsSchemaName) {
		t.Fatalf("Expected APIExport to still contain %s, but is %v instead.", crontabsSchemaName, apiExport.Spec.LatestResourceSchemas)
	}
}

func TestNewSchemasAreCreatedAsNeeded(t *testing.T) {
	const (
		apiExportName = "kcp.example.com"
	)

	ctx := context.Background()
	ctrlruntime.SetLogger(logr.Discard())

	// setup a test environment in kcp
	orgKubconfig := utils.CreateOrganization(t, ctx, "apiexport-new-schemas-are-created", apiExportName)

	// start a service cluster
	envtestKubeconfig, envtestClient, _ := utils.RunEnvtest(t, []string{
		"test/crds/crontab.yaml",
	})

	// publish Crontabs
	t.Logf("Publishing CRD…")
	managedSchemaSuffix := ".crontabs.example.com"
	prCrontabs := &syncagentv1alpha1.PublishedResource{
		ObjectMeta: metav1.ObjectMeta{
			Name: "publish-crontabs",
		},
		Spec: syncagentv1alpha1.PublishedResourceSpec{
			Resource: syncagentv1alpha1.SourceResourceDescriptor{
				APIGroup: "example.com",
				Versions: []string{"v1"},
				Kind:     "CronTab",
			},
			Related: []syncagentv1alpha1.RelatedResourceSpec{
				{
					Identifier: "super-secret",
					Origin:     "kcp",
					Kind:       "Secret",
					Object: syncagentv1alpha1.RelatedResourceObject{
						RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
							Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
								Path: "spec.test.name",
							},
						},
						Namespace: &syncagentv1alpha1.RelatedResourceObjectSpec{
							Reference: &syncagentv1alpha1.RelatedResourceObjectReference{
								Path: "spec.test.namespace",
							},
						},
					},
				},
			},
		},
	}

	if err := envtestClient.Create(ctx, prCrontabs); err != nil {
		t.Fatalf("Failed to create PublishedResource: %v", err)
	}

	// let the agent do its thing
	utils.RunAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, apiExportName)

	// wait for the APIExport to be updated
	t.Logf("Waiting for APIExport to be updated…")

	orgClient := utils.GetClient(t, orgKubconfig)
	apiExportKey := types.NamespacedName{Name: apiExportName}
	apiExport := &kcpapisv1alpha1.APIExport{}

	var oldSchemaName string
	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 1*time.Minute, false, func(ctx context.Context) (done bool, err error) {
		err = orgClient.Get(ctx, apiExportKey, apiExport)
		if err != nil {
			return false, err
		}

		for _, schema := range apiExport.Spec.LatestResourceSchemas {
			if strings.HasSuffix(schema, managedSchemaSuffix) {
				oldSchemaName = schema
				return true, nil
			}
		}

		return false, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for APIExport to be updated: %v", err)
	}

	// Cause a new ARS to be created; usually this is because the underlying CRD changes,
	// but here for simplicity we simply toggle the CRD's scope using projection.
	if err := envtestClient.Get(ctx, ctrlruntimeclient.ObjectKeyFromObject(prCrontabs), prCrontabs); err != nil {
		t.Fatalf("Failed to fetch current PublishedResource: %v", err)
	}

	prCrontabs.Spec.Projection = &syncagentv1alpha1.ResourceProjection{
		Scope: syncagentv1alpha1.ClusterScoped,
	}

	t.Logf("Changing PublishedResource…")
	if err := envtestClient.Update(ctx, prCrontabs); err != nil {
		t.Fatalf("Failed to update PublishedResource: %v", err)
	}

	// wait for the APIExport to be updated
	t.Logf("Waiting for APIExport to be updated…")
	err = wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 1*time.Minute, false, func(ctx context.Context) (done bool, err error) {
		err = orgClient.Get(ctx, apiExportKey, apiExport)
		if err != nil {
			return false, err
		}

		for _, schema := range apiExport.Spec.LatestResourceSchemas {
			if strings.HasSuffix(schema, managedSchemaSuffix) && schema != oldSchemaName {
				return true, nil
			}
		}

		return false, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for APIExport to be updated: %v", err)
	}

	// sanity check
	if len(apiExport.Spec.LatestResourceSchemas) != 1 {
		t.Fatalf("Expected 1 schema, but APIExport has %v instead.", apiExport.Spec.LatestResourceSchemas)
	}
}
