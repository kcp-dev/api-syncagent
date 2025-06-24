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

func TestRegex(t *testing.T) {
	testcases := []struct {
		name      string
		inputData string
		mutation  syncagentv1alpha1.ResourceRegexMutation
		expected  string
	}{
		{
			name:      "replace one existing value",
			inputData: `{"spec":{"secretName":"foo"}}`,
			mutation: syncagentv1alpha1.ResourceRegexMutation{
				Path:        "spec.secretName",
				Pattern:     "",
				Replacement: "new-value",
			},
			expected: `{"spec":{"secretName":"new-value"}}`,
		},
		{
			name:      "rewrite one existing value",
			inputData: `{"spec":{"secretName":"foo"}}`,
			mutation: syncagentv1alpha1.ResourceRegexMutation{
				Path:        "spec.secretName",
				Pattern:     "o",
				Replacement: "u",
			},
			expected: `{"spec":{"secretName":"fuu"}}`,
		},
		{
			name:      "should support grouping",
			inputData: `{"spec":{"secretName":"foo"}}`,
			mutation: syncagentv1alpha1.ResourceRegexMutation{
				Path:        "spec.secretName",
				Pattern:     "(f)oo",
				Replacement: "oo$1",
			},
			expected: `{"spec":{"secretName":"oof"}}`,
		},
		{
			name:      "coalesces to strings",
			inputData: `{"spec":{"aNumber":24}}`,
			mutation: syncagentv1alpha1.ResourceRegexMutation{
				Path:        "spec.aNumber",
				Pattern:     "4",
				Replacement: "5",
			},
			expected: `{"spec":{"aNumber":"25"}}`,
		},
		{
			name:      "can change types",
			inputData: `{"spec":{"aNumber":24}}`,
			mutation: syncagentv1alpha1.ResourceRegexMutation{
				Path:        "spec",
				Replacement: "new-value",
			},
			expected: `{"spec":"new-value"}`,
		},
		{
			name:      "can change types /2",
			inputData: `{"spec":{"aNumber":24}}`,
			mutation: syncagentv1alpha1.ResourceRegexMutation{
				Path: "spec",
				// Due to the string coalescing, this will turn the {aNumber:42} object
				// into a string, of which we match every character and return it,
				// effectively stringify-ing an object.
				Pattern:     "(.)",
				Replacement: "$1",
			},
			expected: `{"spec":"{\"aNumber\":24}"}`,
		},
		{
			name:      "can empty values",
			inputData: `{"spec":{"aNumber":24}}`,
			mutation: syncagentv1alpha1.ResourceRegexMutation{
				Path:        "spec",
				Replacement: "",
			},
			expected: `{"spec":""}`,
		},
		{
			name:      "can empty values /2",
			inputData: `{"spec":{"aNumber":24}}`,
			mutation: syncagentv1alpha1.ResourceRegexMutation{
				Path:        "spec",
				Pattern:     ".+",
				Replacement: "",
			},
			expected: `{"spec":""}`,
		},
	}

	for _, testcase := range testcases {
		t.Run(testcase.name, func(t *testing.T) {
			transformer, err := NewRegex(&testcase.mutation)
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
