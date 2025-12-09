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

package sdk

import (
	"regexp"
	"strings"
	"testing"

	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"
	"github.com/kcp-dev/api-syncagent/test/utils"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestValidateRelatedResourceSpec(t *testing.T) {
	// start a service cluster
	_, envtestClient, _ := utils.RunEnvtest(t, nil)

	testcases := []struct {
		name  string
		spec  syncagentv1alpha1.RelatedResourceSpec
		valid bool
	}{
		{
			name:  "legacy kind ConfigMap",
			valid: true,
			spec: syncagentv1alpha1.RelatedResourceSpec{
				Origin: syncagentv1alpha1.RelatedResourceOriginService,
				Kind:   "ConfigMap",
			},
		},
		{
			name:  "invalid legacy kind",
			valid: false,
			spec: syncagentv1alpha1.RelatedResourceSpec{
				Origin: syncagentv1alpha1.RelatedResourceOriginService,
				Kind:   "Service",
			},
		},
		{
			name:  "cannot combine kind with group",
			valid: false,
			spec: syncagentv1alpha1.RelatedResourceSpec{
				Origin: syncagentv1alpha1.RelatedResourceOriginService,
				Kind:   "ConfigMap",
				Group:  "core",
			},
		},
		{
			name:  "cannot combine kind with resource",
			valid: false,
			spec: syncagentv1alpha1.RelatedResourceSpec{
				Origin:   syncagentv1alpha1.RelatedResourceOriginService,
				Kind:     "ConfigMap",
				Resource: "configmaps",
			},
		},
		{
			name:  "cannot combine kind with version",
			valid: false,
			spec: syncagentv1alpha1.RelatedResourceSpec{
				Origin:  syncagentv1alpha1.RelatedResourceOriginService,
				Kind:    "ConfigMap",
				Version: "v1",
			},
		},
		{
			name:  "cannot combine kind with identityHash",
			valid: false,
			spec: syncagentv1alpha1.RelatedResourceSpec{
				Origin:       syncagentv1alpha1.RelatedResourceOriginService,
				Kind:         "ConfigMap",
				IdentityHash: "abc123",
			},
		},
		{
			name:  "vanilla core/v1 resource",
			valid: true,
			spec: syncagentv1alpha1.RelatedResourceSpec{
				Origin:   syncagentv1alpha1.RelatedResourceOriginService,
				Resource: "configmaps",
				Version:  "v1",
			},
		},
		{
			name:  "resource requires version",
			valid: false,
			spec: syncagentv1alpha1.RelatedResourceSpec{
				Origin:   syncagentv1alpha1.RelatedResourceOriginService,
				Resource: "configmaps",
			},
		},
		{
			name:  "vanilla random GVR",
			valid: true,
			spec: syncagentv1alpha1.RelatedResourceSpec{
				Origin:   syncagentv1alpha1.RelatedResourceOriginService,
				Group:    "example.com",
				Resource: "things",
				Version:  "v1",
			},
		},
		{
			name:  "group requires resource",
			valid: false,
			spec: syncagentv1alpha1.RelatedResourceSpec{
				Origin:  syncagentv1alpha1.RelatedResourceOriginService,
				Group:   "example.com",
				Version: "v1",
			},
		},
		{
			name:  "group requires version",
			valid: false,
			spec: syncagentv1alpha1.RelatedResourceSpec{
				Origin:   syncagentv1alpha1.RelatedResourceOriginService,
				Group:    "example.com",
				Resource: "things",
			},
		},
		{
			name:  "vanilla foreign resource",
			valid: true,
			spec: syncagentv1alpha1.RelatedResourceSpec{
				Origin:       syncagentv1alpha1.RelatedResourceOriginService,
				Group:        "example.com",
				Resource:     "things",
				Version:      "v1",
				IdentityHash: "abc123",
			},
		},
		{
			name:  "identity hash requires group",
			valid: false,
			spec: syncagentv1alpha1.RelatedResourceSpec{
				Origin:       syncagentv1alpha1.RelatedResourceOriginService,
				Resource:     "things",
				Version:      "v1",
				IdentityHash: "abc123",
			},
		},
		{
			name:  "identity hash requires resource",
			valid: false,
			spec: syncagentv1alpha1.RelatedResourceSpec{
				Origin:       syncagentv1alpha1.RelatedResourceOriginService,
				Group:        "example.com",
				Version:      "v1",
				IdentityHash: "abc123",
			},
		},
		{
			name:  "identity hash requires version",
			valid: false,
			spec: syncagentv1alpha1.RelatedResourceSpec{
				Origin:       syncagentv1alpha1.RelatedResourceOriginService,
				Group:        "example.com",
				Resource:     "things",
				IdentityHash: "abc123",
			},
		},
	}

	alphaNum := regexp.MustCompile(`[^a-z0-9]`)

	for _, tt := range testcases {
		t.Run(tt.name, func(t *testing.T) {
			crdName := strings.ToLower(tt.name)
			crdName = alphaNum.ReplaceAllLiteralString(crdName, "-")

			pubRes := &syncagentv1alpha1.PublishedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-" + crdName,
				},
				Spec: syncagentv1alpha1.PublishedResourceSpec{
					Related: []syncagentv1alpha1.RelatedResourceSpec{tt.spec},
				},
			}

			err := envtestClient.Create(t.Context(), pubRes)

			if tt.valid {
				if err != nil {
					t.Fatalf("Spec is valid, but server returned: %v", err)
				}
			} else if err == nil {
				t.Fatal("Spec is invalid, but server accepted it.")
			}
		})
	}
}
