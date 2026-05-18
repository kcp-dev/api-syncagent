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
	"testing"
	"time"

	"github.com/go-logr/logr"

	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"
	"github.com/kcp-dev/api-syncagent/test/utils"

	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrlruntime "sigs.k8s.io/controller-runtime"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// TestRelatedResourceCleanupDisabled verifies that when a primary object
// is deleted, related resources are NOT automatically deleted by default.
func TestRelatedResourceCleanupDisabled(t *testing.T) {
	const (
		apiExportName = "kcp.example.com"
		orgWorkspace  = "cleanup-disabled"
	)

	ctx := t.Context()
	ctrlruntime.SetLogger(logr.Discard())

	// setup a test environment in kcp
	orgKubconfig := utils.CreateOrganization(t, ctx, orgWorkspace, apiExportName)

	// start a service cluster
	envtestKubeconfig, envtestClient, _ := utils.RunEnvtest(t, []string{
		"test/crds/crontab.yaml",
	})

	// unrelated to this test, but ensures that the agent quickly finds the
	// Secret on the service cluster
	dummyLabels := map[string]string{
		"syncagent-e2e": "find-me",
	}

	// publish Crontabs with a related Secret (origin: service)
	// RelatedResourceCleanup is NOT set, so it defaults to false
	t.Log("Publishing CRDs…")
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
			Naming: &syncagentv1alpha1.ResourceNaming{
				Name:      "{{ .Object.metadata.name }}",
				Namespace: "synced-{{ .Object.metadata.namespace }}",
			},
			Projection: &syncagentv1alpha1.ResourceProjection{
				Group: "kcp.example.com",
			},
			Related: []syncagentv1alpha1.RelatedResourceSpec{
				{
					Identifier: "credentials",
					Origin:     syncagentv1alpha1.RelatedResourceOriginService,
					Kind:       "Secret",
					Object: syncagentv1alpha1.RelatedResourceObject{
						RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
							Template: &syncagentv1alpha1.TemplateExpression{
								Template: "my-credentials",
							},
						},
					},
					Watch: &syncagentv1alpha1.RelatedResourceWatch{
						BySelector: &metav1.LabelSelector{
							MatchLabels: dummyLabels,
						},
					},
				},
			},
		},
	}

	if err := envtestClient.Create(ctx, prCrontabs); err != nil {
		t.Fatalf("Failed to create PublishedResource: %v", err)
	}

	// start the agent
	utils.RunAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, apiExportName, "")

	// wait until the API is available
	kcpClusterClient := utils.GetKcpAdminClusterClient(t)

	teamClusterPath := logicalcluster.NewPath("root").Join(orgWorkspace).Join("team-1")
	teamClient := kcpClusterClient.Cluster(teamClusterPath)

	utils.WaitForBoundAPI(t, ctx, teamClient, schema.GroupVersionKind{
		Group:   "kcp.example.com",
		Version: "v1",
		Kind:    "CronTab",
	})

	// Create a CronTab in kcp
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

	crontab.SetLabels(dummyLabels)

	if err := teamClient.Create(ctx, crontab); err != nil {
		t.Fatalf("Failed to create CronTab in kcp: %v", err)
	}

	// Wait for the CronTab to be synced down to the service cluster
	// This also ensures the namespace is created
	t.Log("Waiting for CronTab to be synced to service cluster…")
	serviceCrontab := &unstructured.Unstructured{}
	serviceCrontab.SetAPIVersion("example.com/v1")
	serviceCrontab.SetKind("CronTab")

	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, false, func(ctx context.Context) (done bool, err error) {
		return envtestClient.Get(ctx, types.NamespacedName{Namespace: "synced-default", Name: "my-crontab"}, serviceCrontab) == nil, nil
	})
	if err != nil {
		t.Fatalf("CronTab was not synced to service cluster: %v", err)
	}

	t.Log("CronTab synced to service cluster")

	// Create the related Secret on the service cluster
	t.Log("Creating credential Secret on service cluster…")
	serviceSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-credentials",
			Namespace: "synced-default",
		},
		Data: map[string][]byte{
			"password": []byte("hunter2"),
		},
		Type: corev1.SecretTypeOpaque,
	}

	if err := envtestClient.Create(ctx, serviceSecret); err != nil {
		t.Fatalf("Failed to create Secret on service cluster: %v", err)
	}

	// Wait for the Secret to be synced up to kcp
	t.Log("Waiting for Secret to be synced to kcp…")
	kcpSecret := &corev1.Secret{}
	err = wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, false, func(ctx context.Context) (done bool, err error) {
		return teamClient.Get(ctx, types.NamespacedName{Name: "my-credentials", Namespace: "default"}, kcpSecret) == nil, nil
	})
	if err != nil {
		t.Fatalf("Secret was not synced to kcp: %v", err)
	}

	t.Log("Secret successfully synced to kcp")

	// Delete the primary CronTab
	t.Log("Deleting CronTab in kcp…")
	if err := teamClient.Delete(ctx, crontab); err != nil {
		t.Fatalf("Failed to delete CronTab: %v", err)
	}

	// Wait for CronTab to be fully deleted
	err = wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, false, func(ctx context.Context) (done bool, err error) {
		err = teamClient.Get(ctx, ctrlruntimeclient.ObjectKeyFromObject(crontab), crontab)
		return apierrors.IsNotFound(err), nil
	})
	if err != nil {
		t.Fatalf("CronTab was not deleted: %v", err)
	}

	t.Log("CronTab deleted successfully")

	// Verify that the related resources are NOT deleted on either side of the sync.

	t.Log("Verifying that Secret in kcp remains…")
	err = teamClient.Get(ctx, types.NamespacedName{Name: "my-credentials", Namespace: "default"}, kcpSecret)
	if err != nil {
		if apierrors.IsNotFound(err) {
			t.Error("Secret in kcp was deleted, but should have been preserved")
		}
		t.Errorf("Failed to get Secret in kcp: %v", err)
	}

	t.Log("Verifying that Secret on service cluster remains…")
	err = envtestClient.Get(ctx, types.NamespacedName{Name: "my-credentials", Namespace: "synced-default"}, serviceSecret)
	if err != nil {
		if apierrors.IsNotFound(err) {
			t.Error("Secret on service cluster was deleted, but should have been preserved")
		}
		t.Errorf("Failed to get Secret on service cluster: %v", err)
	}
}

