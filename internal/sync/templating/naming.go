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
	"fmt"
	"strings"

	"github.com/kcp-dev/api-syncagent/internal/crypto"
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// localObjectNamingContext is the data available to Go templates when determining
// the local object name (the `naming` section of a PublishedResource).
type localObjectNamingContext struct {
	// Object is the full remote object found in a kcp workspace.
	Object map[string]any
	// ClusterName is the internal cluster identifier (e.g. "34hg2j4gh24jdfgf").
	ClusterName string
	// ClusterPath is the workspace path (e.g. "root:customer:projectx").
	ClusterPath string
}

func newLocalObjectNamingContext(object *unstructured.Unstructured, clusterName logicalcluster.Name, workspacePath logicalcluster.Path) localObjectNamingContext {
	return localObjectNamingContext{
		Object:      object.Object,
		ClusterName: clusterName.String(),
		ClusterPath: workspacePath.String(),
	}
}

var defaultNamingScheme = syncagentv1alpha1.ResourceNaming{
	Namespace: "{{ .ClusterName }}",
	Name:      "{{ .Object.metadata.namespace | sha3short }}-{{ .Object.metadata.name | sha3short }}",
}

func GenerateLocalObjectName(pr *syncagentv1alpha1.PublishedResource, object *unstructured.Unstructured, clusterName logicalcluster.Name, workspacePath logicalcluster.Path) (types.NamespacedName, error) {
	naming := pr.Spec.Naming
	if naming == nil {
		naming = &syncagentv1alpha1.ResourceNaming{}
	}

	result := types.NamespacedName{}

	pattern := naming.Namespace
	if pattern == "" {
		pattern = defaultNamingScheme.Namespace
	}
	rendered, err := generateLocalObjectIdentifier(pattern, object, clusterName, workspacePath)
	if err != nil {
		return result, fmt.Errorf("invalid namespace naming: %w", err)
	}

	result.Namespace = rendered

	pattern = naming.Name
	if pattern == "" {
		pattern = defaultNamingScheme.Name
	}
	rendered, err = generateLocalObjectIdentifier(pattern, object, clusterName, workspacePath)
	if err != nil {
		return result, fmt.Errorf("invalid name naming: %w", err)
	}

	result.Name = rendered

	return result, nil
}

func generateLocalObjectIdentifier(pattern string, object *unstructured.Unstructured, clusterName logicalcluster.Name, workspacePath logicalcluster.Path) (string, error) {
	// modern Go template style
	if strings.Contains(pattern, "{{") {
		return Render(pattern, newLocalObjectNamingContext(object, clusterName, workspacePath))
	}

	// Legacy $variable style, does also not support clusterPath;
	// note that all of these constants are deprecated already.
	replacer := strings.NewReplacer(
		// order of elements is important here, "$fooHash" needs to be defined before "$foo"
		syncagentv1alpha1.PlaceholderRemoteClusterName, clusterName.String(), //nolint:staticcheck
		syncagentv1alpha1.PlaceholderRemoteNamespaceHash, crypto.ShortHash(object.GetNamespace()), //nolint:staticcheck
		syncagentv1alpha1.PlaceholderRemoteNamespace, object.GetNamespace(), //nolint:staticcheck
		syncagentv1alpha1.PlaceholderRemoteNameHash, crypto.ShortHash(object.GetName()), //nolint:staticcheck
		syncagentv1alpha1.PlaceholderRemoteName, object.GetName(), //nolint:staticcheck
	)

	return replacer.Replace(pattern), nil
}
