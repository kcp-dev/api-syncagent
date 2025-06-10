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
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/kcp-dev/logicalcluster/v3"

	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"
	"github.com/kcp-dev/api-syncagent/test/utils"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrlruntime "sigs.k8s.io/controller-runtime"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func TestSyncSimpleObject(t *testing.T) {
	const (
		apiExportName = "kcp.example.com"
		kcpGroupName  = "kcp.example.com"
		orgWorkspace  = "sync-simple"
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

	// start the agent in the background to update the APIExport with the CronTabs API
	utils.RunAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, apiExportName)

	// wait until the API is available
	kcpClusterClient := utils.GetKcpAdminClusterClient(t)

	teamClusterPath := logicalcluster.NewPath("root").Join(orgWorkspace).Join("team-1")
	teamClient := kcpClusterClient.Cluster(teamClusterPath)

	utils.WaitForBoundAPI(t, ctx, teamClient, schema.GroupVersionResource{
		Group:    kcpGroupName,
		Version:  "v1",
		Resource: "crontabs",
	})

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

func TestSyncSimpleObjectOldNaming(t *testing.T) {
	const (
		apiExportName = "kcp.example.com"
		kcpGroupName  = "kcp.example.com"
		orgWorkspace  = "sync-simple-deprecated"
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
				Name:      "$remoteName",
				Namespace: "synced-$remoteNamespace",
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
	utils.RunAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, apiExportName)

	// wait until the API is available
	kcpClusterClient := utils.GetKcpAdminClusterClient(t)

	teamClusterPath := logicalcluster.NewPath("root").Join(orgWorkspace).Join("team-1")
	teamClient := kcpClusterClient.Cluster(teamClusterPath)

	utils.WaitForBoundAPI(t, ctx, teamClient, schema.GroupVersionResource{
		Group:    kcpGroupName,
		Version:  "v1",
		Resource: "crontabs",
	})

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

func TestSyncWithDefaultNamingRules(t *testing.T) {
	const (
		apiExportName = "kcp.example.com"
		orgWorkspace  = "sync-default-naming-rules"
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
			Projection: &syncagentv1alpha1.ResourceProjection{
				Group: "kcp.example.com",
			},
		},
	}

	if err := envtestClient.Create(ctx, prCrontabs); err != nil {
		t.Fatalf("Failed to create PublishedResource: %v", err)
	}

	// start the agent in the background to update the APIExport with the CronTabs API
	utils.RunAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, apiExportName)

	// wait until the API is available
	kcpClusterClient := utils.GetKcpAdminClusterClient(t)

	crontabsGVR := schema.GroupVersionResource{
		Group:    "kcp.example.com",
		Version:  "v1",
		Resource: "crontabs",
	}

	// create a Crontab object in each team workspace, importantly using the same name and
	// namespace in both workspaces
	crontabYAML := `
apiVersion: kcp.example.com/v1
kind: CronTab
metadata:
  namespace: default
  name: my-crontab
spec:
  cronSpec: '* * *'
  image: ubuntu:latest
`

	t.Log("Creating CronTabs in kcp…")
	for _, team := range []string{"team-1", "team-2"} {
		teamClusterPath := logicalcluster.NewPath("root").Join(orgWorkspace).Join(team)
		teamClient := kcpClusterClient.Cluster(teamClusterPath)

		utils.WaitForBoundAPI(t, ctx, teamClient, crontabsGVR)

		if err := teamClient.Create(ctx, yamlToUnstructured(t, crontabYAML)); err != nil {
			t.Fatalf("Failed to create %s's CronTab in kcp: %v", team, err)
		}
	}

	// wait for the agent to sync both objects done, ensuring that we actually end
	// up with 2 distinct objects

	t.Logf("Wait for CronTabs to be synced…")
	crontabs := &unstructured.UnstructuredList{}
	crontabs.SetAPIVersion("example.com/v1")
	crontabs.SetKind("CronTabList")

	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, false, func(ctx context.Context) (done bool, err error) {
		err = envtestClient.List(ctx, crontabs)
		if err != nil {
			return false, err
		}

		return len(crontabs.Items) == 2, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for objects to be synced down: %v", err)
	}
}

