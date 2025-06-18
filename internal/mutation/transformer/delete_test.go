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

	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"
)

func TestDelete(t *testing.T) {
	testcases := []struct {
		name      string
		inputData string
		mutation  syncagentv1alpha1.ResourceDeleteMutation
		expected  string
	}{
		{
			name:      "can remove object keys",
			inputData: `{"spec":{"secretName":"foo"}}`,
			mutation: syncagentv1alpha1.ResourceDeleteMutation{
				Path: "spec.secretName",
			},
			expected: `{"spec":{}}`,
		},
		{
			name:      "can remove array items",
			inputData: `{"spec":[1,2,3]}`,
			mutation: syncagentv1alpha1.ResourceDeleteMutation{
				Path: "spec.1",
			},
			expected: `{"spec":[1,3]}`,
		},
	}

	for _, testcase := range testcases {
		t.Run(testcase.name, func(t *testing.T) {
			transformer, err := NewDelete(&testcase.mutation)
			if err != nil {
				t.Fatalf("Failed to create transformer: %v", err)
			}

			transformed, err := transformer.ApplyJSON(testcase.inputData, "")
			if err != nil {
				t.Fatalf("Failed to transform: %v", err)
			}

			if testcase.expected != transformed {
				t.Errorf("Expected %q, but got %q.", testcase.expected, transformed)
			}
		})
	}
}
