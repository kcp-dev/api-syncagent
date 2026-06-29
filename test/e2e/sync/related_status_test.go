//go:build e2e

/*
Copyright 2026 The KCP Authors.

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
	crds "github.com/kcp-dev/api-syncagent/test/crds/example/v1"
	"github.com/kcp-dev/api-syncagent/test/utils"

	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrlruntime "sigs.k8s.io/controller-runtime"
)

const subnetAPIVersion = "aws.example.com/v1"
const subnetKind = "Subnet"

func makeSubnet(name, namespace string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(subnetAPIVersion)
	obj.SetKind(subnetKind)
	obj.SetName(name)
	obj.SetNamespace(namespace)
	return obj
}

// TestSyncRelatedObjectStatusFromKcp verifies that when syncStatus: true is set on a related
// resource with origin: kcp, the status is propagated from kcp to the service cluster.
func TestSyncRelatedObjectStatusFromKcp(t *testing.T) {
	const apiExportName = "kcp.example.com"

	ctrlruntime.SetLogger(logr.Discard())

	ctx := t.Context()

	orgKubconfig := utils.CreateOrganization(t, ctx, "related-status-from-kcp", apiExportName)

	envtestKubeconfig, envtestClient, _ := utils.RunEnvtest(t, []string{
		"test/crds/crontab.yaml",
		"test/crds/subnet.yaml",
	})

	// Publish Subnets (with sync disabled) so the Subnet schema is available in kcp.
	prSubnets := &syncagentv1alpha1.PublishedResource{
		ObjectMeta: metav1.ObjectMeta{Name: "publish-subnets"},
		Spec: syncagentv1alpha1.PublishedResourceSpec{
			Resource: syncagentv1alpha1.SourceResourceDescriptor{
				APIGroup: "aws.example.com",
				Version:  "v1",
				Kind:     "Subnet",
			},
			Projection: &syncagentv1alpha1.ResourceProjection{
				Group: "kcp.example.com",
			},
			Synchronization: &syncagentv1alpha1.SynchronizationSpec{Enabled: false},
		},
	}
	if err := envtestClient.Create(ctx, prSubnets); err != nil {
		t.Fatalf("Failed to create Subnet PublishedResource: %v", err)
	}

	// Publish CronTabs with a related Subnet (origin: kcp, syncStatus: true).
	prCrontabs := &syncagentv1alpha1.PublishedResource{
		ObjectMeta: metav1.ObjectMeta{Name: "publish-crontabs"},
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
			Projection: &syncagentv1alpha1.ResourceProjection{Group: "kcp.example.com"},
			Related: []syncagentv1alpha1.RelatedResourceSpec{
				{
					Identifier: "subnet",
					Origin:     syncagentv1alpha1.RelatedResourceOriginKcp,
					Group:      "kcp.example.com",
					Version:    "v1",
					Resource:   "subnets",
					Projection: &syncagentv1alpha1.RelatedResourceProjection{
						Group: "aws.example.com",
					},
					SyncStatus: true,
					Object: syncagentv1alpha1.RelatedResourceObject{
						RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
							Template: &syncagentv1alpha1.TemplateExpression{
								Template: "my-subnet",
							},
						},
					},
				},
			},
		},
	}
	if err := envtestClient.Create(ctx, prCrontabs); err != nil {
		t.Fatalf("Failed to create CronTab PublishedResource: %v", err)
	}

	utils.RunAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, apiExportName, "")

	kcpClusterClient := utils.GetKcpAdminClusterClient(t)
	teamClusterPath := logicalcluster.NewPath("root").Join("related-status-from-kcp").Join("team-1")
	teamClient := kcpClusterClient.Cluster(teamClusterPath)

	utils.WaitForBoundAPI(t, ctx, teamClient, schema.GroupVersionKind{
		Group:   apiExportName,
		Version: "v1",
		Kind:    "CronTab",
	})
	utils.WaitForBoundAPI(t, ctx, teamClient, schema.GroupVersionKind{
		Group:   apiExportName,
		Version: "v1",
		Kind:    "Subnet",
	})

	// Create the Subnet in kcp first (origin: kcp means the user creates it there) and set its
	// status before creating the CronTab. This ensures the status is already present when the
	// agent's second reconciliation cycle (after creating the service cluster copy) runs, so
	// syncObjectStatusForward can see it without needing a Watch trigger.
	kcpSubnet := makeSubnet("my-subnet", "default")
	kcpSubnet.SetAPIVersion("kcp.example.com/v1")
	kcpSubnet.SetKind("Subnet")
	if err := teamClient.Create(ctx, kcpSubnet); err != nil {
		t.Fatalf("Failed to create Subnet in kcp: %v", err)
	}

	// Simulate a controller in kcp setting the subnet's status (e.g. the provisioned subnet ID).
	if err := unstructured.SetNestedField(kcpSubnet.Object, "subnet-kcp-12345", "status", "id"); err != nil {
		t.Fatalf("Failed to set status.id: %v", err)
	}
	if err := teamClient.Status().Update(ctx, kcpSubnet); err != nil {
		t.Fatalf("Failed to update Subnet status in kcp: %v", err)
	}

	// Create the primary CronTab in kcp after the Subnet is ready.
	crontab := utils.ToUnstructured(t, &crds.Crontab{
		TypeMeta:   metav1.TypeMeta{APIVersion: "kcp.example.com/v1", Kind: "CronTab"},
		ObjectMeta: metav1.ObjectMeta{Name: "my-crontab", Namespace: "default"},
		Spec:       crds.CrontabSpec{CronSpec: "* * *", Image: "ubuntu:latest"},
	})
	if err := teamClient.Create(ctx, crontab); err != nil {
		t.Fatalf("Failed to create CronTab: %v", err)
	}

	// Wait for the Subnet copy to appear in the service cluster.
	t.Log("Waiting for Subnet copy to appear in the service cluster…")
	serviceSubnet := makeSubnet("my-subnet", "synced-default")
	serviceSubnet.SetAPIVersion(subnetAPIVersion)
	serviceSubnet.SetKind(subnetKind)

	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 3*time.Minute, false, func(ctx context.Context) (bool, error) {
		return envtestClient.Get(ctx, types.NamespacedName{Name: "my-subnet", Namespace: "synced-default"}, serviceSubnet) == nil, nil
	})
	if err != nil {
		t.Fatalf("Subnet copy never appeared in service cluster: %v", err)
	}

	// Verify the status was forwarded from kcp to the service cluster.
	t.Log("Waiting for Subnet status to be synced to service cluster…")
	err = wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 3*time.Minute, false, func(ctx context.Context) (bool, error) {
		if err := envtestClient.Get(ctx, types.NamespacedName{Name: "my-subnet", Namespace: "synced-default"}, serviceSubnet); err != nil {
			return false, nil
		}

		id, _, _ := unstructured.NestedString(serviceSubnet.Object, "status", "id")
		return id == "subnet-kcp-12345", nil
	})
	if err != nil {
		id, _, _ := unstructured.NestedString(serviceSubnet.Object, "status", "id")
		t.Fatalf("Subnet status was not synced to service cluster (got status.id=%q, want %q)", id, "subnet-kcp-12345")
	}

	// Verify that updating status on the service cluster does NOT propagate back to kcp
	// (there is no reverse sync for origin:kcp + syncStatus).
	serviceSubnet2 := makeSubnet("my-subnet", "synced-default")
	serviceSubnet2.SetAPIVersion(subnetAPIVersion)
	serviceSubnet2.SetKind(subnetKind)
	if err := envtestClient.Get(ctx, types.NamespacedName{Name: "my-subnet", Namespace: "synced-default"}, serviceSubnet2); err != nil {
		t.Fatalf("Failed to re-fetch service cluster Subnet: %v", err)
	}
	if err := unstructured.SetNestedField(serviceSubnet2.Object, "subnet-local-override", "status", "id"); err != nil {
		t.Fatalf("Failed to set status.id: %v", err)
	}
	if err := envtestClient.Status().Update(ctx, serviceSubnet2); err != nil {
		t.Fatalf("Failed to update service cluster Subnet status: %v", err)
	}

	// Briefly poll kcp to confirm its status was not overwritten.
	kcpSubnetCheck := makeSubnet("my-subnet", "default")
	kcpSubnetCheck.SetAPIVersion("kcp.example.com/v1")
	kcpSubnetCheck.SetKind("Subnet")
	if err := teamClient.Get(ctx, types.NamespacedName{Name: "my-subnet", Namespace: "default"}, kcpSubnetCheck); err != nil {
		t.Fatalf("Failed to get kcp Subnet: %v", err)
	}
	kcpID, _, _ := unstructured.NestedString(kcpSubnetCheck.Object, "status", "id")
	if kcpID != "subnet-kcp-12345" {
		t.Fatalf("kcp Subnet status was unexpectedly modified (got %q, want %q)", kcpID, "subnet-kcp-12345")
	}
}

// TestSyncRelatedObjectStatusFromService verifies that when syncStatus: true is set on a related
// resource with origin: service, the status is propagated from the service cluster to kcp.
func TestSyncRelatedObjectStatusFromService(t *testing.T) {
	const apiExportName = "kcp.example.com"

	ctrlruntime.SetLogger(logr.Discard())

	ctx := t.Context()

	orgKubconfig := utils.CreateOrganization(t, ctx, "related-status-from-service", apiExportName)

	envtestKubeconfig, envtestClient, _ := utils.RunEnvtest(t, []string{
		"test/crds/crontab.yaml",
		"test/crds/subnet.yaml",
	})

	// Publish Subnets (with sync disabled) so the Subnet schema is available in kcp
	// and the agent can create kcp-side copies when syncing service-origin related resources.
	prSubnets := &syncagentv1alpha1.PublishedResource{
		ObjectMeta: metav1.ObjectMeta{Name: "publish-subnets"},
		Spec: syncagentv1alpha1.PublishedResourceSpec{
			Resource: syncagentv1alpha1.SourceResourceDescriptor{
				APIGroup: "aws.example.com",
				Version:  "v1",
				Kind:     "Subnet",
			},
			Projection: &syncagentv1alpha1.ResourceProjection{
				Group: "kcp.example.com",
			},
			Synchronization: &syncagentv1alpha1.SynchronizationSpec{Enabled: false},
		},
	}
	if err := envtestClient.Create(ctx, prSubnets); err != nil {
		t.Fatalf("Failed to create Subnet PublishedResource: %v", err)
	}

	// Publish CronTabs with a related Subnet (origin: service, syncStatus: true).
	prCrontabs := &syncagentv1alpha1.PublishedResource{
		ObjectMeta: metav1.ObjectMeta{Name: "publish-crontabs"},
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
			Projection: &syncagentv1alpha1.ResourceProjection{Group: "kcp.example.com"},
			Related: []syncagentv1alpha1.RelatedResourceSpec{
				{
					Identifier: "subnet",
					Origin:     syncagentv1alpha1.RelatedResourceOriginService,
					Group:      "aws.example.com",
					Version:    "v1",
					Resource:   "subnets",
					Projection: &syncagentv1alpha1.RelatedResourceProjection{
						Group: "kcp.example.com",
					},
					SyncStatus: true,
					// Watch triggers CronTab reconciliation whenever a service cluster Subnet
					// changes. This also registers a Subnet informer in the local cache, so
					// syncObjectStatusForward can read the up-to-date status from the cache.
					Watch: &syncagentv1alpha1.RelatedResourceWatch{
						BySelector: &metav1.LabelSelector{},
					},
					Object: syncagentv1alpha1.RelatedResourceObject{
						RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
							Template: &syncagentv1alpha1.TemplateExpression{
								Template: "my-subnet",
							},
						},
					},
				},
			},
		},
	}
	if err := envtestClient.Create(ctx, prCrontabs); err != nil {
		t.Fatalf("Failed to create CronTab PublishedResource: %v", err)
	}

	utils.RunAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, apiExportName, "")

	kcpClusterClient := utils.GetKcpAdminClusterClient(t)
	teamClusterPath := logicalcluster.NewPath("root").Join("related-status-from-service").Join("team-1")
	teamClient := kcpClusterClient.Cluster(teamClusterPath)

	utils.WaitForBoundAPI(t, ctx, teamClient, schema.GroupVersionKind{
		Group:   apiExportName,
		Version: "v1",
		Kind:    "CronTab",
	})

	// Create the primary CronTab in kcp.
	crontab := utils.ToUnstructured(t, &crds.Crontab{
		TypeMeta:   metav1.TypeMeta{APIVersion: "kcp.example.com/v1", Kind: "CronTab"},
		ObjectMeta: metav1.ObjectMeta{Name: "my-crontab", Namespace: "default"},
		Spec:       crds.CrontabSpec{CronSpec: "* * *", Image: "ubuntu:latest"},
	})
	if err := teamClient.Create(ctx, crontab); err != nil {
		t.Fatalf("Failed to create CronTab: %v", err)
	}

	// Simulate a service-cluster operator creating the Subnet and setting its status.
	ensureNamespace(t, ctx, envtestClient, "synced-default")

	serviceSubnet := makeSubnet("my-subnet", "synced-default")
	if err := envtestClient.Create(ctx, serviceSubnet); err != nil {
		t.Fatalf("Failed to create Subnet in service cluster: %v", err)
	}

	// Set the status (e.g. the real AWS subnet ID returned after provisioning).
	if err := unstructured.SetNestedField(serviceSubnet.Object, "subnet-svc-99999", "status", "id"); err != nil {
		t.Fatalf("Failed to set status.id: %v", err)
	}
	if err := envtestClient.Status().Update(ctx, serviceSubnet); err != nil {
		t.Fatalf("Failed to update Subnet status: %v", err)
	}

	// Wait for the kcp copy to appear.
	t.Log("Waiting for Subnet copy to appear in kcp…")
	kcpSubnet := &unstructured.Unstructured{}
	kcpSubnet.SetAPIVersion("kcp.example.com/v1")
	kcpSubnet.SetKind("Subnet")

	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 3*time.Minute, false, func(ctx context.Context) (bool, error) {
		return teamClient.Get(ctx, types.NamespacedName{Name: "my-subnet", Namespace: "default"}, kcpSubnet) == nil, nil
	})
	if err != nil {
		t.Fatalf("Subnet copy never appeared in kcp: %v", err)
	}

	// Wait for the status to be synced to kcp.
	t.Log("Waiting for Subnet status to be synced to kcp…")
	err = wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 3*time.Minute, false, func(ctx context.Context) (bool, error) {
		if err := teamClient.Get(ctx, types.NamespacedName{Name: "my-subnet", Namespace: "default"}, kcpSubnet); err != nil {
			return false, nil
		}

		id, _, _ := unstructured.NestedString(kcpSubnet.Object, "status", "id")
		return id == "subnet-svc-99999", nil
	})
	if err != nil {
		id, _, _ := unstructured.NestedString(kcpSubnet.Object, "status", "id")
		t.Fatalf("Subnet status was not synced to kcp (got status.id=%q, want %q)", id, "subnet-svc-99999")
	}
}

// TestSyncRelatedObjectStatusNotSyncedByDefault verifies that when syncStatus is not set (the
// default), status changes on the origin side do not appear on the destination copy.
func TestSyncRelatedObjectStatusNotSyncedByDefault(t *testing.T) {
	const apiExportName = "kcp.example.com"

	ctrlruntime.SetLogger(logr.Discard())

	ctx := t.Context()

	orgKubconfig := utils.CreateOrganization(t, ctx, "related-status-not-synced", apiExportName)

	envtestKubeconfig, envtestClient, _ := utils.RunEnvtest(t, []string{
		"test/crds/crontab.yaml",
		"test/crds/subnet.yaml",
	})

	prSubnets := &syncagentv1alpha1.PublishedResource{
		ObjectMeta: metav1.ObjectMeta{Name: "publish-subnets"},
		Spec: syncagentv1alpha1.PublishedResourceSpec{
			Resource: syncagentv1alpha1.SourceResourceDescriptor{
				APIGroup: "aws.example.com",
				Version:  "v1",
				Kind:     "Subnet",
			},
			Projection: &syncagentv1alpha1.ResourceProjection{
				Group: "kcp.example.com",
			},
			Synchronization: &syncagentv1alpha1.SynchronizationSpec{Enabled: false},
		},
	}
	if err := envtestClient.Create(ctx, prSubnets); err != nil {
		t.Fatalf("Failed to create Subnet PublishedResource: %v", err)
	}

	// SyncStatus is intentionally NOT set here.
	prCrontabs := &syncagentv1alpha1.PublishedResource{
		ObjectMeta: metav1.ObjectMeta{Name: "publish-crontabs"},
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
			Projection: &syncagentv1alpha1.ResourceProjection{Group: "kcp.example.com"},
			Related: []syncagentv1alpha1.RelatedResourceSpec{
				{
					Identifier: "subnet",
					Origin:     syncagentv1alpha1.RelatedResourceOriginKcp,
					Group:      "kcp.example.com",
					Version:    "v1",
					Resource:   "subnets",
					Projection: &syncagentv1alpha1.RelatedResourceProjection{
						Group: "aws.example.com",
					},
					// SyncStatus: false (default)
					Object: syncagentv1alpha1.RelatedResourceObject{
						RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
							Template: &syncagentv1alpha1.TemplateExpression{
								Template: "my-subnet",
							},
						},
					},
				},
			},
		},
	}
	if err := envtestClient.Create(ctx, prCrontabs); err != nil {
		t.Fatalf("Failed to create CronTab PublishedResource: %v", err)
	}

	utils.RunAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, apiExportName, "")

	kcpClusterClient := utils.GetKcpAdminClusterClient(t)
	teamClusterPath := logicalcluster.NewPath("root").Join("related-status-not-synced").Join("team-1")
	teamClient := kcpClusterClient.Cluster(teamClusterPath)

	utils.WaitForBoundAPI(t, ctx, teamClient, schema.GroupVersionKind{
		Group:   apiExportName,
		Version: "v1",
		Kind:    "CronTab",
	})
	utils.WaitForBoundAPI(t, ctx, teamClient, schema.GroupVersionKind{
		Group:   apiExportName,
		Version: "v1",
		Kind:    "Subnet",
	})

	// Create primary CronTab in kcp.
	crontab := utils.ToUnstructured(t, &crds.Crontab{
		TypeMeta:   metav1.TypeMeta{APIVersion: "kcp.example.com/v1", Kind: "CronTab"},
		ObjectMeta: metav1.ObjectMeta{Name: "my-crontab", Namespace: "default"},
		Spec:       crds.CrontabSpec{CronSpec: "* * *", Image: "ubuntu:latest"},
	})
	if err := teamClient.Create(ctx, crontab); err != nil {
		t.Fatalf("Failed to create CronTab: %v", err)
	}

	// Create the Subnet in kcp and set its status.
	kcpSubnet := makeSubnet("my-subnet", "default")
	kcpSubnet.SetAPIVersion("kcp.example.com/v1")
	kcpSubnet.SetKind("Subnet")
	if err := teamClient.Create(ctx, kcpSubnet); err != nil {
		t.Fatalf("Failed to create Subnet in kcp: %v", err)
	}
	if err := unstructured.SetNestedField(kcpSubnet.Object, "subnet-should-not-appear", "status", "id"); err != nil {
		t.Fatalf("Failed to set status.id: %v", err)
	}
	if err := teamClient.Status().Update(ctx, kcpSubnet); err != nil {
		t.Fatalf("Failed to update Subnet status in kcp: %v", err)
	}

	// Wait for the service cluster copy to appear.
	t.Log("Waiting for Subnet copy to appear in service cluster…")
	serviceSubnet := makeSubnet("my-subnet", "synced-default")
	serviceSubnet.SetAPIVersion(subnetAPIVersion)
	serviceSubnet.SetKind(subnetKind)
	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 3*time.Minute, false, func(ctx context.Context) (bool, error) {
		return envtestClient.Get(ctx, types.NamespacedName{Name: "my-subnet", Namespace: "synced-default"}, serviceSubnet) == nil, nil
	})
	if err != nil {
		t.Fatalf("Subnet copy never appeared in service cluster: %v", err)
	}

	// Give the agent a moment to settle and confirm status is not present.
	// We wait a short time for any potential erroneous status sync to occur.
	var finalServiceSubnet unstructured.Unstructured
	finalServiceSubnet.SetAPIVersion(subnetAPIVersion)
	finalServiceSubnet.SetKind(subnetKind)

	// Poll briefly to check that no status appears.
	statusSeen := false
	_ = wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 5*time.Second, false, func(ctx context.Context) (bool, error) {
		if err := envtestClient.Get(ctx, types.NamespacedName{Name: "my-subnet", Namespace: "synced-default"}, &finalServiceSubnet); err != nil {
			return false, nil
		}
		id, exists, _ := unstructured.NestedString(finalServiceSubnet.Object, "status", "id")
		if exists && id != "" {
			statusSeen = true
			return true, nil
		}
		return false, nil
	})

	if statusSeen {
		id, _, _ := unstructured.NestedString(finalServiceSubnet.Object, "status", "id")
		t.Fatalf("Subnet status was unexpectedly synced to service cluster (got status.id=%q)", id)
	}
}
