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

package apiresourceschema

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"

	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"
	"github.com/kcp-dev/api-syncagent/test/utils"

	kcpapisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrlruntime "sigs.k8s.io/controller-runtime"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func TestARSAreCreated(t *testing.T) {
	const (
		apiExportName = "example.com"
	)

	ctx := context.Background()
	ctrlruntime.SetLogger(logr.Discard())

	// setup a test environment in kcp
	orgKubconfig := utils.CreateOrganization(t, ctx, "ars-are-created", apiExportName)

	// start a service cluster
	envtestKubeconfig, envtestClient, _ := utils.RunEnvtest(t, []string{
		"test/crds/crontab.yaml",
	})

	// publish Crontabs
	t.Logf("Publishing CronTabs…")
	pr := &syncagentv1alpha1.PublishedResource{
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

	if err := envtestClient.Create(ctx, pr); err != nil {
		t.Fatalf("Failed to create PublishedResource: %v", err)
	}

	// let the agent do its thing
	utils.RunAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, apiExportName)

	// wait for the APIExport to be updated
	t.Logf("Waiting for APIExport to be updated…")
	orgClient := utils.GetClient(t, orgKubconfig)
	apiExportKey := types.NamespacedName{Name: apiExportName}

	var arsName string
	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 1*time.Minute, false, func(ctx context.Context) (done bool, err error) {
		apiExport := &kcpapisv1alpha1.APIExport{}
		err = orgClient.Get(ctx, apiExportKey, apiExport)
		if err != nil {
			return false, err
		}

		if len(apiExport.Spec.LatestResourceSchemas) == 0 {
			return false, nil
		}

		arsName = apiExport.Spec.LatestResourceSchemas[0]

		return true, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for APIExport to be updated: %v", err)
	}

	// check the APIResourceSchema
	ars := &kcpapisv1alpha1.APIResourceSchema{}
	err = orgClient.Get(ctx, types.NamespacedName{Name: arsName}, ars)
	if err != nil {
		t.Fatalf("APIResourceSchema does not exist: %v", err)
	}
}

func TestARSAreNotUpdated(t *testing.T) {
	const (
		apiExportName = "example.com"
	)

	ctx := context.Background()
	ctrlruntime.SetLogger(logr.Discard())

	// setup a test environment in kcp
	orgKubconfig := utils.CreateOrganization(t, ctx, "ars-are-not-updated", apiExportName)

	// start a service cluster
	envtestKubeconfig, envtestClient, _ := utils.RunEnvtest(t, []string{
		"test/crds/crontab.yaml",
	})

	// publish Crontabs
	t.Logf("Publishing CronTabs…")
	pr := &syncagentv1alpha1.PublishedResource{
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

	if err := envtestClient.Create(ctx, pr); err != nil {
		t.Fatalf("Failed to create PublishedResource: %v", err)
	}

	// let the agent do its thing
	utils.RunAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, apiExportName)

	// check ARS
	t.Logf("Waiting for APIResourceSchema to be created…")
	orgClient := utils.GetClient(t, orgKubconfig)

	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 1*time.Minute, false, func(ctx context.Context) (done bool, err error) {
		schemas := &kcpapisv1alpha1.APIResourceSchemaList{}
		err = orgClient.List(ctx, schemas, ctrlruntimeclient.HasLabels{syncagentv1alpha1.AgentNameLabel})
		if err != nil {
			return false, err
		}

		return len(schemas.Items) == 1, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for APIResourceSchema to be created: %v", err)
	}

	if err := envtestClient.Get(ctx, ctrlruntimeclient.ObjectKeyFromObject(pr), pr); err != nil {
		t.Fatalf("Failed to fetch PublishedResource: %v", err)
	}

	arsName := pr.Status.ResourceSchemaName
	if arsName == "" {
		t.Fatal("Expected PublishedResource status to contain ARS name, but value is empty.")
	}

	// update the CRD
	t.Logf("Updating CRD (same version, but new schema)…")
	utils.ApplyCRD(t, ctx, envtestClient, "test/crds/crontab-improved.yaml")

	// wait for the 2nd ARS to appear
	t.Logf("Waiting for 2nd APIResourceSchema to be created…")
	err = wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 1*time.Minute, false, func(ctx context.Context) (done bool, err error) {
		schemas := &kcpapisv1alpha1.APIResourceSchemaList{}
		err = orgClient.List(ctx, schemas, ctrlruntimeclient.HasLabels{syncagentv1alpha1.AgentNameLabel})
		if err != nil {
			return false, err
		}

		return len(schemas.Items) == 2, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for 2nd APIResourceSchema to be created: %v", err)
	}

	if err := envtestClient.Get(ctx, ctrlruntimeclient.ObjectKeyFromObject(pr), pr); err != nil {
		t.Fatalf("Failed to fetch PublishedResource: %v", err)
	}

	newARSName := pr.Status.ResourceSchemaName
	if newARSName == "" {
		t.Fatal("Expected PublishedResource status to contain ARS name, but value is empty.")
	}

	if newARSName == arsName {
		t.Fatalf("Expected PublishedResource status to have been updated with new ARS name, but still contains %q.", arsName)
	}
}