func TestLocalChangesAreKept(t *testing.T) {
	const (
		apiExportName = "kcp.example.com"
		kcpGroupName  = "kcp.example.com"
		orgWorkspace  = "sync-undo-local-changes"
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

	// start the agent in the background to update the APIExport with the CronTabs API
	utils.RunAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, apiExportName)

	// wait until the API is available
	kcpClusterClient := utils.GetKcpAdminClusterClient(t)

	teamClusterPath := logicalcluster.NewPath("root").Join(orgWorkspace).Join("team-1")
	teamClient := kcpClusterClient.Cluster(teamClusterPath)

	utils.WaitForBoundAPI(t, ctx, teamClient, schema.GroupVersionResource{
		Group:    kcpGroupName,
		Version:  "v1",
		Resource: "crontabs",
	})

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
	copyKey := types.NamespacedName{Namespace: "synced-default", Name: "my-crontab"}

	copy := &unstructured.Unstructured{}
	copy.SetAPIVersion("example.com/v1")
	copy.SetKind("CronTab")

	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, false, func(ctx context.Context) (done bool, err error) {
		return envtestClient.Get(ctx, copyKey, copy) == nil, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for object to be synced down: %v", err)
	}

	// make some changes on the service cluster; this is usually an external operator doing some
	// defaulting, maybe even a mutation webhook
	t.Logf("Modifying local object…")
	newCronSpec := "this-should-not-be-reverted"
	unstructured.SetNestedField(copy.Object, newCronSpec, "spec", "cronSpec")

	if err := envtestClient.Update(ctx, copy); err != nil {
		t.Fatalf("Failed to update synced object in service cluster: %v", err)
	}

	// make some changes in kcp, these should be applied to the local object without overwriting the cronSpec

	// refresh the current object state
	if err := teamClient.Get(ctx, ctrlruntimeclient.ObjectKeyFromObject(crontab), crontab); err != nil {
		t.Fatalf("Failed to create CronTab in kcp: %v", err)
	}

	newImage := "new-value"
	unstructured.SetNestedField(crontab.Object, newImage, "spec", "image")

	t.Logf("Modifying object in kcp…")
	if err := teamClient.Update(ctx, crontab); err != nil {
		t.Fatalf("Failed to update source object in kcp: %v", err)
	}

	// wait for the agent to sync again
	t.Logf("Waiting for the agent to sync again…")
	err = wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, false, func(ctx context.Context) (done bool, err error) {
		if err := envtestClient.Get(ctx, copyKey, copy); err != nil {
			return false, err
		}

		value, existing, err := unstructured.NestedString(copy.Object, "spec", "cronSpec")
		if err != nil {
			return false, err
		}

		if !existing {
			return false, errors.New("field does not exist in object anymore, this should not have happened")
		}

		if value != newCronSpec {
			return false, fmt.Errorf("cronSpec was reverted back to %q, should still be %q", value, newCronSpec)
		}

		value, existing, err = unstructured.NestedString(copy.Object, "spec", "image")
		if err != nil {
			return false, err
		}

		if !existing {
			return false, errors.New("field does not exist in object anymore, this should not have happened")
		}

		return value == newImage, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for object to be synced: %v", err)
	}

	// Now we actually change the cronSpec in kcp, and this change _must_ make it to the service cluster.
	t.Logf("Modify object in kcp again…")

	if err := teamClient.Get(ctx, ctrlruntimeclient.ObjectKeyFromObject(crontab), crontab); err != nil {
		t.Fatalf("Failed to create CronTab in kcp: %v", err)
	}

	kcpNewCronSpec := "users-new-desired-cronspec"
	unstructured.SetNestedField(crontab.Object, kcpNewCronSpec, "spec", "cronSpec")

	if err := teamClient.Update(ctx, crontab); err != nil {
		t.Fatalf("Failed to update source object in kcp: %v", err)
	}

	// wait for the agent to sync again
	t.Logf("Waiting for the agent to sync again…")
	err = wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, false, func(ctx context.Context) (done bool, err error) {
		if err := envtestClient.Get(ctx, copyKey, copy); err != nil {
			return false, err
		}

		value, existing, err := unstructured.NestedString(copy.Object, "spec", "cronSpec")
		if err != nil {
			return false, err
		}

		if !existing {
			return false, errors.New("field does not exist in object anymore, this should not have happened")
		}

		return value == kcpNewCronSpec, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for object to be synced: %v", err)
	}
}

