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
	"errors"
	"fmt"
	"regexp"

	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	"github.com/kcp-dev/api-syncagent/internal/mutation"
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	// find the source object by applying the ResourceSourceSpec
	sourceObj, err := resolveResourceSource(source, relRes)
	if err != nil {
		return false, fmt.Errorf("failed to get source object: %w", err)
	}

	// the source object doesn't exist yet, so we can just stop
	if sourceObj == nil {
		return false, nil
	}

	// do the same to find the destination object
	destKey, err := resolveResourceDestination(dest, relRes)
	if err != nil {
		return false, fmt.Errorf("failed to determine related object's destination key: %w", err)
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
		// we never want to store sync-related metadata inside kcp
		metadataOnDestination: false,
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

func resolveResourceSource(side syncSide, relRes syncagentv1alpha1.RelatedResourceSpec) (*unstructured.Unstructured, error) {
	jsonData, err := side.object.MarshalJSON()
	if err != nil {
		return nil, err
	}

	// resolving the namespace first allows us to scope down any .List() calls
	// for the name of the object
	namespace := side.object.GetNamespace()
	if relRes.Source.Namespace != nil {
		namespace, err = resolveResourceSourceNamespace(side, string(jsonData), *relRes.Source.Namespace)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve namespace: %w", err)
		}

		if namespace == "" {
			return nil, nil
		}
	} else if namespace == "" {
		return nil, errors.New("primary object is cluster-scoped and no source namespace configuration was provided")
	}

	obj, err := resolveResourceSourceName(side, string(jsonData), relRes, relRes.Source.RelatedResourceSourceSpec, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve: %w", err)
	}

	return obj, nil
}

func resolveResourceSourceNamespace(side syncSide, jsonData string, spec syncagentv1alpha1.RelatedResourceSourceSpec) (string, error) {
	switch {
	case spec.Reference != nil:
		return resolveResourceReference(jsonData, *spec.Reference)

	case spec.Selector != nil:
		namespaces := &corev1.NamespaceList{}

		selector, err := metav1.LabelSelectorAsSelector(&spec.Selector.LabelSelector)
		if err != nil {
			return "", fmt.Errorf("invalid selector configured: %w", err)
		}

		opts := &ctrlruntimeclient.ListOptions{
			LabelSelector: selector,
			Limit:         2,
		}

		if err := side.client.List(side.ctx, namespaces, opts); err != nil {
			return "", fmt.Errorf("failed to evaluate label selector: %w", err)
		}

		switch len(namespaces.Items) {
		case 0:
			// it's okay if the source namespace, and therefore the source related object, doesn't exist (yet)
			return "", nil
		case 1:
			return namespaces.Items[0].Name, nil
		default:
			return "", fmt.Errorf("expected one namespace, but found %d matching the label selector", len(namespaces.Items))
		}

	case spec.Expression != "":
		return "", errors.New("not yet implemented")

	default:
		return "", errors.New("invalid sourceSpec: no mechanism configured")
	}
}

func resolveResourceSourceName(side syncSide, jsonData string, relRes syncagentv1alpha1.RelatedResourceSpec, spec syncagentv1alpha1.RelatedResourceSourceSpec, namespace string) (*unstructured.Unstructured, error) {
	switch {
	case spec.Reference != nil:
		name, err := resolveResourceReference(jsonData, *spec.Reference)
		if err != nil {
			return nil, err
		}

		// we assume an operator on the service side will fill-in this value later
		if name == "" {
			return nil, nil
		}

		// find the source related object
		sourceObj := &unstructured.Unstructured{}
		sourceObj.SetAPIVersion("v1") // we only support ConfigMaps and Secrets, both are in core/v1
		sourceObj.SetKind(relRes.Kind)

		err = side.client.Get(side.ctx, types.NamespacedName{Name: name, Namespace: namespace}, sourceObj)
		if err != nil {
			// the source object doesn't exist yet, so we can just stop
			if apierrors.IsNotFound(err) {
				return nil, nil
			}

			return nil, fmt.Errorf("failed to get source object: %w", err)
		}

		return sourceObj, nil

	case spec.Selector != nil:
		sourceObjs := &unstructured.UnstructuredList{}
		sourceObjs.SetAPIVersion("v1") // we only support ConfigMaps and Secrets, both are in core/v1
		sourceObjs.SetKind(relRes.Kind)

		selector, err := metav1.LabelSelectorAsSelector(&spec.Selector.LabelSelector)
		if err != nil {
			return nil, fmt.Errorf("invalid selector configured: %w", err)
		}

		opts := &ctrlruntimeclient.ListOptions{
			LabelSelector: selector,
			Limit:         2,
			Namespace:     namespace,
		}

		if err := side.client.List(side.ctx, sourceObjs, opts); err != nil {
			return nil, fmt.Errorf("failed to evaluate label selector: %w", err)
		}

		switch len(sourceObjs.Items) {
		case 0:
			// it's okay if the source object doesn't exist (yet)
			return nil, nil
		case 1:
			return &sourceObjs.Items[0], nil
		default:
			return nil, fmt.Errorf("expected one %s object, but found %d matching the label selector", relRes.Kind, len(sourceObjs.Items))
		}

	case spec.Expression != "":
		return nil, errors.New("not yet implemented")

	default:
		return nil, errors.New("invalid sourceSpec: no mechanism configured")
	}
}

func resolveResourceDestination(side syncSide, relRes syncagentv1alpha1.RelatedResourceSpec) (*types.NamespacedName, error) {
	jsonData, err := side.object.MarshalJSON()
	if err != nil {
		return nil, err
	}

	namespace := side.object.GetNamespace()
	if relRes.Destination.Namespace != nil {
		namespace, err = resolveResourceDestinationSpec(string(jsonData), *relRes.Destination.Namespace)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve namespace: %w", err)
		}

		if namespace == "" {
			return nil, nil
		}
	} else if namespace == "" {
		return nil, errors.New("primary object is cluster-scoped and no source namespace configuration was provided")
	}

	name, err := resolveResourceDestinationSpec(string(jsonData), relRes.Destination.RelatedResourceDestinationSpec)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve name: %w", err)
	}

	if name == "" {
		return nil, nil
	}

	return &types.NamespacedName{
		Namespace: namespace,
		Name:      name,
	}, nil
}

func resolveResourceDestinationSpec(jsonData string, spec syncagentv1alpha1.RelatedResourceDestinationSpec) (string, error) {
	switch {
	case spec.Reference != nil:
		return resolveResourceReference(jsonData, *spec.Reference)

	case spec.Expression != "":
		return "", errors.New("not yet implemented")

	default:
		return "", errors.New("invalid sourceSpec: no mechanism configured")
	}
}

func resolveResourceReference(jsonData string, ref syncagentv1alpha1.RelatedResourceReference) (string, error) {
	gval := gjson.Get(jsonData, ref.Path)
	if !gval.Exists() {
		return "", fmt.Errorf("cannot find %s in document", ref.Path)
	}

	if re := ref.Regex; re != nil {
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
