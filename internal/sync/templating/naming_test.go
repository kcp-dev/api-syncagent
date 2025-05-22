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

package templating

import (
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"

	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func createNewObject(name, namespace string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetName(name)
	obj.SetNamespace(namespace)

	return obj
}

func TestGenerateLocalObjectName(t *testing.T) {
	testcases := []struct {
		name         string
		clusterName  string
		clusterPath  string
		remoteObject *unstructured.Unstructured
		namingConfig *syncagentv1alpha1.ResourceNaming
		expected     types.NamespacedName
	}{
		{
			name:         "follow default naming rules",
			clusterName:  "testcluster",
			remoteObject: createNewObject("objname", "objnamespace"),
			namingConfig: nil,
			expected:     types.NamespacedName{Namespace: "testcluster", Name: "2928c2aa0e510f017f07-73d08a1ba188f1340dae"},
		},
		{
			name:         "custom static namespace pattern",
			clusterName:  "testcluster",
			remoteObject: createNewObject("objname", "objnamespace"),
			namingConfig: &syncagentv1alpha1.ResourceNaming{Namespace: "foobar"},
			expected:     types.NamespacedName{Namespace: "foobar", Name: "2928c2aa0e510f017f07-73d08a1ba188f1340dae"},
		},
		{
			name:         "custom dynamic namespace pattern",
			clusterName:  "testcluster",
			remoteObject: createNewObject("objname", "objnamespace"),
			namingConfig: &syncagentv1alpha1.ResourceNaming{Namespace: "foobar-$remoteClusterName"},
			expected:     types.NamespacedName{Namespace: "foobar-testcluster", Name: "2928c2aa0e510f017f07-73d08a1ba188f1340dae"},
		},
		{
			name:         "plain, unhashed values should be available in patterns",
			clusterName:  "testcluster",
			remoteObject: createNewObject("objname", "objnamespace"),
			namingConfig: &syncagentv1alpha1.ResourceNaming{Namespace: "$remoteNamespace"},
			expected:     types.NamespacedName{Namespace: "objnamespace", Name: "2928c2aa0e510f017f07-73d08a1ba188f1340dae"},
		},
		{
			name:         "configured but empty patterns",
			clusterName:  "testcluster",
			remoteObject: createNewObject("objname", "objnamespace"),
			namingConfig: &syncagentv1alpha1.ResourceNaming{Namespace: "", Name: ""},
			expected:     types.NamespacedName{Namespace: "testcluster", Name: "2928c2aa0e510f017f07-73d08a1ba188f1340dae"},
		},
		{
			name:         "custom dynamic name pattern",
			clusterName:  "testcluster",
			remoteObject: createNewObject("objname", "objnamespace"),
			namingConfig: &syncagentv1alpha1.ResourceNaming{Name: "foobar-$remoteName"},
			expected:     types.NamespacedName{Namespace: "testcluster", Name: "foobar-objname"},
		},
	}

	for _, testcase := range testcases {
		t.Run(testcase.name, func(t *testing.T) {
			pubRes := &syncagentv1alpha1.PublishedResource{
				Spec: syncagentv1alpha1.PublishedResourceSpec{
					Naming: testcase.namingConfig,
				},
			}

			generatedName, err := GenerateLocalObjectName(pubRes, testcase.remoteObject, logicalcluster.Name(testcase.clusterName), logicalcluster.NewPath(testcase.clusterPath))
			if err != nil {
				t.Fatalf("Unexpected error: %v.", err)
			}

			if generatedName.String() != testcase.expected.String() {
				t.Errorf("Expected %q, but got %q.", testcase.expected, generatedName)
			}
		})
	}
}
