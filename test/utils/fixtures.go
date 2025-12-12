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

package utils

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"
	kcpapisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	kcpcorev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"

	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/util/yaml"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func CreateOrganization(
	t *testing.T,
	ctx context.Context,
	workspaceName string,
	apiExportName string,
) string {
	t.Helper()

	kcpClusterClient := GetKcpAdminClusterClient(t)
	agent := rbacv1.Subject{
		Kind: "User",
		Name: "api-syncagent-e2e",
	}

	// setup workspaces
	clusterPath := logicalcluster.NewPath("root")
	orgClusterName := CreateWorkspace(t, ctx, kcpClusterClient.Cluster(clusterPath), workspaceName)

	// grant access and allow the agent to resolve its own workspace path
	orgClient := kcpClusterClient.Cluster(clusterPath.Join(workspaceName))
	GrantWorkspaceAccess(t, ctx, orgClient, agent, rbacv1.PolicyRule{
		APIGroups:     []string{"core.kcp.io"},
		Resources:     []string{"logicalclusters"},
		ResourceNames: []string{"cluster"},
		Verbs:         []string{"get"},
	})

	// add some consumer workspaces
	teamClusters := []logicalcluster.Name{
		CreateWorkspace(t, ctx, orgClient, "team-1"),
		CreateWorkspace(t, ctx, orgClient, "team-2"),
	}

	// setup the APIExport and wait for it to be ready
	apiExport := CreateAPIExport(t, ctx, orgClient, apiExportName, &agent)

	// bind it in all team workspaces, so the virtual workspace is ready inside kcp
	for _, teamCluster := range teamClusters {
		BindToAPIExport(t, ctx, kcpClusterClient.Cluster(teamCluster.Path()), apiExport)
	}

	return CreateKcpAgentKubeconfig(t, fmt.Sprintf("/clusters/%s", orgClusterName))
}

func CreateWorkspace(t *testing.T, ctx context.Context, client ctrlruntimeclient.Client, workspaceName string) logicalcluster.Name {
	t.Helper()

	testWs := &kcptenancyv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name: workspaceName,
		},
	}

	t.Logf("Creating workspace %s…", workspaceName)
	if err := client.Create(ctx, testWs); err != nil {
		t.Fatalf("Failed to create %q workspace: %v", workspaceName, err)
	}

	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, false, func(ctx context.Context) (done bool, err error) {
		err = client.Get(ctx, ctrlruntimeclient.ObjectKeyFromObject(testWs), testWs)
		if err != nil {
			return false, err
		}

		return testWs.Status.Phase == kcpcorev1alpha1.LogicalClusterPhaseReady, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for workspace to become ready: %v", err)
	}

	return logicalcluster.Name(testWs.Spec.Cluster)
}

func CreateAPIExport(t *testing.T, ctx context.Context, client ctrlruntimeclient.Client, name string, rbacSubject *rbacv1.Subject) *kcpapisv1alpha1.APIExport {
	t.Helper()

	// create the APIExport to server with the Sync Agent
	apiExport := &kcpapisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}

	t.Logf("Creating APIExport %q…", name)
	if err := client.Create(ctx, apiExport); err != nil {
		t.Fatalf("Failed to create APIExport: %v", err)
	}

	// In kcp 0.27, we have to manually create the AEES. To make the tests work consistently with
	// 0.27 and later versions, we simply always create one.
	endpointSlice := &kcpapisv1alpha1.APIExportEndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: kcpapisv1alpha1.APIExportEndpointSliceSpec{
			APIExport: kcpapisv1alpha1.ExportBindingReference{
				Name: name,
			},
		},
	}

	t.Logf("Creating APIExportEndpointSlice %q…", endpointSlice.Name)
	if err := client.Create(ctx, endpointSlice); err != nil {
		t.Fatalf("Failed to create APIExportEndpointSlice: %v", err)
	}

	// grant permissions to access/manage the APIExport
	if rbacSubject != nil {
		clusterRoleName := "api-syncagent"
		clusterRole := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterRoleName,
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups:     []string{"apis.kcp.io"},
					Resources:     []string{"apiexports"},
					ResourceNames: []string{name},
					Verbs:         []string{"get", "list", "watch", "patch", "update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"events"},
					Verbs:     []string{"get", "create", "update", "patch"},
				},
				{
					APIGroups: []string{"apis.kcp.io"},
					Resources: []string{"apiexportendpointslices"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{"apis.kcp.io"},
					Resources: []string{"apiresourceschemas"},
					Verbs:     []string{"get", "list", "watch", "create"},
				},
				{
					APIGroups:     []string{"apis.kcp.io"},
					Resources:     []string{"apiexports/content"},
					ResourceNames: []string{name},
					Verbs:         []string{"*"},
				},
			},
		}

		if err := client.Create(ctx, clusterRole); err != nil {
			t.Fatalf("Failed to create ClusterRole: %v", err)
		}

		clusterRoleBinding := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterRoleName,
			},
			Subjects: []rbacv1.Subject{*rbacSubject},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     clusterRoleName,
			},
		}

		if err := client.Create(ctx, clusterRoleBinding); err != nil {
			t.Fatalf("Failed to create ClusterRoleBinding: %v", err)
		}
	}

	return apiExport
}

