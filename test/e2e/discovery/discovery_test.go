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

package discovery

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"

	"github.com/kcp-dev/api-syncagent/internal/discovery"
	"github.com/kcp-dev/api-syncagent/test/utils"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/clientcmd"
	ctrlruntime "sigs.k8s.io/controller-runtime"
)

func TestDiscoverSingleVersionCRD(t *testing.T) {
	testcases := []struct {
		name             string
		crdFiles         []string
		groupKind        schema.GroupKind
		expectedVersions []string
		expectedNames    apiextensionsv1.CustomResourceDefinitionNames
	}{
		{
			name:             "get non-CRD resource",
			groupKind:        schema.GroupKind{Group: "rbac.authorization.k8s.io", Kind: "Role"},
			expectedVersions: []string{"v1"},
			expectedNames: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "roles",
				Singular: "role",
				Kind:     "Role",
				ListKind: "RoleList",
			},
		},
		{
			name:             "get CRD with single version",
			crdFiles:         []string{"test/crds/backup.yaml"},
			groupKind:        schema.GroupKind{Group: "eksempel.no", Kind: "Backup"},
			expectedVersions: []string{"v1"},
			expectedNames: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "backups",
				Singular: "backup",
				Kind:     "Backup",
				ListKind: "BackupList",
			},
		},
		{
			name:             "get CRD with multiple versions",
			crdFiles:         []string{"test/crds/crontab-multi-versions.yaml"},
			groupKind:        schema.GroupKind{Group: "example.com", Kind: "CronTab"},
			expectedVersions: []string{"v1", "v2"},
			expectedNames: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:     "crontabs",
				Singular:   "crontab",
				ShortNames: []string{"ct"},
				Kind:       "CronTab",
				ListKind:   "CronTabList",
			},
		},
		{
			name:             "get non-CRD with multiple versions",
			groupKind:        schema.GroupKind{Group: "autoscaling", Kind: "HorizontalPodAutoscaler"},
			expectedVersions: []string{"v1", "v2"},
			expectedNames: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:     "horizontalpodautoscalers",
				Singular:   "horizontalpodautoscaler",
				ShortNames: []string{"hpa"},
				Kind:       "HorizontalPodAutoscaler",
				ListKind:   "HorizontalPodAutoscalerList",
				Categories: []string{"all"},
			},
		},
	}

	for _, testcase := range testcases {
		t.Run(testcase.name, func(t *testing.T) {
			ctx := context.Background()
			ctrlruntime.SetLogger(logr.Discard())

			kubeconfigFile, _, _ := utils.RunEnvtest(t, testcase.crdFiles)
			kubeconfig, err := clientcmd.LoadFromFile(kubeconfigFile)
			if err != nil {
				t.Fatalf("Failed to load envtest kubeconfig: %v", err)
			}

			restConfig, err := clientcmd.NewDefaultClientConfig(*kubeconfig, nil).ClientConfig()
			if err != nil {
				t.Fatalf("Failed to load envtest kubeconfig: %v", err)
			}

			client, err := discovery.NewClient(restConfig)
			if err != nil {
				t.Fatalf("Failed to create discovery client: %v", err)
			}

			crd, err := client.RetrieveCRD(ctx, testcase.groupKind)
			if err != nil {
				t.Fatalf("Failed to discover GK: %v", err)
			}

			if diff := cmp.Diff(testcase.expectedNames, crd.Spec.Names); diff != "" {
				t.Errorf("Did not get expected CRD names:\n\n%s", diff)
			}

			var versions []string
			for _, v := range crd.Spec.Versions {
				versions = append(versions, v.Name)
			}

			if diff := cmp.Diff(testcase.expectedVersions, versions); diff != "" {
				t.Errorf("Did not get expected CRD versions:\n\n%s", diff)
			}
		})
	}
}
