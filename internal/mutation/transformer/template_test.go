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

package transformer

import (
	"testing"

	"github.com/kcp-dev/api-syncagent/internal/test/diff"
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"
	"github.com/kcp-dev/api-syncagent/test/utils"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestTemplate(t *testing.T) {
	commonInputObject := utils.YAMLToUnstructured(t, `
apiVersion: kcp.example.com/v1
kind: CronTab
metadata:
  namespace: default
  name: my-crontab
spec:
  cronSpec: '* * *'
  image: ubuntu:latest
`)

	testcases := []struct {
		name      string
		inputData *unstructured.Unstructured
		otherObj  *unstructured.Unstructured
		mutation  syncagentv1alpha1.ResourceTemplateMutation
		expected  *unstructured.Unstructured
	}{
		{
			name:      "empty template returns empty value",
			inputData: commonInputObject,
			mutation: syncagentv1alpha1.ResourceTemplateMutation{
				Path: "spec.cronSpec",
			},
			expected: utils.YAMLToUnstructured(t, `
apiVersion: kcp.example.com/v1
kind: CronTab
metadata:
  namespace: default
  name: my-crontab
spec:
  cronSpec: ''
  image: ubuntu:latest
`),
		},
		{
			name:      "can change value type",
			inputData: commonInputObject,
			mutation: syncagentv1alpha1.ResourceTemplateMutation{
				Path: "spec",
			},
			expected: utils.YAMLToUnstructured(t, `
apiVersion: kcp.example.com/v1
kind: CronTab
metadata:
  namespace: default
  name: my-crontab
spec: ''
`),
		},
		{
			name:      "execute basic template",
			inputData: commonInputObject,
			mutation: syncagentv1alpha1.ResourceTemplateMutation{
				Path:     "spec.image",
				Template: `{{ upper .Value.String }}`,
			},
			expected: utils.YAMLToUnstructured(t, `
apiVersion: kcp.example.com/v1
kind: CronTab
metadata:
  namespace: default
  name: my-crontab
spec:
  cronSpec: '* * *'
  image: UBUNTU:LATEST
`),
		},
	}

	for _, testcase := range testcases {
		t.Run(testcase.name, func(t *testing.T) {
			transformer, err := NewTemplate(&testcase.mutation)
			if err != nil {
				t.Fatalf("Failed to create transformer: %v", err)
			}

			transformed, err := transformer.Apply(testcase.inputData, testcase.otherObj)
			if err != nil {
				t.Fatalf("Failed to transform: %v", err)
			}

			if changes := diff.ObjectDiff(testcase.expected, transformed); changes != "" {
				t.Errorf("Did not get expected object:\n\n%s", changes)
			}
		})
	}
}