func GrantWorkspaceAccess(t *testing.T, ctx context.Context, client ctrlruntimeclient.Client, rbacSubject rbacv1.Subject, extraRules ...rbacv1.PolicyRule) {
	t.Helper()

	clusterRoleName := fmt.Sprintf("access-workspace:%s", strings.ToLower(rbacSubject.Name))
	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: clusterRoleName,
		},
		Rules: append([]rbacv1.PolicyRule{
			{
				Verbs:           []string{"access"},
				NonResourceURLs: []string{"/"},
			},
		}, extraRules...),
	}

	if err := client.Create(ctx, clusterRole); err != nil {
		t.Fatalf("Failed to create ClusterRole: %v", err)
	}

	clusterRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "workspace-access-",
		},
		Subjects: []rbacv1.Subject{rbacSubject},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     clusterRoleName,
		},
	}

	if err := client.Create(ctx, clusterRoleBinding); err != nil {
		t.Fatalf("Failed to create ClusterRoleBinding: %v", err)
	}
}

func BindToAPIExport(t *testing.T, ctx context.Context, client ctrlruntimeclient.Client, apiExport *kcpapisv1alpha1.APIExport) *kcpapisv1alpha1.APIBinding {
	t.Helper()

	apiBinding := &kcpapisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: apiExport.Name,
		},
		Spec: kcpapisv1alpha1.APIBindingSpec{
			Reference: kcpapisv1alpha1.BindingReference{
				Export: &kcpapisv1alpha1.ExportBindingReference{
					Path: string(logicalcluster.From(apiExport)),
					Name: apiExport.Name,
				},
			},
			// Specifying claims when the APIExport has none will lead to a condition
			// on the APIBinding, but will not impact its functionality.
			PermissionClaims: []kcpapisv1alpha1.AcceptablePermissionClaim{
				// the agent nearly always requires access to namespaces within workspaces
				{
					PermissionClaim: kcpapisv1alpha1.PermissionClaim{
						GroupResource: kcpapisv1alpha1.GroupResource{
							Group:    "",
							Resource: "namespaces",
						},
						All: true,
					},
					State: kcpapisv1alpha1.ClaimAccepted,
				},
				{
					PermissionClaim: kcpapisv1alpha1.PermissionClaim{
						GroupResource: kcpapisv1alpha1.GroupResource{
							Group:    "",
							Resource: "events",
						},
						All: true,
					},
					State: kcpapisv1alpha1.ClaimAccepted,
				},
				// for related resources, the agent can also sync ConfigMaps and Secrets;
				// for all testcases that use foreign resources (i.e. those not part of
				// the core Kubernetes group or the same APIExport), the tests will adjust
				// these permission claims later.
				{
					PermissionClaim: kcpapisv1alpha1.PermissionClaim{
						GroupResource: kcpapisv1alpha1.GroupResource{
							Group:    "",
							Resource: "secrets",
						},
						All: true,
					},
					State: kcpapisv1alpha1.ClaimAccepted,
				},
				{
					PermissionClaim: kcpapisv1alpha1.PermissionClaim{
						GroupResource: kcpapisv1alpha1.GroupResource{
							Group:    "",
							Resource: "configmaps",
						},
						All: true,
					},
					State: kcpapisv1alpha1.ClaimAccepted,
				},
				{
					PermissionClaim: kcpapisv1alpha1.PermissionClaim{
						GroupResource: kcpapisv1alpha1.GroupResource{
							Group:    "core.kcp.io",
							Resource: "logicalclusters",
						},
						All: true,
					},
					State: kcpapisv1alpha1.ClaimAccepted,
				},
			},
		},
	}

	t.Logf("Creating APIBinding %q…", apiBinding.Name)
	if err := client.Create(ctx, apiBinding); err != nil {
		t.Fatalf("Failed to create APIBinding: %v", err)
	}

	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, false, func(ctx context.Context) (done bool, err error) {
		err = client.Get(ctx, ctrlruntimeclient.ObjectKeyFromObject(apiBinding), apiBinding)
		if err != nil {
			return false, err
		}

		return conditions.IsTrue(apiBinding, conditionsv1alpha1.ReadyCondition), nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for APIBinding virtual workspace to become ready: %v", err)
	}

	return apiBinding
}

