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
	"github.com/kcp-dev/api-syncagent/test/utils"

	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrlruntime "sigs.k8s.io/controller-runtime"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	deletionPropagationTestAnnotation = "syncagent.kcp.io/deletion-propagation-policy"
	deletionPropagationTestForeground = "foreground"
	deletionPropagationTestOrphan     = "orphan"
)

func TestPrimaryDeletionPropagationPolicy(t *testing.T) {
	ctx := t.Context()
	ctrlruntime.SetLogger(logr.Discard())

	const (
		apiExportName = "kcp.example.com"
		orgWorkspace  = "primary-deletion-propagation"
	)
	orgKubconfig := utils.CreateOrganization(t, ctx, orgWorkspace, apiExportName)
	envtestKubeconfig, envtestClient, _ := utils.RunEnvtest(t, []string{"test/crds/crontab.yaml"})

	if err := envtestClient.Create(ctx, deletionPropagationPublishedResource(false)); err != nil {
		t.Fatalf("Failed to create PublishedResource: %v", err)
	}

	utils.RunAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, apiExportName, "")
	teamClient := deletionPropagationTeamClient(t, ctx, orgWorkspace)

	crontab := deletionPropagationCronTab("primary")
	if err := teamClient.Create(ctx, crontab); err != nil {
		t.Fatalf("Failed to create CronTab: %v", err)
	}

	serviceCrontab := waitForServiceCronTab(t, ctx, envtestClient, crontab.GetName())
	setDeletionPropagationAnnotation(t, ctx, teamClient, crontab, deletionPropagationTestForeground)

	if err := teamClient.Delete(
		ctx,
		crontab,
		ctrlruntimeclient.PropagationPolicy(metav1.DeletePropagationBackground),
	); err != nil {
		t.Fatalf("Failed to delete CronTab: %v", err)
	}

	waitForForegroundDeletion(
		t,
		ctx,
		envtestClient,
		serviceCrontab,
	)
}

func TestKcpRelatedCleanupUsesItsOwnDeletionPropagationPolicy(t *testing.T) {
	ctx := t.Context()
	ctrlruntime.SetLogger(logr.Discard())

	const (
		apiExportName = "kcp.example.com"
		orgWorkspace  = "related-deletion-propagation"
	)
	orgKubconfig := utils.CreateOrganization(t, ctx, orgWorkspace, apiExportName)
	envtestKubeconfig, envtestClient, _ := utils.RunEnvtest(t, []string{"test/crds/crontab.yaml"})

	if err := envtestClient.Create(ctx, deletionPropagationPublishedResource(true)); err != nil {
		t.Fatalf("Failed to create PublishedResource: %v", err)
	}

	utils.RunAgent(ctx, t, "bob", orgKubconfig, envtestKubeconfig, apiExportName, "")
	teamClient := deletionPropagationTeamClient(t, ctx, orgWorkspace)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "credentials",
			Namespace: "default",
		},
		StringData: map[string]string{"password": "hunter2"},
	}
	if err := teamClient.Create(ctx, secret); err != nil {
		t.Fatalf("Failed to create related Secret: %v", err)
	}

	crontab := deletionPropagationCronTab("primary")
	if err := teamClient.Create(ctx, crontab); err != nil {
		t.Fatalf("Failed to create CronTab: %v", err)
	}

	waitForServiceCronTab(t, ctx, envtestClient, crontab.GetName())
	serviceSecret := waitForServiceSecret(t, ctx, envtestClient, secret.GetName())

	setDeletionPropagationAnnotation(t, ctx, teamClient, secret, deletionPropagationTestForeground)
	setDeletionPropagationAnnotation(t, ctx, teamClient, crontab, deletionPropagationTestOrphan)

	if err := teamClient.Delete(
		ctx,
		crontab,
		ctrlruntimeclient.PropagationPolicy(metav1.DeletePropagationBackground),
	); err != nil {
		t.Fatalf("Failed to delete CronTab: %v", err)
	}

	// Related cleanup uses the annotation stored on the related kcp source.
	waitForForegroundDeletion(
		t,
		ctx,
		envtestClient,
		serviceSecret,
	)
}

func deletionPropagationPublishedResource(withRelatedSecret bool) *syncagentv1alpha1.PublishedResource {
	pr := &syncagentv1alpha1.PublishedResource{
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
		},
	}

	if withRelatedSecret {
		pr.Spec.Related = []syncagentv1alpha1.RelatedResourceSpec{
			{
				Identifier: "credentials",
				Origin:     syncagentv1alpha1.RelatedResourceOriginKcp,
				Kind:       "Secret",
				Object: syncagentv1alpha1.RelatedResourceObject{
					RelatedResourceObjectSpec: syncagentv1alpha1.RelatedResourceObjectSpec{
						Template: &syncagentv1alpha1.TemplateExpression{Template: "credentials"},
					},
				},
			},
		}
	}

	return pr
}

