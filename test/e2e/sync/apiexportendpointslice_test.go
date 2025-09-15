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
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/kcp-dev/logicalcluster/v3"

	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"
	"github.com/kcp-dev/api-syncagent/test/utils"

	kcpdevv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrlruntime "sigs.k8s.io/controller-runtime"
)

// TestAPIExportEndpointSlice is functionally equivalent to a simple sync test,
// but is bootstrapping the agent using a AEES ref instead of an APIExport ref.
func TestAPIExportEndpointSliceSameCluster(t *testing.T) {
	const (
		apiExportName = "kcp.example.com"
		kcpGroupName  = "kcp.example.com"
		orgWorkspace  = "endpointslice-same-cluster"
	)

	ctx := t.Context()
	ctrlruntime.SetLogger(logr.Discard())

	// setup a test environment in kcp
	orgKubconfig := utils.CreateOrganization(t, ctx, orgWorkspace, apiExportName)

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
				Group: kcpGroupName,
			},
		},
	}

	if err := envtestClient.Create(ctx, prCrontabs); err != nil {
		t.Fatalf("Failed to create PublishedResource: %v", err)
	}

	// In kcp 0.27, we have to manually create the AEES. To make this test work consistently with
	// 0.27 and later versions, we simply always create one.
	kcpClusterClient := utils.GetKcpAdminClusterClient(t)
	orgClient := kcpClusterClient.Cluster(logicalcluster.NewPath("root").Join(orgWorkspace))

	endpointSlice := &kcpdevv1alpha1.APIExportEndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name: "dummy",
		},
		Spec: kcpdevv1alpha1.APIExportEndpointSliceSpec{
			APIExport: kcpdevv1alpha1.ExportBindingReference{
				Name: apiExportName,
			},
		},
	}

	t.Logf("Creating APIExportEndpointSlice %q…", endpointSlice.Name)
	if err := orgClient.Create(ctx, endpointSlice); err != nil {
		t.Fatalf("Failed to create APIExportEndpointSlice: %v", err)
	}

	// start the agent in the background to update the APIExport with the CronTabs API;
	utils.RunEndpointSliceAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, endpointSlice.Name)

	// wait until the API is available
	teamClusterPath := logicalcluster.NewPath("root").Join(orgWorkspace).Join("team-1")
	teamClient := kcpClusterClient.Cluster(teamClusterPath)

	utils.WaitForBoundAPI(t, ctx, teamClient, schema.GroupVersionResource{
		Group:    kcpGroupName,
		Version:  "v1",
		Resource: "crontabs",
	})

	// In kcp 0.27, the binding' status is not perfectly in-sync with the actual APIs available in
	// the workspace; since this is fixed in 0.28+ and we really care about the APIBinding, not necessarily
	// the Kubernetes magic behind it, we keep the loop above but on 0.27 add a small synthetic delay.
	// TODO: Remove this once we do not support kcp 0.27 anymore.
	if utils.KCPMinor() < 28 {
		time.Sleep(2 * time.Second)
	}

	// create a Crontab object in a team workspace
	t.Log("Creating CronTab in kcp…")
	crontab := utils.YAMLToUnstructured(t, `
apiVersion: kcp.example.com/v1
kind: CronTab
metadata:
  namespace: default
  name: my-crontab
spec:
  cronSpec: '* * *'
  image: ubuntu:latest
`)

	if err := teamClient.Create(ctx, crontab); err != nil {
		t.Fatalf("Failed to create CronTab in kcp: %v", err)
	}

	// wait for the agent to sync the object down into the service cluster

	t.Logf("Wait for CronTab to be synced…")
	copy := &unstructured.Unstructured{}
	copy.SetAPIVersion("example.com/v1")
	copy.SetKind("CronTab")

	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, false, func(ctx context.Context) (done bool, err error) {
		copyKey := types.NamespacedName{Namespace: "synced-default", Name: "my-crontab"}
		return envtestClient.Get(ctx, copyKey, copy) == nil, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for object to be synced down: %v", err)
	}
}