func TestARSOnlyContainsSelectedCRDVersion(t *testing.T) {
	const (
		apiExportName = "example.com"
		theVersion    = "v1"
	)

	ctx := context.Background()
	ctrlruntime.SetLogger(logr.Discard())

	// setup a test environment in kcp
	orgKubconfig := utils.CreateOrganization(t, ctx, "ars-single-version-only", apiExportName)

	// start a service cluster
	envtestKubeconfig, envtestClient, _ := utils.RunEnvtest(t, []string{
		"test/crds/crontab-multi-versions.yaml",
	})

	// publish Crontabs
	t.Logf("Publishing CronTabs…")
	pr := &syncagentv1alpha1.PublishedResource{
		ObjectMeta: metav1.ObjectMeta{
			Name: "publish-crontabs",
		},
		Spec: syncagentv1alpha1.PublishedResourceSpec{
			Resource: syncagentv1alpha1.SourceResourceDescriptor{
				APIGroup: "example.com",
				Version:  theVersion,
				Kind:     "CronTab",
			},
		},
	}

	if err := envtestClient.Create(ctx, pr); err != nil {
		t.Fatalf("Failed to create PublishedResource: %v", err)
	}

	// let the agent do its thing
	utils.RunAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, apiExportName)

	// wait for the APIExport to be updated
	t.Logf("Waiting for APIExport to be updated…")
	orgClient := utils.GetClient(t, orgKubconfig)
	apiExportKey := types.NamespacedName{Name: apiExportName}

	var arsName string
	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 1*time.Minute, false, func(ctx context.Context) (done bool, err error) {
		apiExport := &kcpapisv1alpha1.APIExport{}
		err = orgClient.Get(ctx, apiExportKey, apiExport)
		if err != nil {
			return false, err
		}

		if len(apiExport.Spec.LatestResourceSchemas) == 0 {
			return false, nil
		}

		arsName = apiExport.Spec.LatestResourceSchemas[0]

		return true, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for APIExport to be updated: %v", err)
	}

	// check the APIResourceSchema
	ars := &kcpapisv1alpha1.APIResourceSchema{}
	err = orgClient.Get(ctx, types.NamespacedName{Name: arsName}, ars)
	if err != nil {
		t.Fatalf("APIResourceSchema does not exist: %v", err)
	}

	if len(ars.Spec.Versions) != 1 {
		t.Fatalf("Expected only one version to remain in ARS, but found %d.", len(ars.Spec.Versions))
	}

	if name := ars.Spec.Versions[0].Name; name != theVersion {
		t.Fatalf("Expected ARS to contain %q, but contains %q.", theVersion, name)
	}
}

