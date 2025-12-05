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

	crds "github.com/kcp-dev/api-syncagent/test/crds/example/v1"
	"github.com/kcp-dev/api-syncagent/test/utils"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRender(t *testing.T) {
	clusterName := logicalcluster.Name("12386rtr4u")
	clusterPath := logicalcluster.NewPath("root:yadda:yadda")

	crontab := &crds.Crontab{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-crontab",
			Namespace: "default",
		},
		Spec: crds.CrontabSpec{
			Image: "ubuntu:latest",
		},
	}

	object := utils.ToUnstructured(t, crontab)
	object.SetAPIVersion("kcp.example.com/v1")
	object.SetKind("CronTab")

	testcases := []struct {
		name     string
		template string
		data     any
		expected string
	}{
		{
			name:     "simple object access",
			data:     newLocalObjectNamingContext(object, clusterName, clusterPath),
			template: `{{ .Object.spec.image }}`,
			expected: crontab.Spec.Image,
		},
		{
			name:     "default empty fields",
			data:     newLocalObjectNamingContext(object, clusterName, clusterPath),
			template: `{{ .Object.spec.spec | default "test" }}`,
			expected: "test",
		},
		{
			name:     "default non-existing field",
			data:     newLocalObjectNamingContext(object, clusterName, clusterPath),
			template: `{{ .Object.does.not.exist | default "test" }}`,
			expected: "test",
		},
	}

	for _, testcase := range testcases {
		t.Run(testcase.name, func(t *testing.T) {
			rendered, err := Render(testcase.template, testcase.data)
			if err != nil {
				t.Fatalf("Unexpected error: %v.", err)
			}

			if rendered != testcase.expected {
				t.Errorf("Expected %q, but got %q.", testcase.expected, rendered)
			}
		})
	}
}