func AcceptAllPermissionClaims(t *testing.T, ctx context.Context, client ctrlruntimeclient.Client, apiExport *kcpapisv1alpha1.APIExport) {
	allBindings := &kcpapisv1alpha1.APIBindingList{}
	if err := client.List(ctx, allBindings); err != nil {
		t.Fatalf("Failed to list APIBindings: %v", err)
	}

	var apiBinding *kcpapisv1alpha1.APIBinding
	for _, binding := range allBindings.Items {
		// for simplicity, we only match against the name
		if exp := binding.Spec.Reference.Export; exp != nil && exp.Name == apiExport.Name {
			apiBinding = &binding
			break
		}
	}

	if apiBinding == nil {
		t.Fatalf("No APIBinding found that binds %s.", apiExport.Name)
	}

	accepted := []kcpapisv1alpha1.AcceptablePermissionClaim{}
	for _, claim := range apiExport.Spec.PermissionClaims {
		accepted = append(accepted, kcpapisv1alpha1.AcceptablePermissionClaim{
			PermissionClaim: claim,
			State:           kcpapisv1alpha1.ClaimAccepted,
		})
	}

	apiBinding.Spec.PermissionClaims = accepted
	if err := client.Update(ctx, apiBinding); err != nil {
		t.Fatalf("Failed to update APIBinding: %v", err)
	}
}

func ApplyCRD(t *testing.T, ctx context.Context, client ctrlruntimeclient.Client, filename string) {
	t.Helper()

	crd := loadCRD(t, filename)

	existingCRD := &apiextensionsv1.CustomResourceDefinition{}
	if err := client.Get(ctx, ctrlruntimeclient.ObjectKeyFromObject(crd), existingCRD); err != nil {
		if err := client.Create(ctx, crd); err != nil {
			t.Fatalf("Failed to create CRD: %v", err)
		}
	} else {
		existingCRD.Spec = crd.Spec

		if err := client.Update(ctx, existingCRD); err != nil {
			t.Fatalf("Failed to update CRD: %v", err)
		}
	}
}

func loadCRD(t *testing.T, filename string) *apiextensionsv1.CustomResourceDefinition {
	t.Helper()

	rootDirectory := requiredEnv(t, "ROOT_DIRECTORY")

	f, err := os.Open(filepath.Join(rootDirectory, filename))
	if err != nil {
		t.Fatalf("Failed to read CRD: %v", err)
	}
	defer f.Close()

	crd := &apiextensionsv1.CustomResourceDefinition{}
	dec := yaml.NewYAMLOrJSONDecoder(f, 1024)
	if err := dec.Decode(crd); err != nil {
		t.Fatalf("Failed to decode CRD: %v", err)
	}

	return crd
}
