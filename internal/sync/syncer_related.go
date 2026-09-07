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
	"maps"
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
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func (s *ResourceSyncer) processRelatedResources(ctx context.Context, log *zap.SugaredLogger, stateStore ObjectStateStore, remote, local syncSide, primaryDeleting bool) (requeue bool, err error) {
	for _, relatedResource := range s.pubRes.Spec.Related {
		requeue, err := s.processRelatedResource(ctx, log.With("identifier", relatedResource.Identifier), stateStore, remote, local, relatedResource, primaryDeleting)
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

func (s *ResourceSyncer) processRelatedResource(ctx context.Context, log *zap.SugaredLogger, stateStore ObjectStateStore, remote, local syncSide, relRes syncagentv1alpha1.RelatedResourceSpec, primaryDeleting bool) (requeue bool, err error) {
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

	// normalize the deprecated cleanup bool and the cleanup policy field into a single policy.
	policy := relRes.EffectiveCleanupPolicy()

	// find the all objects on the origin side that match the given criteria
	resolvedObjects, err := resolveRelatedResourceObjects(ctx, log, origin, dest, relRes)
	if err != nil {
		return false, fmt.Errorf("failed to get resolve origin objects: %w", err)
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

	// The primary object (always the kcp side) owns these related copies; stamp its coordinates
	// onto every copy so that all copies of this (primary, identifier) can be enumerated later to
	// prune stale copies or tear them all down.
	primary := remote.object
	destLabels := relatedCopyLabels(primary, remote.clusterName, s.pubRes.Name, relRes.Identifier, s.agentName)
	destAnnotations := relatedCopyAnnotations(primary)

	// remember which destination copies we (re)synced this pass, so a MatchOrigin prune can delete
	// the copies that no longer have a matching origin object.
	synced := sets.New[string]()
	deletionPolicies := map[string]metav1.DeletionPropagation{}

	// We "forward" the deletion to the related objects only if the primary is already in deletion
	// and the related object either originated from the user (so on the service cluster we just
	// have a useless copy once the main object has been cleared up) OR the admin explicitly opted
	// into a cleanup policy that removes copies on primary deletion.
	forceDelete := primaryDeleting &&
		(policy == syncagentv1alpha1.RelatedResourceCleanupPolicyOnPrimaryDeletion ||
			policy == syncagentv1alpha1.RelatedResourceCleanupPolicyMatchOrigin ||
			relRes.Origin == syncagentv1alpha1.RelatedResourceOriginKcp)

	for _, resolved := range resolvedObjects {
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

		deletionPolicy := relatedDeletionPropagationPolicy(relRes.Origin, resolved.original)
		deletionPolicies[relatedCopyKey(resolved.destination.Namespace, resolved.destination.Name)] = deletionPolicy

		// When status sync is enabled, include "status" in subresources so it is stripped from
		// the spec patch (avoiding a no-op write on resources that have a status subresource).
		// The status is then separately written via the status subresource endpoint by syncStatusForward.
		relatedSubresources := []string(nil)
		if relRes.SyncStatus {
			relatedSubresources = []string{"status"}
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
			subresources:      relatedSubresources,
			syncStatusBack:    false,
			syncStatusForward: relRes.SyncStatus,
			// if the origin is on the remote side, we want to add a finalizer to make
			// sure we can clean up properly; when forceDelete is enabled, we are in deletion mode and
			// want to force the deletion, so we ignore the related object's origin (it was taken into
			// account when defining forceDelete).
			blockSourceDeletion: forceDelete || relRes.Origin == syncagentv1alpha1.RelatedResourceOriginKcp,
			// apply mutation rules configured for the related resource
			mutator: s.relatedMutators[relRes.Identifier],
			// we never want to store sync-related metadata inside kcp
			metadataOnDestination: false,
			// stamp owning-primary provenance so that the copies can be enumerated for pruning.
			destLabels:      destLabels,
			destAnnotations: destAnnotations,
			// events are always created on the kcp side
			eventObjSide: eventObjSide,
			// force deletion of related resources when the primary object is being deleted
			forceDelete: forceDelete,
			// Kcp-origin objects can request how their service-cluster copies are deleted.
			deletionPropagationPolicy: deletionPolicy,
			// propagate the SSA mode chosen for the primary syncer to keep
			// behavior consistent across the whole resource graph
			useServerSideApply: s.useServerSideApply,
		}

		req, err := syncer.Sync(ctx, log, sourceSide, destSide)
		if err != nil {
			return false, fmt.Errorf("failed to sync related object: %w", err)
		}

		// Updating a related object should not immediately trigger a requeue,
		// but only after all related objects are done. This is purely to not perform
		// too many unnecessary requeues.
		requeue = requeue || req

		synced.Insert(relatedCopyKey(resolved.destination.Namespace, resolved.destination.Name))
	}

	// Remember the related objects on the primary object for the end-user. This is rebuilt from the
	// freshly-resolved set so entries for objects that no longer resolve are dropped; a fully-empty
	// resolution is left untouched (see rememberRelatedObjects) to avoid churning the primary.
	if relRes.Origin == syncagentv1alpha1.RelatedResourceOriginService {
		annRequeue, err := s.rememberRelatedObjects(ctx, log, remote, relRes.Identifier, resolvedObjects)
		if err != nil {
			return false, err
		}

		// we updated the main object, so we requeue immediately because successive patches would
		// fail anyway; the prune below then runs on the next reconciliation.
		if annRequeue {
			return true, nil
		}
	}

	// Prune / teardown destination copies as configured. Only objects carrying our provenance
	// labels are ever considered, and we only ever act on the destination client, so the original
	// origin-side objects are never touched.
	switch {
	case primaryDeleting && (policy == syncagentv1alpha1.RelatedResourceCleanupPolicyOnPrimaryDeletion ||
		policy == syncagentv1alpha1.RelatedResourceCleanupPolicyMatchOrigin):
		// On primary teardown, delete ALL labelled copies. This is a superset of the per-object
		// deletion performed in the loop above and additionally reclaims copies whose origin object
		// had already disappeared mid-life (which the loop can no longer resolve).
		selector := relatedCopySelector(primary, remote.clusterName, s.pubRes.Name, relRes.Identifier, s.agentName)

		pruneRequeue, err := s.pruneRelatedCopies(ctx, log, dest, primary, projectedGVK, selector, nil, deletionPolicies, true)
		if err != nil {
			return false, fmt.Errorf("failed to tear down related copies: %w", err)
		}

		requeue = requeue || pruneRequeue

	case !primaryDeleting && policy == syncagentv1alpha1.RelatedResourceCleanupPolicyMatchOrigin:
		// Keep the destination set equal to the origin set: prune every labelled copy whose origin
		// object was not resolved this pass.
		selector := relatedCopySelector(primary, remote.clusterName, s.pubRes.Name, relRes.Identifier, s.agentName)

		// A fully-empty resolution here would prune *every* copy at once, because the keep set is
		// empty. resolvedObjects comes from a cache-backed read, and a relisting or otherwise stale
		// informer -- notably a kcp virtual-workspace cache -- can momentarily return nothing even
		// though the origin objects still exist. Acting on that would delete and then immediately
		// recreate all copies, disrupting consumers (e.g. Secrets mounted by workloads). The
		// non-destructive annotation bookkeeping already refuses to act on an empty resolution for
		// the same reason (see rememberRelatedObjects); the destructive prune must be at least as
		// careful. Before deleting the whole set, re-confirm the emptiness against a live read; if
		// the live read disagrees, requeue and let the cache converge. A genuinely empty origin is
		// still pruned, so the single-object mid-life case the feature promises keeps working.
		//
		// Partial under-resolution (a non-empty but incomplete set) is intentionally not guarded:
		// it prunes at most a subset and self-heals on the next pass, matching the cache semantics
		// the rest of the sync already relies on.
		if len(resolvedObjects) == 0 {
			confirmedEmpty, checked, err := s.confirmOriginEmpty(ctx, origin, dest, relRes)
			if err != nil {
				return false, fmt.Errorf("failed to confirm empty origin before pruning related copies: %w", err)
			}

			switch {
			case !checked:
				// No live reader configured to confirm against (e.g. in unit tests). Do not risk
				// deleting the whole set on an unverified empty resolution; teardown still reclaims
				// the copies if the primary is ever deleted.
				log.Debug("Skipping full related-copy prune on empty resolution: no live reader configured to confirm it.")
				return requeue, nil

			case !confirmedEmpty:
				log.Warn("Skipping related-copy prune: origin resolved to no objects from cache, but a live read still sees origin objects; requeueing to let the cache converge.")
				return true, nil
			}
		}

		pruneRequeue, err := s.pruneRelatedCopies(ctx, log, dest, primary, projectedGVK, selector, synced, deletionPolicies, false)
		if err != nil {
			return false, fmt.Errorf("failed to prune related copies: %w", err)
		}

		requeue = requeue || pruneRequeue
	}

	return requeue, nil
}

// relatedCopyKey builds the namespace/name key used to track and match destination copies.
func relatedCopyKey(namespace, name string) string {
	return namespace + "/" + name
}

// rememberRelatedObjects writes the human-facing provenance annotations onto the primary object,
// one per related copy (indexed). It rebuilds the full set for the given identifier from the
// currently resolved objects, so annotations for objects that are no longer resolved are removed and
// do not accumulate. It reports requeue=true when it patched the primary object.
func (s *ResourceSyncer) rememberRelatedObjects(ctx context.Context, log *zap.SugaredLogger, remote syncSide, identifier string, resolvedObjects []resolvedObject) (requeue bool, err error) {
	// When nothing resolves there is nothing new to remember, and we deliberately do not treat this
	// as "clear all annotations for this identifier". These annotations are purely informational and
	// the prune is the authoritative cleanup for the copies themselves; wiping them on every empty
	// pass would otherwise churn the primary object with patches and requeues for all cleanup
	// policies, not just the pruning ones (this used to be avoided by an early return before the
	// resolved set was allowed to be empty so the prune could run).
	if len(resolvedObjects) == 0 {
		return false, nil
	}

	// TODO: Improve this logic, the added index is just a hack until we find a better solution to
	// let the user know about the related object (this annotation is not relevant for the syncing
	// logic, it's purely for the end-user).
	prefix := fmt.Sprintf("%s%s.", relatedObjectAnnotationPrefix, identifier)

	desired := map[string]string{}
	for idx, resolved := range resolvedObjects {
		value, err := json.Marshal(relatedObjectAnnotation{
			Namespace:  resolved.destination.Namespace,
			Name:       resolved.destination.Name,
			APIVersion: resolved.original.GetAPIVersion(),
			Kind:       resolved.original.GetKind(),
		})
		if err != nil {
			return false, fmt.Errorf("failed to encode related object annotation: %w", err)
		}

		desired[fmt.Sprintf("%s%d", prefix, idx)] = string(value)
	}

	annotations := remote.object.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}

	// determine whether the existing annotations for this identifier already match the desired set.
	changed := false
	for key := range annotations {
		if strings.HasPrefix(key, prefix) {
			if _, ok := desired[key]; !ok {
				changed = true
				break
			}
		}
	}
	if !changed {
		for key, value := range desired {
			if annotations[key] != value {
				changed = true
				break
			}
		}
	}

	if !changed {
		return false, nil
	}

	oldState := remote.object.DeepCopy()

	// drop all existing entries for this identifier, then add the freshly computed ones.
	for key := range annotations {
		if strings.HasPrefix(key, prefix) {
			delete(annotations, key)
		}
	}
	maps.Copy(annotations, desired)
	remote.object.SetAnnotations(annotations)

	log.Debug("Remembering related objects in main object…")
	if err := remote.client.Patch(ctx, remote.object, ctrlruntimeclient.MergeFrom(oldState)); err != nil {
		return false, fmt.Errorf("failed to update related data in remote object: %w", err)
	}

	return true, nil
}

// pruneRelatedCopies lists the destination copies matching the given (primary + identifier + agent)
// selector and deletes those that are no longer wanted. When deleteAll is true, every matching copy
// is deleted (primary teardown); otherwise only copies whose key is not in the keep set are deleted
// (mid-life prune). It only ever operates on the destination client, so origin objects are never
// touched, and it only ever sees objects that carry our provenance labels, so hand-created objects
// are never in scope.
func (s *ResourceSyncer) pruneRelatedCopies(ctx context.Context, log *zap.SugaredLogger, dest syncSide, primary *unstructured.Unstructured, projectedGVK schema.GroupVersionKind, selector labels.Selector, keep sets.Set[string], deletionPolicies map[string]metav1.DeletionPropagation, deleteAll bool) (requeue bool, err error) {
	list := &unstructured.UnstructuredList{}
	list.SetAPIVersion(projectedGVK.GroupVersion().String())
	list.SetKind(projectedGVK.Kind + "List")

	// List cluster-wide (across all namespaces). A related resource can map its copies into a
	// namespace other than the primary's (via spec.object.namespace rewrites), so scoping the List
	// to a single namespace would miss — and therefore never prune — copies that landed elsewhere.
	// The label selector already scopes the result to this primary + identifier + agent, so only
	// copies this agent created for this primary can match.
	listOpts := []ctrlruntimeclient.ListOption{
		ctrlruntimeclient.MatchingLabelsSelector{Selector: selector},
	}

	if err := dest.client.List(ctx, list, listOpts...); err != nil {
		return false, fmt.Errorf("failed to list related copies: %w", err)
	}

	recorder := recorderFromContext(ctx)

	for i := range list.Items {
		item := &list.Items[i]

		if !deleteAll && keep.Has(relatedCopyKey(item.GetNamespace(), item.GetName())) {
			continue
		}

		// already being deleted; come back once it is gone.
		if item.GetDeletionTimestamp() != nil {
			requeue = true
			continue
		}

		log.Debugw("Pruning related object copy…", "namespace", item.GetNamespace(), "name", item.GetName())
		policy := deletionPolicies[relatedCopyKey(item.GetNamespace(), item.GetName())]
		if err := dest.client.Delete(ctx, item,
			ctrlruntimeclient.PropagationPolicy(normalizeDeletionPropagationPolicy(policy))); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}

			return false, fmt.Errorf("failed to delete related copy: %w", err)
		}

		if recorder != nil {
			recorder.Eventf(primary, corev1.EventTypeNormal, "ObjectCleanup", "Deleted orphaned copy %s/%s of a related resource.", item.GetNamespace(), item.GetName())
		}

		requeue = true
	}

	return requeue, nil
}

