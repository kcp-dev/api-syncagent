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

package kubeconfig

import (
	"testing"

	"k8s.io/client-go/rest"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name       string
		kubeconfig *rest.Config
		expErr     bool
	}{
		{
			name:       "nil kubeconfig",
			kubeconfig: nil,
			expErr:     true,
		},
		{
			name: "kubeconfig without workspace",
			kubeconfig: &rest.Config{
				Host: "https://example.com",
			},
			expErr: true,
		},
		{
			name: "valid kcp kubeconfig",
			kubeconfig: &rest.Config{
				Host: "https://example.com/clusters/foo",
			},
			expErr: false,
		},
	}

	for _, testcase := range tests {
		t.Run(testcase.name, func(t *testing.T) {
			err := Validate(testcase.kubeconfig)
			if (err != nil) != testcase.expErr {
				t.Errorf("Expected error %v, got %v", testcase.expErr, err)
			}
		})
	}
}