func TestAPIExportEndpointSliceDifferentCluster(t *testing.T) {
	const (
		apiExportName     = "kcp.example.com"
		kcpGroupName      = "kcp.example.com"
		orgWorkspace      = "endpointslice-different-cluster"
		endpointWorkspace = "endpoint"
	)

	ctx := t.Context()
	ctrlruntime.SetLogger(logr.Discard())

	// setup a test environment in kcp
	rootCluster := logicalcluster.NewPath("root")
	utils.CreateOrganization(t, ctx, orgWorkspace, apiExportName)

	// create a custom AEES in a different cluster than the APIExport
	kcpClusterClient := utils.GetKcpAdminClusterClient(t)
	orgClient := kcpClusterClient.Cluster(rootCluster.Join(orgWorkspace))
	endpointClusterName := utils.CreateWorkspace(t, ctx, orgClient, endpointWorkspace)
	endpointClient := kcpClusterClient.Cluster(endpointClusterName.Path())

	endpointSlice := &kcpdevv1alpha1.APIExportEndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name: "dummy",
		},
		Spec: kcpdevv1alpha1.APIExportEndpointSliceSpec{
			APIExport: kcpdevv1alpha1.ExportBindingReference{
				Path: rootCluster.Join(orgWorkspace).String(),
				Name: apiExportName,
			},
		},
	}

	t.Logf("Creating APIExportEndpointSlice %q…", endpointSlice.Name)
	if err := endpointClient.Create(ctx, endpointSlice); err != nil {
		t.Fatalf("Failed to create APIExportEndpointSlice: %v", err)
	}

	agent := rbacv1.Subject{
		Kind: "User",
		Name: "api-syncagent-e2e",
	}

	utils.GrantWorkspaceAccess(t, ctx, endpointClient, agent, rbacv1.PolicyRule{
		APIGroups:     []string{"core.kcp.io"},
		Resources:     []string{"logicalclusters"},
		ResourceNames: []string{"cluster"},
		Verbs:         []string{"get"},
	}, rbacv1.PolicyRule{
		APIGroups:     []string{"apis.kcp.io"},
		Resources:     []string{"apiexportendpointslices"},
		ResourceNames: []string{endpointSlice.Name},
		Verbs:         []string{"get", "list", "watch"},
	})

	endpointKubeconfig := utils.CreateKcpAgentKubeconfig(t, fmt.Sprintf("/clusters/%s", endpointClusterName))

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
				Group: kcpGroupName,
			},
		},
	}

	if err := envtestClient.Create(ctx, prCrontabs); err != nil {
		t.Fatalf("Failed to create PublishedResource: %v", err)
	}

	// start the agent in the background to update the APIExport with the CronTabs API
	utils.RunEndpointSliceAgent(ctx, t, "bob", endpointKubeconfig, envtestKubeconfig, endpointSlice.Name)

	// wait until the API is available

	teamClusterPath := logicalcluster.NewPath("root").Join(orgWorkspace).Join("team-1")
	teamClient := kcpClusterClient.Cluster(teamClusterPath)

	utils.WaitForBoundAPI(t, ctx, teamClient, schema.GroupVersionResource{
		Group:    kcpGroupName,
		Version:  "v1",
		Resource: "crontabs",
	})

	// TODO: Remove this once we do not support kcp 0.27 anymore.
	if utils.KCPMinor() < 28 {
		time.Sleep(2 * time.Second)
	}

	// create a Crontab object in a team workspace
	t.Log("Creating CronTab in kcp…")
	crontab := utils.YAMLToUnstructured(t, `
apiVersion: kcp.example.com/v1
kind: CronTab
metadata:
  namespace: default
  name: my-crontab
spec:
  cronSpec: '* * *'
  image: ubuntu:latest
`)

	if err := teamClient.Create(ctx, crontab); err != nil {
		t.Fatalf("Failed to create CronTab in kcp: %v", err)
	}

	// wait for the agent to sync the object down into the service cluster

	t.Logf("Wait for CronTab to be synced…")
	copy := &unstructured.Unstructured{}
	copy.SetAPIVersion("example.com/v1")
	copy.SetKind("CronTab")

	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, false, func(ctx context.Context) (done bool, err error) {
		copyKey := types.NamespacedName{Namespace: "synced-default", Name: "my-crontab"}
		return envtestClient.Get(ctx, copyKey, copy) == nil, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for object to be synced down: %v", err)
	}
}