// liveReadClient serves reads from an uncached reader (hitting the API server directly) while
// borrowing the RESTMapper, scheme and everything else from an underlying cached client. It lets a
// destructive prune re-run the origin resolution against the API server instead of trusting a
// possibly-stale informer cache, without duplicating the resolution logic.
type liveReadClient struct {
	ctrlruntimeclient.Client
	reader ctrlruntimeclient.Reader
}

func (c *liveReadClient) Get(ctx context.Context, key ctrlruntimeclient.ObjectKey, obj ctrlruntimeclient.Object, opts ...ctrlruntimeclient.GetOption) error {
	return c.reader.Get(ctx, key, obj, opts...)
}

func (c *liveReadClient) List(ctx context.Context, list ctrlruntimeclient.ObjectList, opts ...ctrlruntimeclient.ListOption) error {
	return c.reader.List(ctx, list, opts...)
}

// confirmOriginEmpty re-runs the origin resolution against a live (uncached) reader to double-check
// a cache-driven empty resolution before a MatchOrigin prune deletes all copies of a related
// resource. It returns checked=false when no live reader is configured for the origin side, in
// which case the caller must not treat the emptiness as authoritative. When a reader is configured,
// confirmedEmpty reports whether the live resolution also yields no objects.
func (s *ResourceSyncer) confirmOriginEmpty(ctx context.Context, origin, dest syncSide, relRes syncagentv1alpha1.RelatedResourceSpec) (confirmedEmpty bool, checked bool, err error) {
	var reader ctrlruntimeclient.Reader
	if relRes.Origin == syncagentv1alpha1.RelatedResourceOriginService {
		reader = s.localAPIReader
	} else {
		reader = s.remoteAPIReader
	}

	if reader == nil {
		return false, false, nil
	}

	liveOrigin := syncSide{
		clusterName: origin.clusterName,
		client:      &liveReadClient{Client: origin.client, reader: reader},
		object:      origin.object,
	}

	resolved, err := resolveRelatedResourceObjects(ctx, s.log, liveOrigin, dest, relRes)
	if err != nil {
		return false, true, err
	}

	return len(resolved) == 0, true, nil
}

