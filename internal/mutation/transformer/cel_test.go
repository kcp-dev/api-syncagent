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

func TestCEL(t *testing.T) {
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
		mutation  syncagentv1alpha1.ResourceCELMutation
		expected  *unstructured.Unstructured
	}{
		{
			name:      "replace value at path with a fixed value of the same type",
			inputData: commonInputObject,
			mutation: syncagentv1alpha1.ResourceCELMutation{
				Path:       "spec.cronSpec",
				Expression: `"hei verden"`,
			},
			expected: utils.YAMLToUnstructured(t, `
apiVersion: kcp.example.com/v1
kind: CronTab
metadata:
  namespace: default
  name: my-crontab
spec:
  cronSpec: "hei verden"
  image: ubuntu:latest
`),
		},
		{
			name:      "replace value at path with a fixed value of other type",
			inputData: commonInputObject,
			mutation: syncagentv1alpha1.ResourceCELMutation{
				Path:       "spec.cronSpec",
				Expression: `42`,
			},
			expected: utils.YAMLToUnstructured(t, `
apiVersion: kcp.example.com/v1
kind: CronTab
metadata:
  namespace: default
  name: my-crontab
spec:
  cronSpec: 42
  image: ubuntu:latest
`),
		},
		{
			name:      "access the current value at the path",
			inputData: commonInputObject,
			mutation: syncagentv1alpha1.ResourceCELMutation{
				Path:       "spec.cronSpec",
				Expression: `value + "foo"`,
			},
			expected: utils.YAMLToUnstructured(t, `
apiVersion: kcp.example.com/v1
kind: CronTab
metadata:
  namespace: default
  name: my-crontab
spec:
  cronSpec: '* * *foo'
  image: ubuntu:latest
`),
		},
		{
			name:      "access value from the object from the other side",
			inputData: commonInputObject,
			otherObj:  commonInputObject,
			mutation: syncagentv1alpha1.ResourceCELMutation{
				Path:       "spec.cronSpec",
				Expression: `other.spec.image`,
			},
			expected: utils.YAMLToUnstructured(t, `
apiVersion: kcp.example.com/v1
kind: CronTab
metadata:
  namespace: default
  name: my-crontab
spec:
  cronSpec: ubuntu:latest
  image: ubuntu:latest
`),
		},
	}

	for _, testcase := range testcases {
		t.Run(testcase.name, func(t *testing.T) {
			transformer, err := NewCEL(&testcase.mutation)
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
