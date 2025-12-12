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
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// relatedObjectContext is the data available to Go templates when evaluating
// the origin of a related object.
type relatedObjectContext struct {
	// Side is set to either one of the possible origin values to indicate for
	// which cluster the template is currently being evaluated for.
	Side syncagentv1alpha1.RelatedResourceOrigin
	// Object is the primary object belonging to the related object. Since related
	// object templates are evaluated twice (once for the origin side and once
	// for the destination side), object is the primary object on the side the
	// template is evaluated for.
	Object map[string]any
	// ClusterName is the internal cluster identifier (e.g. "34hg2j4gh24jdfgf")
	// of the kcp workspace that the synchronization is currently processing. This
	// value is set for both evaluations, regardless of side.
	ClusterName string
	// ClusterPath is the workspace path (e.g. "root:customer:projectx"). This
	// value is set for both evaluations, regardless of side.
	ClusterPath string
}

func NewRelatedObjectContext(object *unstructured.Unstructured, side syncagentv1alpha1.RelatedResourceOrigin, clusterName logicalcluster.Name, clusterPath logicalcluster.Path) relatedObjectContext {
	return relatedObjectContext{
		Side:        side,
		Object:      object.Object,
		ClusterName: clusterName.String(),
		ClusterPath: clusterPath.String(),
	}
}

// relatedObjectContext is the data available to Go templates in the keys and values
// of a label selector for a related object.
type relatedObjectLabelContext struct {
	// LocalObject is the primary object copy on the local side of the sync
	// (i.e. on the service cluster).
	LocalObject map[string]any
	// RemoteObject is the primary object original, in kcp.
	RemoteObject map[string]any
	// ClusterName is the internal cluster identifier (e.g. "34hg2j4gh24jdfgf")
	// of the kcp workspace that the synchronization is currently processing
	// (where the remote object exists).
	ClusterName string
	// ClusterPath is the workspace path (e.g. "root:customer:projectx").
	ClusterPath string
}

func NewRelatedObjectLabelContext(localObject, remoteObject *unstructured.Unstructured, clusterName logicalcluster.Name, clusterPath logicalcluster.Path) relatedObjectLabelContext {
	return relatedObjectLabelContext{
		LocalObject:  localObject.Object,
		RemoteObject: remoteObject.Object,
		ClusterName:  clusterName.String(),
		ClusterPath:  clusterPath.String(),
	}
}

// relatedObjectLabelRewriteContext is the data available to Go templates when
// mapping the found namespace names and objects names from having evaluated a
// label selector previously.
type relatedObjectLabelRewriteContext struct {
	// Value is either the a found namespace name (when a label selector was
	// used to select the source namespaces for related objects) or the name of
	// a found object (when a label selector was used to find objects). In the
	// former case, the template should return the new namespace to use on the
	// destination side, in the latter case it should return the new object name
	// to use on the destination side.
	Value string
	// When a rewrite is used to rewrite object names, RelatedObject is the
	// original related object (found on the origin side). This enables you to
	// ignore the given Value entirely and just select anything from the object
	// itself.
	// RelatedObject is nil when the rewrite is performed for a namespace.
	RelatedObject map[string]any
	// LocalObject is the primary object copy on the local side of the sync
	// (i.e. on the service cluster).
	LocalObject map[string]any
	// RemoteObject is the primary object original, in kcp.
	RemoteObject map[string]any
	// ClusterName is the internal cluster identifier (e.g. "34hg2j4gh24jdfgf")
	// of the kcp workspace that the synchronization is currently processing
	// (where the remote object exists).
	ClusterName string
	// ClusterPath is the workspace path (e.g. "root:customer:projectx").
	ClusterPath string
}

func NewRelatedObjectLabelRewriteContext(value string, localObject, remoteObject, relatedObject *unstructured.Unstructured, clusterName logicalcluster.Name, clusterPath logicalcluster.Path) relatedObjectLabelRewriteContext {
	ctx := relatedObjectLabelRewriteContext{
		Value:        value,
		LocalObject:  localObject.Object,
		RemoteObject: remoteObject.Object,
		ClusterName:  clusterName.String(),
		ClusterPath:  clusterPath.String(),
	}

	if relatedObject != nil {
		ctx.RelatedObject = relatedObject.Object
	}

	return ctx
}