func TestProjection(t *testing.T) {
	const (
		apiExportName   = "example.com"
		originalVersion = "v1"
	)

	ctx := context.Background()
	ctrlruntime.SetLogger(logr.Discard())

	// setup a test environment in kcp
	orgKubconfig := utils.CreateOrganization(t, ctx, "ars-projections", apiExportName)

	// start a service cluster
	envtestKubeconfig, envtestClient, _ := utils.RunEnvtest(t, []string{
		"test/crds/crontab.yaml",
	})

	// publish Crontabs
	t.Logf("Publishing CronTabs…")
	pr := &syncagentv1alpha1.PublishedResource{
		ObjectMeta: metav1.ObjectMeta{
			Name: "publish-crontabs",
		},
		Spec: syncagentv1alpha1.PublishedResourceSpec{
			Resource: syncagentv1alpha1.SourceResourceDescriptor{
				APIGroup: "example.com",
				Version:  originalVersion,
				Kind:     "CronTab",
			},
			Projection: &syncagentv1alpha1.ResourceProjection{
				Version:    "v6",
				Scope:      syncagentv1alpha1.ClusterScoped,
				Kind:       "CronusTabulatus",
				Plural:     "cronustabulati",
				ShortNames: []string{"cront"},
			},
		},
	}

	if err := envtestClient.Create(ctx, pr); err != nil {
		t.Fatalf("Failed to create PublishedResource: %v", err)
	}

	// let the agent do its thing
	utils.RunAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, apiExportName)

	// wait for the APIExport to be updated
	t.Logf("Waiting for APIExport to be updated…")
	orgClient := utils.GetClient(t, orgKubconfig)
	apiExportKey := types.NamespacedName{Name: apiExportName}

	var arsName string
	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 1*time.Minute, false, func(ctx context.Context) (done bool, err error) {
		apiExport := &kcpapisv1alpha1.APIExport{}
		err = orgClient.Get(ctx, apiExportKey, apiExport)
		if err != nil {
			return false, err
		}

		if len(apiExport.Spec.LatestResourceSchemas) == 0 {
			return false, nil
		}

		arsName = apiExport.Spec.LatestResourceSchemas[0]

		return true, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for APIExport to be updated: %v", err)
	}

	// check the APIResourceSchema
	ars := &kcpapisv1alpha1.APIResourceSchema{}
	err = orgClient.Get(ctx, types.NamespacedName{Name: arsName}, ars)
	if err != nil {
		t.Fatalf("APIResourceSchema does not exist: %v", err)
	}

	if len(ars.Spec.Versions) != 1 {
		t.Fatalf("Expected only one version to remain in ARS, but found %d.", len(ars.Spec.Versions))
	}

	if name := ars.Spec.Versions[0].Name; name != pr.Spec.Projection.Version {
		t.Errorf("Expected ARS to contain version %q, but contains %q.", pr.Spec.Projection.Version, name)
	}

	if ars.Spec.Scope != apiextensionsv1.ResourceScope(pr.Spec.Projection.Scope) {
		t.Errorf("Expected ARS to be of scope %q, but is %q.", pr.Spec.Projection.Scope, ars.Spec.Scope)
	}

	if ars.Spec.Names.Kind != pr.Spec.Projection.Kind {
		t.Errorf("Expected ARS to be kind %q, but is %q.", pr.Spec.Projection.Kind, ars.Spec.Names.Kind)
	}

	if ars.Spec.Names.Plural != pr.Spec.Projection.Plural {
		t.Errorf("Expected ARS to have plural name %q, but has %q.", pr.Spec.Projection.Plural, ars.Spec.Names.Plural)
	}

	if !cmp.Equal(ars.Spec.Names.ShortNames, pr.Spec.Projection.ShortNames) {
		t.Errorf("Expected ARS to have short names %v, but has %v.", pr.Spec.Projection.ShortNames, ars.Spec.Names.ShortNames)
	}
}

func TestNonCRDResource(t *testing.T) {
	const (
		apiExportName   = "example.com"
		originalVersion = "v1"
	)

	ctx := context.Background()
	ctrlruntime.SetLogger(logr.Discard())

	// setup a test environment in kcp
	orgKubconfig := utils.CreateOrganization(t, ctx, "ars-non-crd", apiExportName)

	// start a service cluster
	envtestKubeconfig, envtestClient, _ := utils.RunEnvtest(t, nil)

	// publish rbac Roles
	t.Logf("Publishing Roles…")
	pr := &syncagentv1alpha1.PublishedResource{
		ObjectMeta: metav1.ObjectMeta{
			Name: "publish-roles",
		},
		Spec: syncagentv1alpha1.PublishedResourceSpec{
			Resource: syncagentv1alpha1.SourceResourceDescriptor{
				APIGroup: "rbac.authorization.k8s.io",
				Version:  "v1",
				Kind:     "Role",
			},
			Projection: &syncagentv1alpha1.ResourceProjection{
				// must project to a different API group, because kubernetes will reject CRDs in those
				// known core groups with the "api-approved.kubernetes.io" annotation on them.
				Group: apiExportName,
			},
		},
	}

	if err := envtestClient.Create(ctx, pr); err != nil {
		t.Fatalf("Failed to create PublishedResource: %v", err)
	}

	// let the agent do its thing
	utils.RunAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, apiExportName)

	// wait for the APIExport to be updated
	t.Logf("Waiting for APIExport to be updated…")
	orgClient := utils.GetClient(t, orgKubconfig)
	apiExportKey := types.NamespacedName{Name: apiExportName}

	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 1*time.Minute, false, func(ctx context.Context) (done bool, err error) {
		apiExport := &kcpapisv1alpha1.APIExport{}
		err = orgClient.Get(ctx, apiExportKey, apiExport)
		if err != nil {
			return false, err
		}

		return len(apiExport.Spec.LatestResourceSchemas) > 0, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for APIExport to be updated: %v", err)
	}
}
