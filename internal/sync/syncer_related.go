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
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	"github.com/kcp-dev/api-syncagent/internal/mutation"
	"github.com/kcp-dev/api-syncagent/internal/projection"
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"
	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func (s *ResourceSyncer) processRelatedResources(log *zap.SugaredLogger, stateStore ObjectStateStore, remote, local syncSide) (requeue bool, err error) {
	for _, relatedResource := range s.pubRes.Spec.Related {
		requeue, err := s.processRelatedResource(log.With("identifier", relatedResource.Identifier), stateStore, remote, local, relatedResource)
		if err != nil {
			return false, fmt.Errorf("failed to process related resource %s: %w", relatedResource.Identifier, err)
		}

		if requeue {
			return true, nil
		}
	}

	return false, nil
}

type relatedObjectAnnotation struct {
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
}

func (s *ResourceSyncer) processRelatedResource(log *zap.SugaredLogger, stateStore ObjectStateStore, remote, local syncSide, relRes syncagentv1alpha1.RelatedResourceSpec) (requeue bool, err error) {
	// decide what direction to sync (local->remote vs. remote->local)
	var (
		source syncSide
		dest   syncSide
	)

	if relRes.Origin == "service" {
		source = local
		dest = remote
	} else {
		source = remote
		dest = local
	}

	var sourceKey *ctrlruntimeclient.ObjectKey
	switch {
	case relRes.Reference != nil:
		// to find the source related object, we first need to determine its name/namespace
		sourceKey, err = resolveResourceReference(source.object, *relRes.Reference)
		if err != nil {
			return false, fmt.Errorf("failed to determine related object's source key: %w", err)
		}
	case relRes.LabelSelector != nil:
		// if no reference is given, we can use a label selector to find the source object
		sourceObjs := &unstructured.UnstructuredList{}
		sourceObjs.SetAPIVersion("v1")
		sourceObjs.SetKind(relRes.Kind)

		// TODO: would need to handle replacer here as well to select right object.
		cn, ok := source.object.GetLabels()[remoteObjectClusterLabel]
		if !ok {
			return false, fmt.Errorf("missing cluster label on source object")
		}
		clusterName := logicalcluster.Name(cn)
		labels := projection.GenerateLocalLabelSelector(&relRes, source.object, clusterName)

		if err := source.client.List(source.ctx, sourceObjs, ctrlruntimeclient.MatchingLabels(labels.MatchLabels)); err != nil {
			return false, fmt.Errorf("failed to list related objects: %w", err)
		}
		if len(sourceObjs.Items) == 0 {
			return false, nil
		}
		if len(sourceObjs.Items) > 1 {
			// HACK: we take first one, as we can't select multiple objects or by name only!
			log.Warnw("found multiple related objects, taking first one", "count", len(sourceObjs.Items))
		}
		sourceKey = &ctrlruntimeclient.ObjectKey{
			Namespace: sourceObjs.Items[0].GetNamespace(),
			Name:      sourceObjs.Items[0].GetName(),
		}
	}

	// find the source related object
	sourceObj := &unstructured.Unstructured{}
	sourceObj.SetAPIVersion("v1") // we only support ConfigMaps and Secrets, both are in core/v1
	sourceObj.SetKind(relRes.Kind)

	err = source.client.Get(source.ctx, *sourceKey, sourceObj)
	if err != nil {
		// the source object doesn't exist yet, so we can just stop
		if apierrors.IsNotFound(err) {
			return false, nil
		}

		return false, fmt.Errorf("failed to get source object: %w", err)
	}

	// do the same to find the destination object
	var destKey *ctrlruntimeclient.ObjectKey
	switch {
	case relRes.Reference != nil:
		destKey, err = resolveResourceReference(dest.object, *relRes.Reference)
		if err != nil {
			return false, fmt.Errorf("failed to determine related object's destination key: %w", err)
		}
	case relRes.LabelSelector != nil:
		destKey = &ctrlruntimeclient.ObjectKey{
			Namespace: sourceObj.GetNamespace(),
			Name:      sourceObj.GetName(),
		}
	}

	destObj := &unstructured.Unstructured{}
	destObj.SetAPIVersion("v1")
	destObj.SetKind(relRes.Kind)

	if err := dest.client.Get(dest.ctx, *destKey, destObj); err != nil {
		if !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("failed to get destination object: %w", err)
		}

		// signal to the syncer that the destination object doesn't exist
		destObj = nil
	}

	// Synchronize objects the same way the parent object was synchronized.

	sourceSide := syncSide{
		ctx:         source.ctx,
		clusterName: source.clusterName,
		client:      source.client,
		object:      sourceObj,
	}

	destSide := syncSide{
		ctx:         dest.ctx,
		clusterName: dest.clusterName,
		client:      dest.client,
		object:      destObj,
	}

	syncer := objectSyncer{
		// Related objects within kcp are not labelled with the agent name because it's unnecessary.
		// agentName: "",
		// use the same state store as we used for the main resource, to keep everything contained
		// in one place, on the service cluster side
		stateStore: stateStore,
		// how to create a new destination object
		destCreator: func(source *unstructured.Unstructured) *unstructured.Unstructured {
			dest := source.DeepCopy()
			dest.SetName(destKey.Name)
			dest.SetNamespace(destKey.Namespace)

			return dest
		},
		// ConfigMaps and Secrets have no subresources
		subresources: nil,
		// only sync the status back if the object originates in kcp,
		// as the service side should never have to rely on new status infos coming
		// from the kcp side
		syncStatusBack: relRes.Origin == "kcp",
		// if the origin is on the remote side, we want to add a finalizer to make
		// sure we can clean up properly
		blockSourceDeletion: relRes.Origin == "kcp",
		// apply mutation rules configured for the related resource
		mutator: mutation.NewMutator(relRes.Mutation),
	}

	requeue, err = syncer.Sync(log, sourceSide, destSide)
	if err != nil {
		return false, fmt.Errorf("failed to sync related object: %w", err)
	}

	if requeue {
		return true, nil
	}

	// now that the related object was successfully synced, we can remember its details on the
	// main object
	if relRes.Origin == "service" {
		annotation := relatedObjectAnnotationPrefix + relRes.Identifier

		value, err := json.Marshal(relatedObjectAnnotation{
			Namespace:  destKey.Namespace,
			Name:       destKey.Name,
			APIVersion: "v1", // we only support ConfigMaps and Secrets
			Kind:       relRes.Kind,
		})
		if err != nil {
			return false, fmt.Errorf("failed to encode related object annotation: %w", err)
		}

		annotations := remote.object.GetAnnotations()
		existing := annotations[annotation]

		if existing != string(value) {
			oldState := remote.object.DeepCopy()

			annotations[annotation] = string(value)
			remote.object.SetAnnotations(annotations)

			log.Debug("Remembering related object in main object…")
			if err := remote.client.Patch(remote.ctx, remote.object, ctrlruntimeclient.MergeFrom(oldState)); err != nil {
				return false, fmt.Errorf("failed to update related data in remote object: %w", err)
			}

			// requeue
			return true, nil
		}
	}

	return false, nil
}

