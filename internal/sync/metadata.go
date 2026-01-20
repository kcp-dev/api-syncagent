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

package sync

import (
	"fmt"
	"maps"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

func stripMetadata(obj *unstructured.Unstructured) error {
	obj.SetCreationTimestamp(metav1.Time{})
	obj.SetFinalizers(nil)
	obj.SetGeneration(0)
	obj.SetOwnerReferences(nil)
	obj.SetResourceVersion("")
	obj.SetManagedFields(nil)
	obj.SetUID("")
	obj.SetSelfLink("")

	if err := stripAnnotations(obj); err != nil {
		return fmt.Errorf("failed to strip annotations: %w", err)
	}
	if err := stripLabels(obj); err != nil {
		return fmt.Errorf("failed to strip labels: %w", err)
	}

	return nil
}

func setNestedMapOmitempty(obj *unstructured.Unstructured, value map[string]string, path ...string) error {
	if len(value) == 0 {
		unstructured.RemoveNestedField(obj.Object, path...)
		return nil
	}

	return unstructured.SetNestedStringMap(obj.Object, value, path...)
}

func stripAnnotations(obj *unstructured.Unstructured) error {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		return nil
	}

	if err := setNestedMapOmitempty(obj, filterUnsyncableAnnotations(annotations), "metadata", "annotations"); err != nil {
		return err
	}

	return nil
}

func stripLabels(obj *unstructured.Unstructured) error {
	labels := obj.GetLabels()
	if labels == nil {
		return nil
	}

	if err := setNestedMapOmitempty(obj, filterUnsyncableLabels(labels), "metadata", "labels"); err != nil {
		return err
	}

	return nil
}

// unsyncableLabels are labels we never want to copy from the remote to local objects.
var unsyncableLabels = sets.New(
	remoteObjectClusterLabel,
	remoteObjectNamespaceHashLabel,
	remoteObjectNameHashLabel,
)

// filterUnsyncableLabels removes all unwanted remote labels and returns a new label set.
func filterUnsyncableLabels(original labels.Set) labels.Set {
	filtered := filterLabels(original, unsyncableLabels)

	out := labels.Set{}
	for k, v := range filtered {
		if !strings.HasPrefix(k, "claimed.internal.apis.kcp.io/") {
			out[k] = v
		}
	}

	return out
}

// unsyncableAnnotations are annotations we never want to copy from the remote to local objects.
var unsyncableAnnotations = sets.New(
	"kcp.io/cluster",
	"kubectl.kubernetes.io/last-applied-configuration",
	remoteObjectNamespaceAnnotation,
	remoteObjectNameAnnotation,
	remoteObjectWorkspacePathAnnotation,
)

// filterUnsyncableAnnotations removes all unwanted remote annotations and returns a new label set.
func filterUnsyncableAnnotations(original labels.Set) labels.Set {
	filtered := filterLabels(original, unsyncableAnnotations)

	maps.DeleteFunc(filtered, func(annotation string, _ string) bool {
		return strings.HasPrefix(annotation, relatedObjectAnnotationPrefix)
	})

	return filtered
}

func filterLabels(original labels.Set, forbidList sets.Set[string]) labels.Set {
	filtered := labels.Set{}
	for k, v := range original {
		if !forbidList.Has(k) {
			filtered[k] = v
		}
	}

	return filtered
}

func RemoteNameForLocalObject(localObj ctrlruntimeclient.Object) *mcreconcile.Request {
	labels := localObj.GetLabels()
	annotations := localObj.GetAnnotations()
	clusterName := labels[remoteObjectClusterLabel]
	namespace := annotations[remoteObjectNamespaceAnnotation]
	name := annotations[remoteObjectNameAnnotation]

	// reject/ignore invalid/badly labelled object
	if clusterName == "" || name == "" {
		return nil
	}

	return &mcreconcile.Request{
		ClusterName: clusterName,
		Request: reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: namespace,
				Name:      name,
			},
		},
	}
}

// threeWayDiffMetadata is used when updating an object. Since the lastKnownState for any object
// does not contain syncer-related metadata, this function determines whether labels/annotations are
// missing by comparing the desired* sets with the current state on the destObj.
// If a label/annotation is found to be missing or wrong, this function will set it on the sourceObj.
// This is confusing at first, but the source object here is just a DeepCopy from the actual source
// object and the caller is not meant to persist changes on the source object. The reason the changes
// are performed on the source object is so that when creating the patch later on (which is done by
// comparing the source object with the lastKnownState), the patch will contain the necessary changes.
func threeWayDiffMetadata(sourceObj, destObj *unstructured.Unstructured, desiredLabels, desiredAnnotations labels.Set) {
	destLabels := destObj.GetLabels()
	sourceLabels := sourceObj.GetLabels()

	for label, value := range desiredLabels {
		if destValue, ok := destLabels[label]; !ok || destValue != value {
			if sourceLabels == nil {
				sourceLabels = map[string]string{}
			}

			sourceLabels[label] = value
		}
	}

	sourceObj.SetLabels(sourceLabels)

	destAnnotations := destObj.GetAnnotations()
	sourceAnnotations := sourceObj.GetAnnotations()

	for label, value := range desiredAnnotations {
		if destValue, ok := destAnnotations[label]; !ok || destValue != value {
			if sourceAnnotations == nil {
				sourceAnnotations = map[string]string{}
			}

			sourceAnnotations[label] = value
		}
	}

	sourceObj.SetAnnotations(sourceAnnotations)
}