func deletionPropagationTeamClient(
	t *testing.T,
	ctx context.Context,
	orgWorkspace string,
) ctrlruntimeclient.Client {
	t.Helper()

	teamClient := utils.GetKcpAdminClusterClient(t).Cluster(
		logicalcluster.NewPath("root").Join(orgWorkspace).Join("team-1"),
	)
	utils.WaitForBoundAPI(t, ctx, teamClient, schema.GroupVersionKind{
		Group: "kcp.example.com", Version: "v1", Kind: "CronTab",
	})

	return teamClient
}

func deletionPropagationCronTab(name string) *unstructured.Unstructured {
	crontab := &unstructured.Unstructured{}
	crontab.SetAPIVersion("kcp.example.com/v1")
	crontab.SetKind("CronTab")
	crontab.SetNamespace("default")
	crontab.SetName(name)
	crontab.Object["spec"] = map[string]any{"cronSpec": "* * *"}

	return crontab
}

func setDeletionPropagationAnnotation(
	t *testing.T,
	ctx context.Context,
	client ctrlruntimeclient.Client,
	obj ctrlruntimeclient.Object,
	policy string,
) {
	t.Helper()

	current := obj.DeepCopyObject().(ctrlruntimeclient.Object)
	if err := client.Get(ctx, ctrlruntimeclient.ObjectKeyFromObject(obj), current); err != nil {
		t.Fatalf("Failed to get object before annotating it: %v", err)
	}
	annotations := current.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[deletionPropagationTestAnnotation] = policy
	current.SetAnnotations(annotations)
	if err := client.Update(ctx, current); err != nil {
		t.Fatalf("Failed to set deletion propagation annotation: %v", err)
	}
}

func waitForServiceCronTab(
	t *testing.T,
	ctx context.Context,
	client ctrlruntimeclient.Client,
	name string,
) *unstructured.Unstructured {
	t.Helper()

	crontab := &unstructured.Unstructured{}
	crontab.SetAPIVersion("example.com/v1")
	crontab.SetKind("CronTab")
	if err := wait.PollUntilContextTimeout(
		ctx,
		500*time.Millisecond,
		30*time.Second,
		false,
		func(ctx context.Context) (bool, error) {
			return client.Get(
				ctx,
				types.NamespacedName{Namespace: "synced-default", Name: name},
				crontab,
			) == nil, nil
		},
	); err != nil {
		t.Fatalf("CronTab was not synced to the service cluster: %v", err)
	}

	return crontab
}

func waitForServiceSecret(
	t *testing.T,
	ctx context.Context,
	client ctrlruntimeclient.Client,
	name string,
) *corev1.Secret {
	t.Helper()

	secret := &corev1.Secret{}
	if err := wait.PollUntilContextTimeout(
		ctx,
		500*time.Millisecond,
		30*time.Second,
		false,
		func(ctx context.Context) (bool, error) {
			return client.Get(
				ctx,
				types.NamespacedName{Namespace: "synced-default", Name: name},
				secret,
			) == nil, nil
		},
	); err != nil {
		t.Fatalf("related Secret was not synced to the service cluster: %v", err)
	}

	return secret
}

func waitForForegroundDeletion(
	t *testing.T,
	ctx context.Context,
	client ctrlruntimeclient.Client,
	obj ctrlruntimeclient.Object,
) {
	t.Helper()

	if err := wait.PollUntilContextTimeout(
		ctx,
		500*time.Millisecond,
		30*time.Second,
		false,
		func(ctx context.Context) (bool, error) {
			current := obj.DeepCopyObject().(ctrlruntimeclient.Object)
			if err := client.Get(ctx, ctrlruntimeclient.ObjectKeyFromObject(obj), current); err != nil {
				return false, nil
			}
			if current.GetDeletionTimestamp() == nil {
				return false, nil
			}

			hasForegroundFinalizer := false
			for _, finalizer := range current.GetFinalizers() {
				if finalizer == metav1.FinalizerDeleteDependents {
					hasForegroundFinalizer = true
				}
				if finalizer == metav1.FinalizerOrphanDependents {
					return false, nil
				}
			}

			return hasForegroundFinalizer, nil
		},
	); err != nil {
		t.Fatalf("Destination object did not enter foreground deletion: %v", err)
	}
}
