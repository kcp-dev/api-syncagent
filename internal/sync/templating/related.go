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
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// relatedObjectContext is the data available to Go templates when determining
// the local object name (the `naming` section of a PublishedResource).
type relatedObjectContext struct {
	// Object is the primary object belonging to the related object. Since related
	// object templates are evaluated twice (once for the origin side and once
	// for the destination side), object is the primary object on the side the
	// template is evaluated for.
	Object map[string]any
	// ClusterName is the internal cluster identifier (e.g. "34hg2j4gh24jdfgf")
	// of the kcp workspace that the synchronization is currently processing. This
	// value is set for both evaluations, regardless of side.
	ClusterName logicalcluster.Name
	// ClusterPath is the workspace path (e.g. "root:customer:projectx"). This
	// value is set for both evaluations, regardless of side.
	ClusterPath logicalcluster.Path
}

func NewRelatedObjectContext(object *unstructured.Unstructured, clusterName logicalcluster.Name, clusterPath logicalcluster.Path) relatedObjectContext {
	return relatedObjectContext{
		Object:      object.Object,
		ClusterName: clusterName,
		ClusterPath: clusterPath,
	}
}