func resolveResourceReference(obj *unstructured.Unstructured, ref syncagentv1alpha1.RelatedResourceReference) (*ctrlruntimeclient.ObjectKey, error) {
	jsonData, err := obj.MarshalJSON()
	if err != nil {
		return nil, err
	}

	name, err := resolveResourceLocator(string(jsonData), ref.Name)
	if err != nil {
		return nil, fmt.Errorf("cannot determine name: %w", err)
	}

	namespace := obj.GetNamespace()
	if ref.Namespace != nil {
		namespace, err = resolveResourceLocator(string(jsonData), *ref.Namespace)
		if err != nil {
			return nil, fmt.Errorf("cannot determine namespace: %w", err)
		}
	}

	return &types.NamespacedName{
		Namespace: namespace,
		Name:      name,
	}, nil
}

func resolveResourceLocator(jsonData string, loc syncagentv1alpha1.ResourceLocator) (string, error) {
	gval := gjson.Get(jsonData, loc.Path)
	if !gval.Exists() {
		return "", fmt.Errorf("cannot find %s in document", loc.Path)
	}

	if re := loc.Regex; re != nil {
		if re.Pattern == "" {
			return re.Replacement, nil
		}

		expr, err := regexp.Compile(re.Pattern)
		if err != nil {
			return "", fmt.Errorf("invalid pattern %q: %w", re.Pattern, err)
		}

		// this does apply some coalescing, like turning numbers into strings
		strVal := gval.String()

		return expr.ReplaceAllString(strVal, re.Replacement), nil
	}

	return gval.String(), nil
}