func TestRelatedResourceCleanupEnabled(t *testing.T) {
	const (
		apiExportName = "kcp.example.com"
		orgWorkspace  = "cleanup-enabled"
	)

	ctx := t.Context()
	ctrlruntime.SetLogger(logr.Discard())

	// setup a test environment in kcp
	orgKubconfig := utils.CreateOrganization(t, ctx, orgWorkspace, apiExportName)

	// start a service cluster
	envtestKubeconfig, envtestClient, _ := utils.RunEnvtest(t, []string{
		"test/crds/crontab.yaml",
	})

	// unrelated to this test, but ensures that the agent quickly finds the
	// Secret on the service cluster
	dummyLabels := map[string]string{
		"syncagent-e2e": "find-me",
	}

	// publish Crontabs with a related Secret (origin: service)
	// RelatedResourceCleanup is NOT set, so it defaults to false
	t.Log("Publishing CRDs…")
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
			Naming: &syncagentv1alpha1.ResourceNaming{
				Name:      "{{ .Object.metadata.name }}",
				Namespace: "synced-{{ .Object.metadata.namespace }}",
			},
			Projection: &syncagentv1alpha1.ResourceProjection{
				Group: "kcp.example.com",
			},
			Related: []syncagentv1alpha1.RelatedResourceSpec{
				{
					Identifier: "credentials",
					Origin:     syncagentv1alpha1.RelatedResourceOriginService,
					Kind:       "Secret",
					Object: syncagentv1alpha1.RelatedResourceObject{
						RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
							Template: &syncagentv1alpha1.TemplateExpression{
								Template: "my-credentials",
							},
						},
					},
					Cleanup: true,
					Watch: &syncagentv1alpha1.RelatedResourceWatch{
						BySelector: &metav1.LabelSelector{
							MatchLabels: dummyLabels,
						},
					},
				},
			},
		},
	}

	if err := envtestClient.Create(ctx, prCrontabs); err != nil {
		t.Fatalf("Failed to create PublishedResource: %v", err)
	}

	// start the agent
	utils.RunAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, apiExportName, "")

	// wait until the API is available
	kcpClusterClient := utils.GetKcpAdminClusterClient(t)

	teamClusterPath := logicalcluster.NewPath("root").Join(orgWorkspace).Join("team-1")
	teamClient := kcpClusterClient.Cluster(teamClusterPath)

	utils.WaitForBoundAPI(t, ctx, teamClient, schema.GroupVersionKind{
		Group:   "kcp.example.com",
		Version: "v1",
		Kind:    "CronTab",
	})

	// Create a CronTab in kcp
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

	crontab.SetLabels(dummyLabels)

	if err := teamClient.Create(ctx, crontab); err != nil {
		t.Fatalf("Failed to create CronTab in kcp: %v", err)
	}

	// Wait for the CronTab to be synced down to the service cluster
	// This also ensures the namespace is created
	t.Log("Waiting for CronTab to be synced to service cluster…")
	serviceCrontab := &unstructured.Unstructured{}
	serviceCrontab.SetAPIVersion("example.com/v1")
	serviceCrontab.SetKind("CronTab")

	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, false, func(ctx context.Context) (done bool, err error) {
		return envtestClient.Get(ctx, types.NamespacedName{Namespace: "synced-default", Name: "my-crontab"}, serviceCrontab) == nil, nil
	})
	if err != nil {
		t.Fatalf("CronTab was not synced to service cluster: %v", err)
	}

	t.Log("CronTab synced to service cluster")

	// Create the related Secret on the service cluster
	t.Log("Creating credential Secret on service cluster…")
	serviceSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-credentials",
			Namespace: "synced-default",
		},
		Data: map[string][]byte{
			"password": []byte("hunter2"),
		},
		Type: corev1.SecretTypeOpaque,
	}

	if err := envtestClient.Create(ctx, serviceSecret); err != nil {
		t.Fatalf("Failed to create Secret on service cluster: %v", err)
	}

	// Wait for the Secret to be synced up to kcp
	t.Log("Waiting for Secret to be synced to kcp…")
	kcpSecret := &corev1.Secret{}
	err = wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, false, func(ctx context.Context) (done bool, err error) {
		return teamClient.Get(ctx, types.NamespacedName{Name: "my-credentials", Namespace: "default"}, kcpSecret) == nil, nil
	})
	if err != nil {
		t.Fatalf("Secret was not synced to kcp: %v", err)
	}

	t.Log("Secret successfully synced to kcp")

	// Delete the primary CronTab
	t.Log("Deleting CronTab in kcp…")
	if err := teamClient.Delete(ctx, crontab); err != nil {
		t.Fatalf("Failed to delete CronTab: %v", err)
	}

	// Wait for CronTab to be fully deleted
	err = wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, false, func(ctx context.Context) (done bool, err error) {
		err = teamClient.Get(ctx, ctrlruntimeclient.ObjectKeyFromObject(crontab), crontab)
		return apierrors.IsNotFound(err), nil
	})
	if err != nil {
		t.Fatalf("CronTab was not deleted: %v", err)
	}

	t.Log("CronTab deleted successfully")

	// Verify that the related resources are deleted on the destination side.

	t.Log("Verifying that Secret in kcp is deleted…")
	err = teamClient.Get(ctx, types.NamespacedName{Name: "my-credentials", Namespace: "default"}, kcpSecret)
	if err == nil {
		t.Error("Secret in kcp was preserved, but should have been deleted")
	} else if !apierrors.IsNotFound(err) {
		t.Errorf("Failed to get Secret in kcp: %v", err)
	}

	t.Log("Verifying that Secret on service cluster remains…")
	err = envtestClient.Get(ctx, types.NamespacedName{Name: "my-credentials", Namespace: "synced-default"}, serviceSecret)
	if err != nil {
		if apierrors.IsNotFound(err) {
			t.Error("Secret on service cluster was deleted, but should have been preserved")
		}
		t.Errorf("Failed to get Secret on service cluster: %v", err)
	}
}
