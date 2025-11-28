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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	"github.com/kcp-dev/api-syncagent/internal/projection"
	"github.com/kcp-dev/api-syncagent/internal/sync/templating"
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func (s *ResourceSyncer) processRelatedResources(ctx context.Context, log *zap.SugaredLogger, stateStore ObjectStateStore, remote, local syncSide) (requeue bool, err error) {
	for _, relatedResource := range s.pubRes.Spec.Related {
		requeue, err := s.processRelatedResource(ctx, log.With("identifier", relatedResource.Identifier), stateStore, remote, local, relatedResource)
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

func (s *ResourceSyncer) processRelatedResource(ctx context.Context, log *zap.SugaredLogger, stateStore ObjectStateStore, remote, local syncSide, relRes syncagentv1alpha1.RelatedResourceSpec) (requeue bool, err error) {
	// decide what direction to sync (local->remote vs. remote->local)
	var (
		origin       syncSide
		dest         syncSide
		eventObjSide syncSideType
	)

	if relRes.Origin == syncagentv1alpha1.RelatedResourceOriginService {
		origin = local
		dest = remote
		eventObjSide = syncSideDestination
	} else {
		origin = remote
		dest = local
		eventObjSide = syncSideSource
	}

	// find the all objects on the origin side that match the given criteria
	resolvedObjects, err := resolveRelatedResourceObjects(ctx, origin, dest, relRes)
	if err != nil {
		return false, fmt.Errorf("failed to get resolve origin objects: %w", err)
	}

	// no objects were found yet, that's okay
	if len(resolvedObjects) == 0 {
		return false, nil
	}

	slices.SortStableFunc(resolvedObjects, func(a, b resolvedObject) int {
		aKey := ctrlruntimeclient.ObjectKeyFromObject(a.original).String()
		bKey := ctrlruntimeclient.ObjectKeyFromObject(b.original).String()

		return strings.Compare(aKey, bKey)
	})

	// Synchronize related objects the same way the parent object was synchronized.
	projectedGVR := projection.RelatedResourceProjectedGVR(&relRes)

	projectedGVK, err := dest.client.RESTMapper().KindFor(projectedGVR)
	if err != nil {
		return false, fmt.Errorf("failed to lookup %v: %w", projectedGVR, err)
	}

	for idx, resolved := range resolvedObjects {
		destObject := &unstructured.Unstructured{}
		destObject.SetAPIVersion(projectedGVK.GroupVersion().String())
		destObject.SetKind(projectedGVK.Kind)

		if err = dest.client.Get(ctx, resolved.destination, destObject); err != nil {
			destObject = nil
		}

		sourceSide := syncSide{
			clusterName: origin.clusterName,
			client:      origin.client,
			object:      resolved.original,
		}

		destSide := syncSide{
			clusterName: dest.clusterName,
			client:      dest.client,
			object:      destObject,
		}

		syncer := objectSyncer{
			// Related objects within kcp are not labelled with the agent name because it's unnecessary.
			// agentName: "",
			// use the same state store as we used for the main resource, to keep everything contained
			// in one place, on the service cluster side
			stateStore: stateStore,
			// how to create a new destination object
			destCreator: func(source *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				dest := source.DeepCopy()
				dest.SetAPIVersion(projectedGVK.GroupVersion().String())
				dest.SetKind(projectedGVK.Kind)
				dest.SetName(resolved.destination.Name)
				dest.SetNamespace(resolved.destination.Namespace)

				return dest, nil
			},
			// reloated resources have no subresources
			subresources: nil,
			// only sync the status back if the object originates in kcp,
			// as the service side should never have to rely on new status infos coming
			// from the kcp side
			syncStatusBack: relRes.Origin == "kcp",
			// if the origin is on the remote side, we want to add a finalizer to make
			// sure we can clean up properly
			blockSourceDeletion: relRes.Origin == "kcp",
			// apply mutation rules configured for the related resource
			mutator: s.relatedMutators[relRes.Identifier],
			// we never want to store sync-related metadata inside kcp
			metadataOnDestination: false,
			// events are always created on the kcp side
			eventObjSide: eventObjSide,
		}

		req, err := syncer.Sync(ctx, log, sourceSide, destSide)
		if err != nil {
			return false, fmt.Errorf("failed to sync related object: %w", err)
		}

		// Updating a related object should not immediately trigger a requeue,
		// but only after all related objects are done. This is purely to not perform
		// too many unnecessary requeues.
		requeue = requeue || req

		// now that the related object was successfully synced, we can remember its details on the
		// main object
		if relRes.Origin == syncagentv1alpha1.RelatedResourceOriginService {
			// TODO: Improve this logic, the added index is just a hack until we find a better solution
			// to let the user know about the related object (this annotation is not relevant for the
			// syncing logic, it's purely for the end-user).
			annotation := fmt.Sprintf("%s%s.%d", relatedObjectAnnotationPrefix, relRes.Identifier, idx)

			value, err := json.Marshal(relatedObjectAnnotation{
				Namespace:  resolved.destination.Namespace,
				Name:       resolved.destination.Name,
				APIVersion: resolved.original.GetAPIVersion(),
				Kind:       resolved.original.GetKind(),
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
				if err := remote.client.Patch(ctx, remote.object, ctrlruntimeclient.MergeFrom(oldState)); err != nil {
					return false, fmt.Errorf("failed to update related data in remote object: %w", err)
				}

				// requeue (since this updated the main object, we do actually want to
				// requeue immediately because successive patches would fail anyway)
				return true, nil
			}
		}
	}

	return requeue, nil
}

// resolvedObject is the result of following the configuration of a related resources. It contains
// the original object (on the origin side of the related resource) and the target name to be used
// on the destination side of the sync.
type resolvedObject struct {
	original    *unstructured.Unstructured
	destination types.NamespacedName
}

func resolveRelatedResourceObjects(ctx context.Context, relatedOrigin, relatedDest syncSide, relRes syncagentv1alpha1.RelatedResourceSpec) ([]resolvedObject, error) {
	// resolving the originNamespace first allows us to scope down any .List() calls later
	originNamespace := relatedOrigin.object.GetNamespace()
	destNamespace := relatedDest.object.GetNamespace()
	origin := relRes.Origin

	namespaceMap := map[string]string{
		originNamespace: destNamespace,
	}

	if nsSpec := relRes.Object.Namespace; nsSpec != nil {
		var err error
		namespaceMap, err = resolveRelatedResourceOriginNamespaces(ctx, relatedOrigin, relatedDest, origin, *nsSpec)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve namespace: %w", err)
		}

		if len(namespaceMap) == 0 {
			return nil, nil
		}
	} else if originNamespace == "" {
		return nil, errors.New("primary object is cluster-scoped and no source namespace configuration was provided")
	} else if destNamespace == "" {
		return nil, errors.New("primary object copy is cluster-scoped and no source namespace configuration was provided")
	}

	// At this point we know all the namespaces in which can look for related objects.
	// For all but the label selector-based specs, this map will have exactly 1 element, otherwise
	// more. Empty maps are not possible at this point.
	// The namespace map contains a mapping from origin side to destination side.
	// Armed with this, we can now resolve the object names and thereby find all objects that match
	// this related resource configuration. Again, for label selectors this can be multiple,
	// otherwise at most 1.

	objects, err := resolveRelatedResourceObjectsInNamespaces(ctx, relatedOrigin, relatedDest, relRes, relRes.Object.RelatedResourceObjectSpec, namespaceMap)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve objects: %w", err)
	}

	return objects, nil
}

func resolveRelatedResourceOriginNamespaces(ctx context.Context, relatedOrigin, relatedDest syncSide, origin syncagentv1alpha1.RelatedResourceOrigin, spec syncagentv1alpha1.RelatedResourceObjectSpec) (map[string]string, error) {
	switch {
	case spec.Reference != nil:
		originNamespaces, err := resolveObjectReference(relatedOrigin.object, *spec.Reference)
		if err != nil {
			return nil, err
		}

		if len(originNamespaces) == 0 {
			return nil, nil
		}

		destNamespaces, err := resolveObjectReference(relatedDest.object, *spec.Reference)
		if err != nil {
			return nil, err
		}

		if len(destNamespaces) != len(originNamespaces) {
			return nil, fmt.Errorf("cannot sync related resources: found %d namespaces on the origin, but %d on the destination side", len(originNamespaces), len(destNamespaces))
		}

		return mapSlices(originNamespaces, destNamespaces), nil

	case spec.Selector != nil:
		namespaces := &corev1.NamespaceList{}

		labelSelector, err := templateLabelSelector(relatedOrigin, relatedDest, origin, &spec.Selector.LabelSelector)
		if err != nil {
			return nil, fmt.Errorf("failed to apply templates to label selector: %w", err)
		}

		selector, err := metav1.LabelSelectorAsSelector(labelSelector)
		if err != nil {
			return nil, fmt.Errorf("invalid selector configured: %w", err)
		}

		opts := &ctrlruntimeclient.ListOptions{
			LabelSelector: selector,
		}

		if err := relatedOrigin.client.List(ctx, namespaces, opts); err != nil {
			return nil, fmt.Errorf("failed to evaluate label selector: %w", err)
		}

		namespaceMap := map[string]string{}
		for _, namespace := range namespaces.Items {
			name := namespace.Name

			destinationName, err := applySelectorRewrites(relatedOrigin, relatedDest, origin, name, nil, spec.Selector.Rewrite)
			if err != nil {
				return nil, fmt.Errorf("failed to rewrite origin namespace: %w", err)
			}

			namespaceMap[name] = destinationName
		}

		return namespaceMap, nil

	case spec.Template != nil:
		originValue, destValue, err := applyTemplateBothSides(relatedOrigin, relatedDest, origin, *spec.Template)
		if err != nil {
			return nil, fmt.Errorf("failed to apply template: %w", err)
		}

		if originValue == "" || destValue == "" {
			return nil, nil
		}

		return map[string]string{
			originValue: destValue,
		}, nil

	default:
		return nil, errors.New("invalid sourceSpec: no mechanism configured")
	}
}

func mapSlices(a, b []string) map[string]string {
	mapping := map[string]string{}
	for i, aItem := range a {
		bItem := b[i]

		// ignore any origin<->dest pair where either of the sides is empty
		if bItem == "" || aItem == "" {
			continue
		}

		mapping[aItem] = bItem
	}

	return mapping
}

func resolveRelatedResourceObjectsInNamespaces(ctx context.Context, relatedOrigin, relatedDest syncSide, relRes syncagentv1alpha1.RelatedResourceSpec, spec syncagentv1alpha1.RelatedResourceObjectSpec, namespaceMap map[string]string) ([]resolvedObject, error) {
	result := []resolvedObject{}

	for originNamespace, destNamespace := range namespaceMap {
		nameMap, err := resolveRelatedResourceObjectsInNamespace(ctx, relatedOrigin, relatedDest, relRes, spec, originNamespace)
		if err != nil {
			return nil, fmt.Errorf("failed to find objects on origin side: %w", err)
		}

		for originName, destName := range nameMap {
			originGVR := projection.RelatedResourceGVR(&relRes)

			originGVK, err := relatedOrigin.client.RESTMapper().KindFor(originGVR)
			if err != nil {
				return nil, fmt.Errorf("failed to lookup %v: %w", originGVR, err)
			}

			originObj := &unstructured.Unstructured{}
			originObj.SetAPIVersion(originGVK.GroupVersion().String())
			originObj.SetKind(originGVK.Kind)

			err = relatedOrigin.client.Get(ctx, types.NamespacedName{Name: originName, Namespace: originNamespace}, originObj)
			if err != nil {
				// this should rarely happen, only if an object was deleted in between the .List() call
				// above and the .Get() call here.
				if apierrors.IsNotFound(err) {
					continue
				}

				return nil, fmt.Errorf("failed to get origin object: %w", err)
			}

			result = append(result, resolvedObject{
				original: originObj,
				destination: types.NamespacedName{
					Namespace: destNamespace,
					Name:      destName,
				},
			})
		}
	}

	return result, nil
}

func resolveRelatedResourceObjectsInNamespace(ctx context.Context, relatedOrigin, relatedDest syncSide, relRes syncagentv1alpha1.RelatedResourceSpec, spec syncagentv1alpha1.RelatedResourceObjectSpec, namespace string) (map[string]string, error) {
	switch {
	case spec.Reference != nil:
		originNames, err := resolveObjectReference(relatedOrigin.object, *spec.Reference)
		if err != nil {
			return nil, err
		}

		if len(originNames) == 0 {
			return nil, nil
		}

		destNames, err := resolveObjectReference(relatedDest.object, *spec.Reference)
		if err != nil {
			return nil, err
		}

		if len(destNames) != len(originNames) {
			return nil, fmt.Errorf("cannot sync related resources: found %d names on the origin, but %d on the destination side", len(originNames), len(destNames))
		}

		return mapSlices(originNames, destNames), nil

	case spec.Selector != nil:
		originGVR := projection.RelatedResourceGVR(&relRes)

		originGVK, err := relatedOrigin.client.RESTMapper().KindFor(originGVR)
		if err != nil {
			return nil, fmt.Errorf("failed to lookup %v: %w", originGVR, err)
		}

		originObjects := &unstructured.UnstructuredList{}
		originObjects.SetAPIVersion(originGVK.GroupVersion().String())
		originObjects.SetKind(originGVK.Kind)

		labelSelector, err := templateLabelSelector(relatedOrigin, relatedDest, relRes.Origin, &spec.Selector.LabelSelector)
		if err != nil {
			return nil, fmt.Errorf("failed to apply templates to label selector: %w", err)
		}

		selector, err := metav1.LabelSelectorAsSelector(labelSelector)
		if err != nil {
			return nil, fmt.Errorf("invalid selector configured: %w", err)
		}

		opts := &ctrlruntimeclient.ListOptions{
			LabelSelector: selector,
			Namespace:     namespace,
		}

		if err := relatedOrigin.client.List(ctx, originObjects, opts); err != nil {
			return nil, fmt.Errorf("failed to select origin objects based on label selector: %w", err)
		}

		nameMap := map[string]string{}
		for _, originObject := range originObjects.Items {
			name := originObject.GetName()

			destinationName, err := applySelectorRewrites(relatedOrigin, relatedDest, relRes.Origin, name, &originObject, spec.Selector.Rewrite)
			if err != nil {
				return nil, fmt.Errorf("failed to rewrite origin name: %w", err)
			}

			nameMap[name] = destinationName
		}

		return nameMap, nil

	case spec.Template != nil:
		originValue, destValue, err := applyTemplateBothSides(relatedOrigin, relatedDest, relRes.Origin, *spec.Template)
		if err != nil {
			return nil, fmt.Errorf("failed to apply template: %w", err)
		}

		if originValue == "" || destValue == "" {
			return nil, nil
		}

		return map[string]string{
			originValue: destValue,
		}, nil

	default:
		return nil, errors.New("invalid objectSpec: no mechanism configured")
	}
}

func resolveObjectReference(object *unstructured.Unstructured, ref syncagentv1alpha1.RelatedResourceObjectReference) ([]string, error) {
	data, err := object.MarshalJSON()
	if err != nil {
		return nil, err
	}

	return resolveReference(data, ref)
}

func resolveReference(jsonData []byte, ref syncagentv1alpha1.RelatedResourceObjectReference) ([]string, error) {
	result := gjson.Get(string(jsonData), ref.Path)
	if !result.Exists() {
		return nil, nil
	}

	var values []string
	if result.IsArray() {
		for _, elem := range result.Array() {
			values = append(values, strings.TrimSpace(elem.String()))
		}
	} else {
		values = append(values, strings.TrimSpace(result.String()))
	}

	if re := ref.Regex; re != nil {
		var err error

		for i, value := range values {
			value, err = applyRegularExpression(value, *re)
			if err != nil {
				return nil, err
			}

			values[i] = value
		}
	}

	return values, nil
}

// applyTemplate is used after a label selector has been applied and a list of namespaces or objects
// has been selected. To map these to the destination side, rewrites can be applied, and these are
// first applied to all found namespaces (in which case, the value parameter here is the namespace
// name and originRelatedObject is nil) and then again to all found objects (in which case the value
// parameter is the object's name and originRelatedObject is set). In both cases the rewrite is supposed
// to return a string.
func applySelectorRewrites(relatedOrigin, relatedDest syncSide, origin syncagentv1alpha1.RelatedResourceOrigin, value string, originRelatedObject *unstructured.Unstructured, rewrite syncagentv1alpha1.RelatedResourceSelectorRewrite) (string, error) {
	switch {
	case rewrite.Regex != nil:
		return applyRegularExpression(value, *rewrite.Regex)
	case rewrite.Template != nil:
		return applyTemplate(relatedOrigin, relatedDest, origin, *rewrite.Template, value, originRelatedObject)
	default:
		return "", errors.New("invalid rewrite: no mechanism configured")
	}
}

func applyRegularExpression(value string, re syncagentv1alpha1.RegularExpression) (string, error) {
	if re.Pattern == "" {
		return re.Replacement, nil
	}

	expr, err := regexp.Compile(re.Pattern)
	if err != nil {
		return "", fmt.Errorf("invalid pattern %q: %w", re.Pattern, err)
	}

	return expr.ReplaceAllString(value, re.Replacement), nil
}

func applyTemplate(relatedOrigin, relatedDest syncSide, origin syncagentv1alpha1.RelatedResourceOrigin, tpl syncagentv1alpha1.TemplateExpression, value string, originRelatedObject *unstructured.Unstructured) (string, error) {
	localSide, remoteSide := remapSyncSides(relatedOrigin, relatedDest, origin)
	ctx := templating.NewRelatedObjectLabelRewriteContext(value, localSide.object, remoteSide.object, originRelatedObject, remoteSide.clusterName, remoteSide.workspacePath)

	return templating.Render(tpl.Template, ctx)
}

func applyTemplateBothSides(relatedOrigin, relatedDest syncSide, origin syncagentv1alpha1.RelatedResourceOrigin, tpl syncagentv1alpha1.TemplateExpression) (originValue, destValue string, err error) {
	_, remoteSide := remapSyncSides(relatedOrigin, relatedDest, origin)

	// evaluate the template for the origin object side
	ctx := templating.NewRelatedObjectContext(relatedOrigin.object, origin, remoteSide.clusterName, remoteSide.workspacePath)
	originValue, err = templating.Render(tpl.Template, ctx)
	if err != nil {
		return "", "", fmt.Errorf("failed to evaluate template on origin side: %w", err)
	}

	// and once more on the other side
	ctx = templating.NewRelatedObjectContext(relatedDest.object, oppositeSide(origin), remoteSide.clusterName, remoteSide.workspacePath)
	destValue, err = templating.Render(tpl.Template, ctx)
	if err != nil {
		return "", "", fmt.Errorf("failed to evaluate template on destination side: %w", err)
	}

	return originValue, destValue, nil
}

// templateLabelSelector applies Go templating logic to all keys and values in the MatchLabels of
// a label selector.
func templateLabelSelector(relatedOrigin, relatedDest syncSide, origin syncagentv1alpha1.RelatedResourceOrigin, selector *metav1.LabelSelector) (*metav1.LabelSelector, error) {
	localSide, remoteSide := remapSyncSides(relatedOrigin, relatedDest, origin)

	ctx := templating.NewRelatedObjectLabelContext(localSide.object, remoteSide.object, remoteSide.clusterName, remoteSide.workspacePath)

	newMatchLabels := map[string]string{}
	for key, value := range selector.MatchLabels {
		if strings.Contains(key, "{{") {
			rendered, err := templating.Render(key, ctx)
			if err != nil {
				return nil, fmt.Errorf("failed to evaluate key as template: %w", err)
			}

			key = rendered
		}

		if strings.Contains(value, "{{") {
			rendered, err := templating.Render(value, ctx)
			if err != nil {
				return nil, fmt.Errorf("failed to evaluate value as template: %w", err)
			}

			value = rendered
		}

		if key != "" {
			newMatchLabels[key] = value
		}
	}

	selector.MatchLabels = newMatchLabels

	return selector, nil
}

func remapSyncSides(relatedOrigin, relatedDest syncSide, origin syncagentv1alpha1.RelatedResourceOrigin) (localSide, remoteSide syncSide) {
	if origin == syncagentv1alpha1.RelatedResourceOriginKcp {
		return relatedDest, relatedOrigin
	}

	return relatedOrigin, relatedDest
}

func oppositeSide(origin syncagentv1alpha1.RelatedResourceOrigin) syncagentv1alpha1.RelatedResourceOrigin {
	if origin == syncagentv1alpha1.RelatedResourceOriginKcp {
		return syncagentv1alpha1.RelatedResourceOriginService
	}

	return syncagentv1alpha1.RelatedResourceOriginKcp
}