func TestResourceFilter(t *testing.T) {
	const (
		apiExportName = "kcp.example.com"
		kcpGroupName  = "kcp.example.com"
		orgWorkspace  = "sync-resource-filter"
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
			Filter: &syncagentv1alpha1.ResourceFilter{
				Resource: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"include": "me",
					},
				},
			},
		},
	}

	if err := envtestClient.Create(ctx, prCrontabs); err != nil {
		t.Fatalf("Failed to create PublishedResource: %v", err)
	}

	// start the agent in the background to update the APIExport with the CronTabs API
	utils.RunAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, apiExportName)

	// wait until the API is available
	kcpClusterClient := utils.GetKcpAdminClusterClient(t)

	teamClusterPath := logicalcluster.NewPath("root").Join(orgWorkspace).Join("team-1")
	teamClient := kcpClusterClient.Cluster(teamClusterPath)

	utils.WaitForBoundAPI(t, ctx, teamClient, schema.GroupVersionResource{
		Group:    kcpGroupName,
		Version:  "v1",
		Resource: "crontabs",
	})

	// create two Crontab objects in a team workspace
	t.Log("Creating CronTab in kcp…")
	ignoredCrontab := utils.YAMLToUnstructured(t, `
apiVersion: kcp.example.com/v1
kind: CronTab
metadata:
  namespace: default
  name: ignored
spec:
  image: ubuntu:latest
`)

	if err := teamClient.Create(ctx, ignoredCrontab); err != nil {
		t.Fatalf("Failed to create CronTab in kcp: %v", err)
	}

	includedCrontab := utils.YAMLToUnstructured(t, `
apiVersion: kcp.example.com/v1
kind: CronTab
metadata:
  namespace: default
  name: included
  labels:
    include: me
spec:
  image: debian:12
`)

	if err := teamClient.Create(ctx, includedCrontab); err != nil {
		t.Fatalf("Failed to create CronTab in kcp: %v", err)
	}

	// wait for the agent to sync only one of the objects down into the service cluster

	t.Logf("Wait for CronTab to be synced…")
	copy := &unstructured.Unstructured{}
	copy.SetAPIVersion("example.com/v1")
	copy.SetKind("CronTab")

	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, false, func(ctx context.Context) (done bool, err error) {
		copyKey := types.NamespacedName{Namespace: "synced-default", Name: "included"}
		return envtestClient.Get(ctx, copyKey, copy) == nil, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for object to be synced down: %v", err)
	}

	// the only good negative check is to wait for a timeout
	err = wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, false, func(ctx context.Context) (done bool, err error) {
		copyKey := types.NamespacedName{Namespace: "synced-default", Name: "ignored"}
		return envtestClient.Get(ctx, copyKey, copy) == nil, nil
	})
	if err == nil {
		t.Fatal("Expected no ignored object to be found on the service cluster, but did.")
	}
}

func TestSyncingOverlyLongNames(t *testing.T) {
	const (
		apiExportName = "kcp.example.com"
		kcpGroupName  = "kcp.example.com"
		orgWorkspace  = "sync-long-names"
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

	// start the agent in the background to update the APIExport with the CronTabs API
	utils.RunAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, apiExportName)

	// wait until the API is available
	kcpClusterClient := utils.GetKcpAdminClusterClient(t)

	teamClusterPath := logicalcluster.NewPath("root").Join(orgWorkspace).Join("team-1")
	teamClient := kcpClusterClient.Cluster(teamClusterPath)

	utils.WaitForBoundAPI(t, ctx, teamClient, schema.GroupVersionResource{
		Group:    kcpGroupName,
		Version:  "v1",
		Resource: "crontabs",
	})

	// create a namespace and CronTab with extremely long names
	namespace := &corev1.Namespace{}
	namespace.Name = strings.Repeat("yadda", 3) // 250 chars in total

	if err := teamClient.Create(ctx, namespace); err != nil {
		t.Fatalf("Failed to create namespace in kcp: %v", err)
	}

	t.Log("Creating CronTab in kcp…")
	ignoredCrontab := utils.YAMLToUnstructured(t, `
apiVersion: kcp.example.com/v1
kind: CronTab
metadata:
  name: TBD
  namespace: TBD
spec:
  image: ubuntu:latest
`)
	ignoredCrontab.SetNamespace(namespace.Name)
	ignoredCrontab.SetName(strings.Repeat("yotta", 50))

	if err := teamClient.Create(ctx, ignoredCrontab); err != nil {
		t.Fatalf("Failed to create CronTab in kcp: %v", err)
	}

	// wait for the agent to sync the object down into the service cluster

	t.Logf("Wait for CronTab to be synced…")
	copy := &unstructured.Unstructured{}
	copy.SetAPIVersion("example.com/v1")
	copy.SetKind("CronTab")

	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, false, func(ctx context.Context) (done bool, err error) {
		copyKey := types.NamespacedName{Namespace: fmt.Sprintf("synced-%s", namespace.Name), Name: ignoredCrontab.GetName()}
		return envtestClient.Get(ctx, copyKey, copy) == nil, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for object to be synced down: %v", err)
	}
}
