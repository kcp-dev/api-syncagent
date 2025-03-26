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
	"github.com/kcp-dev/api-syncagent/test/utils"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrlruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/kontext"
)

func TestSyncSecretBackToKcp(t *testing.T) {
	const (
		apiExportName = "kcp.example.com"
		orgWorkspace  = "sync-related-secret-to-kcp"
	)

	ctx := context.Background()
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
				Name:      "$remoteName",
				Namespace: "synced-$remoteNamespace",
			},
			Related: []syncagentv1alpha1.RelatedResourceSpec{{
				Identifier: "credentials",
				Origin:     "service",
				Kind:       "Secret",
				Reference: syncagentv1alpha1.RelatedResourceReference{
					Name: syncagentv1alpha1.ResourceLocator{
						Path: "metadata.name", // irrelevant
						Regex: &syncagentv1alpha1.RegexResourceLocator{
							Replacement: "my-credentials",
						},
					},
				},
			}},
		},
	}

	if err := envtestClient.Create(ctx, prCrontabs); err != nil {
		t.Fatalf("Failed to create PublishedResource: %v", err)
	}

	// start the agent in the background to update the APIExport with the CronTabs API
	utils.RunAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, apiExportName)

	// wait until the API is available
	teamCtx := kontext.WithCluster(ctx, logicalcluster.Name(fmt.Sprintf("root:%s:team-1", orgWorkspace)))
	kcpClient := utils.GetKcpAdminClusterClient(t)
	utils.WaitForBoundAPI(t, teamCtx, kcpClient, schema.GroupVersionResource{
		Group:    apiExportName,
		Version:  "v1",
		Resource: "crontabs",
	})

	// create a Crontab object in a team workspace
	t.Log("Creating CronTab in kcp…")
	crontab := yamlToUnstructured(t, `
apiVersion: kcp.example.com/v1
kind: CronTab
metadata:
  namespace: default
  name: my-crontab
spec:
  cronSpec: '* * *'
  image: ubuntu:latest
`)

	if err := kcpClient.Create(teamCtx, crontab); err != nil {
		t.Fatalf("Failed to create CronTab in kcp: %v", err)
	}

	// fake operator: create a credential Secret
	t.Log("Creating credential Secret in service cluster…")
	namespace := &corev1.Namespace{}
	namespace.Name = "synced-default"

	if err := envtestClient.Create(ctx, namespace); err != nil {
		t.Fatalf("Failed to create namespace in kcp: %v", err)
	}

	credentials := &corev1.Secret{}
	credentials.Name = "my-credentials"
	credentials.Namespace = namespace.Name
	credentials.Labels = map[string]string{
		"hello": "world",
	}
	credentials.Data = map[string][]byte{
		"password": []byte("hunter2"),
	}

	if err := envtestClient.Create(ctx, credentials); err != nil {
		t.Fatalf("Failed to create Secret in service cluster: %v", err)
	}

	// wait for the agent to sync the object down into the service cluster and
	// the Secret back up to kcp
	t.Logf("Wait for CronTab/Secret to be synced…")
	copy := &unstructured.Unstructured{}
	copy.SetAPIVersion("example.com/v1")
	copy.SetKind("CronTab")

	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, false, func(ctx context.Context) (done bool, err error) {
		copyKey := types.NamespacedName{Namespace: "synced-default", Name: "my-crontab"}
		return envtestClient.Get(ctx, copyKey, copy) == nil, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for CronTab to be synced down: %v", err)
	}

	copySecret := &corev1.Secret{}

	err = wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, false, func(ctx context.Context) (done bool, err error) {
		copyKey := types.NamespacedName{Namespace: "default", Name: "my-credentials"}
		return kcpClient.Get(teamCtx, copyKey, copySecret) == nil, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for Secret to be synced up: %v", err)
	}

	// ensure the secret in kcp does not have any sync-related metadata
	maps.DeleteFunc(copySecret.Labels, func(k, v string) bool {
		return strings.HasPrefix(k, "claimed.internal.apis.kcp.io/")
	})

	if changes := diff.ObjectDiff(credentials.Labels, copySecret.Labels); changes != "" {
		t.Errorf("Secret in kcp has unexpected labels:\n%s", changes)
	}

	delete(copySecret.Annotations, "kcp.io/cluster")
	if len(copySecret.Annotations) == 0 {
		copySecret.Annotations = nil
	}

	if changes := diff.ObjectDiff(credentials.Annotations, copySecret.Annotations); changes != "" {
		t.Errorf("Secret in kcp has unexpected annotations:\n%s", changes)
	}
}