// resolvedObject is the result of following the configuration of a related resources. It contains
// the original object (on the origin side of the related resource) and the target name to be used
// on the destination side of the sync.
type resolvedObject struct {
	original    *unstructured.Unstructured
	destination types.NamespacedName
}

func resolveRelatedResourceObjects(ctx context.Context, log *zap.SugaredLogger, relatedOrigin, relatedDest syncSide, relRes syncagentv1alpha1.RelatedResourceSpec) ([]resolvedObject, error) {
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

	objects, err := resolveRelatedResourceObjectsInNamespaces(ctx, log, relatedOrigin, relatedDest, relRes, relRes.Object.RelatedResourceObjectSpec, namespaceMap)
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

func resolveRelatedResourceObjectsInNamespaces(ctx context.Context, log *zap.SugaredLogger, relatedOrigin, relatedDest syncSide, relRes syncagentv1alpha1.RelatedResourceSpec, spec syncagentv1alpha1.RelatedResourceObjectSpec, namespaceMap map[string]string) ([]resolvedObject, error) {
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
				// This should rarely happen, only if an object was deleted in between the .List()
				// call above and the .Get() call here. It can also happen permanently if a
				// misconfigured object.template/reference/selector computes a name that never
				// matches a real object - in that case this branch is hit on every single
				// reconcile, forever, with the primary object's reconcile otherwise reporting
				// success. Log it so that case is diagnosable without reading the source.
				if apierrors.IsNotFound(err) {
					log.Debugw(
						"Origin object for related resource not found, skipping",
						"identifier", relRes.Identifier,
						"namespace", originNamespace,
						"name", originName,
					)

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
