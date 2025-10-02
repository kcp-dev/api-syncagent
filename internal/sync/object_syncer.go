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
	"fmt"
	"slices"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/kcp-dev/logicalcluster/v3"
	"go.uber.org/zap"
	"k8c.io/reconciler/pkg/equality"

	"github.com/kcp-dev/api-syncagent/internal/mutation"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/api/meta"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

type objectCreatorFunc func(source *unstructured.Unstructured) (*unstructured.Unstructured, error)

type objectSyncer struct {
	// When set, the syncer will create a label on the destination object that contains
	// this value; used to allow multiple agents syncing *the same* API from one
	// service cluster onto multiple different kcp's.
	agentName string
	// creates a new destination object; does not need to perform cleanup like
	// removing unwanted metadata, that's done by the syncer automatically
	destCreator objectCreatorFunc
	// list of subresources in the resource type
	subresources []string
	// whether to enable status subresource back-syncing
	syncStatusBack bool
	// whether or not to add/expect a finalizer on the source
	blockSourceDeletion bool
	// whether or not to place sync-related metadata on the destination object
	metadataOnDestination bool
	// optional mutations for both directions of the sync
	mutator mutation.Mutator
	// stateStore is capable of remembering the state of a Kubernetes object
	stateStore ObjectStateStore
	// eventObjSide is configuring whether the source or destination object will
	// receive events. Since these objects might be created during the sync,
	// they cannot be specified here directly.
	eventObjSide syncSideType
}

type syncSideType int

const (
	syncSideSource syncSideType = iota
	syncSideDestination
)

type syncSide struct {
	clusterName   logicalcluster.Name
	workspacePath logicalcluster.Path
	client        ctrlruntimeclient.Client
	object        *unstructured.Unstructured
}

func (s *objectSyncer) recordEvent(ctx context.Context, source, dest syncSide, eventtype, reason, msg string, args ...any) {
	recorder := recorderFromContext(ctx)

	var obj runtime.Object
	if s.eventObjSide == syncSideDestination {
		obj = dest.object
	} else {
		obj = source.object
	}

	recorder.Eventf(obj, eventtype, reason, msg, args...)
}

func (s *objectSyncer) Sync(ctx context.Context, log *zap.SugaredLogger, source, dest syncSide) (requeue bool, err error) {
	// handle deletion: if source object is in deletion, delete the destination object (the clone)
	if source.object.GetDeletionTimestamp() != nil {
		return s.handleDeletion(ctx, log, source, dest)
	}

	// add finalizer to source object so that we never orphan the destination object
	if s.blockSourceDeletion {
		updated, err := ensureFinalizer(ctx, log, source.client, source.object, deletionFinalizer)
		if err != nil {
			return false, fmt.Errorf("failed to add cleanup finalizer to source object: %w", err)
		}

		// the patch above would trigger a new reconciliation anyway
		if updated {
			s.recordEvent(ctx, source, dest, corev1.EventTypeNormal, "ObjectAccepted", "Object has been seen by the service provider.")
			return true, nil
		}
	}

	// Apply custom mutation rules; transform the source object into its mutated form, which
	// then serves as the basis for the object content synchronization. Then transform the
	// destination object's status.
	source, dest, err = s.applyMutations(source, dest)
	if err != nil {
		return false, fmt.Errorf("failed to apply mutations: %w", err)
	}

	// if no destination object exists yet, attempt to create it;
	// note that the object _might_ exist, but we were not able to find it because of broken labels
	if dest.object == nil {
		err := s.ensureDestinationObject(ctx, log, source, dest)
		if err != nil {
			return false, fmt.Errorf("failed to create destination object: %w", err)
		}

		// The function above either created a new destination object or patched-in the missing labels,
		// in both cases do we want to requeue.
		return true, nil
	}

	// destination object exists, time to synchronize state

	// do not try to update a destination object that is in deletion
	// (this should only happen if a service admin manually deletes something on the service cluster)
	if dest.object.GetDeletionTimestamp() != nil {
		log.Debugw("Destination object is in deletion, skipping any further synchronization", "dest-object", newObjectKey(dest.object, dest.clusterName, logicalcluster.None))
		return false, nil
	}

	requeue, err = s.syncObjectContents(ctx, log, source, dest)
	if err != nil {
		return false, fmt.Errorf("failed to synchronize object state: %w", err)
	}

	return requeue, nil
}

func (s *objectSyncer) applyMutations(source, dest syncSide) (syncSide, syncSide, error) {
	if s.mutator == nil {
		return source, dest, nil
	}

	// Mutation rules can access the mutated name of the destination object; in case there
	// is no such object yet, we have to temporarily create one here in memory to have
	// the mutated names available.
	destObject := dest.object
	if destObject == nil {
		var err error
		destObject, err = s.destCreator(source.object)
		if err != nil {
			return source, dest, fmt.Errorf("failed to create destination object: %w", err)
		}
	}

	sourceObj, err := s.mutator.MutateSpec(source.object.DeepCopy(), destObject)
	if err != nil {
		return source, dest, fmt.Errorf("failed to apply spec mutation rules: %w", err)
	}

	// from now on, we only work on the mutated source
	source.object = sourceObj

	// if the destination object already exists, we can mutate its status as well
	// (this is mostly only relevant for the primary object sync, which goes
	// kcp->service cluster; related resources do not backsync the status subresource).
	if dest.object != nil {
		destObject, err = s.mutator.MutateStatus(dest.object.DeepCopy(), sourceObj)
		if err != nil {
			return source, dest, fmt.Errorf("failed to apply status mutation rules: %w", err)
		}

		dest.object = destObject
	}

	return source, dest, nil
}

func (s *objectSyncer) syncObjectContents(ctx context.Context, log *zap.SugaredLogger, source, dest syncSide) (requeue bool, err error) {
	// Sync the spec (or more generally, the desired state) from source to dest.
	requeue, err = s.syncObjectSpec(ctx, log, source, dest)
	if requeue || err != nil {
		return requeue, err
	}

	// Sync the status back in the opposite direction, from dest to source.
	return s.syncObjectStatus(ctx, log, source, dest)
}

func (s *objectSyncer) syncObjectSpec(ctx context.Context, log *zap.SugaredLogger, source, dest syncSide) (requeue bool, err error) {
	// figure out the last known state
	lastKnownSourceState, err := s.stateStore.Get(ctx, source)
	if err != nil {
		return false, fmt.Errorf("failed to determine last known state: %w", err)
	}

	sourceObjCopy := source.object.DeepCopy()
	if err = stripMetadata(sourceObjCopy); err != nil {
		return false, fmt.Errorf("failed to strip metadata from source object: %w", err)
	}

	log = log.With("dest-object", newObjectKey(dest.object, dest.clusterName, logicalcluster.None))

	// calculate the patch to go from the last known state to the current source object's state
	if lastKnownSourceState != nil {
		// ignore difference in GVK
		lastKnownSourceState.SetAPIVersion(sourceObjCopy.GetAPIVersion())
		lastKnownSourceState.SetKind(sourceObjCopy.GetKind())

		// We want to now restore/fix broken labels or annotations on the destination object. Not all
		// of the sync-related metadata is relevant to _finding_ the destination object, so there is
		// a chance that a user has fiddled with the metadata and would have broken some other part
		// of the syncing.
		// The lastKnownState is based on the source object, so just from looking at it we could not
		// determine whether a label/annotation is missing/broken. However we do need to know this,
		// because we later have to distinguish between empty and non-empty patches, so that we know
		// when to requeue or stop syncing. So we cannot just blindly call ensureLabels() on the
		// sourceObjCopy, as that could create meaningless patches.
		// To achieve this, we individually check the labels/annotations on the destination object,
		// which we thankfully already fetched earlier.
		if s.metadataOnDestination {
			sourceKey := newObjectKey(source.object, source.clusterName, source.workspacePath)
			threeWayDiffMetadata(sourceObjCopy, dest.object, sourceKey.Labels(), sourceKey.Annotations())
		}

		// now we can diff the two versions and create a patch
		rawPatch, err := s.createMergePatch(lastKnownSourceState, sourceObjCopy)
		if err != nil {
			return false, fmt.Errorf("failed to calculate patch: %w", err)
		}

		// only patch if the patch is not empty
		if string(rawPatch) != "{}" {
			log.Debugw("Patching destination object…", "patch", string(rawPatch))

			if err := dest.client.Patch(ctx, dest.object, ctrlruntimeclient.RawPatch(types.MergePatchType, rawPatch)); err != nil {
				return false, fmt.Errorf("failed to patch destination object: %w", err)
			}

			requeue = true
		}
	} else {
		// there is no last state available, we have to fall back to doing a stupid full update
		sourceContent := source.object.UnstructuredContent()
		destContent := dest.object.UnstructuredContent()

		// update things like spec and other top level elements
		for key, data := range sourceContent {
			if !s.isIrrelevantTopLevelField(key) {
				destContent[key] = data
			}
		}

		// update selected metadata fields
		ensureLabels(dest.object, filterUnsyncableLabels(sourceObjCopy.GetLabels()))
		ensureAnnotations(dest.object, filterUnsyncableAnnotations(sourceObjCopy.GetAnnotations()))

		// TODO: Check if anything has changed and skip the .Update() call if source and dest
		// are identical w.r.t. the fields we have copied (spec, annotations, labels, ..).
		log.Warn("Updating destination object because last-known-state is missing/invalid…")

		if err := dest.client.Update(ctx, dest.object); err != nil {
			return false, fmt.Errorf("failed to update destination object: %w", err)
		}

		requeue = true
	}

	if requeue {
		s.recordEvent(ctx, source, dest, corev1.EventTypeNormal, "ObjectSynced", "The current desired state of the object has been synchronized.")

		// remember this object state for the next reconciliation (this will strip any syncer-related
		// metadata the 3-way diff may have added above)
		if err := s.stateStore.Put(ctx, sourceObjCopy, source.clusterName, s.subresources); err != nil {
			return true, fmt.Errorf("failed to update sync state: %w", err)
		}
	}

	return requeue, nil
}

func (s *objectSyncer) syncObjectStatus(ctx context.Context, log *zap.SugaredLogger, source, dest syncSide) (requeue bool, err error) {
	if !s.syncStatusBack {
		return false, nil
	}

	// Source and dest in this function are from the viewpoint of the entire object's sync, meaning
	// this function _technically_ syncs from dest to source.

	sourceContent := source.object.UnstructuredContent()
	destContent := dest.object.UnstructuredContent()

	if !equality.Semantic.DeepEqual(sourceContent["status"], destContent["status"]) {
		sourceContent["status"] = destContent["status"]

		log.Debug("Updating source object status…")
		if err := source.client.Status().Update(ctx, source.object); err != nil {
			return false, fmt.Errorf("failed to update source object status: %w", err)
		}

		s.recordEvent(ctx, source, dest, corev1.EventTypeNormal, "ObjectStatusSynced", "The current object status has been updated.")
	}

	// always return false; there is no need to requeue the source object when we changed its status
	return false, nil
}

func (s *objectSyncer) ensureDestinationObject(ctx context.Context, log *zap.SugaredLogger, source, dest syncSide) error {
	// create a copy of the source with GVK projected and renaming rules applied
	destObj, err := s.destCreator(source.object)
	if err != nil {
		return fmt.Errorf("failed to create destination object: %w", err)
	}

    log.Debugw("Starting ensureDestinationObject",
        "sourceNS", source.object.GetNamespace(),
        "destNS", destObj.GetNamespace(),
        "destGVK", destObj.GroupVersionKind(),
        "destName", destObj.GetName(),
    )
	// make sure the target namespace on the destination cluster exists
	if err := s.ensureNamespace(ctx, log, dest.client, source, destObj.GetNamespace()); err != nil {
	    log.Errorw("ensureNamespace failed",
            "namespace", destObj.GetNamespace(),
            "error", err,
        )
		return fmt.Errorf("failed to ensure destination namespace: %w", err)
	}

    log.Debugw("Namespace ensured", "namespace", destObj.GetNamespace())

	// remove source metadata (like UID and generation, but also labels and annotations belonging to
	// the sync-agent) to allow destination object creation to succeed
	if err := stripMetadata(destObj); err != nil {
		return fmt.Errorf("failed to strip metadata from destination object: %w", err)
	}

	// remember the connection between the source and destination object
	sourceObjKey := newObjectKey(source.object, source.clusterName, source.workspacePath)
	if s.metadataOnDestination {
		ensureLabels(destObj, sourceObjKey.Labels())
		ensureAnnotations(destObj, sourceObjKey.Annotations())

		// remember what agent synced this object
		s.labelWithAgent(destObj)
	}

	// finally, we can create the destination object
	objectLog := log.With("dest-object", newObjectKey(destObj, dest.clusterName, logicalcluster.None))
	objectLog.Debugw("Creating destination object…")

	if err := dest.client.Create(ctx, destObj); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create destination object: %w", err)
		}

		if err := s.adoptExistingDestinationObject(ctx, objectLog, dest, destObj, sourceObjKey); err != nil {
			return fmt.Errorf("failed to adopt destination object: %w", err)
		}
	}

	dest.object = destObj
	s.recordEvent(ctx, source, dest, corev1.EventTypeNormal, "ObjectPlaced", "Object has been placed.")

	// remember the state of the object that we just created
	if err := s.stateStore.Put(ctx, source.object, source.clusterName, s.subresources); err != nil {
		return fmt.Errorf("failed to update sync state: %w", err)
	}

	return nil
}

func (s *objectSyncer) adoptExistingDestinationObject(ctx context.Context, log *zap.SugaredLogger, dest syncSide, existingDestObj *unstructured.Unstructured, sourceKey objectKey) error {
	// Cannot add labels to an object in deletion, also there would be no point
	// in adopting a soon-to-disappear object; instead we silently wait, requeue
	// and when the object is gone, recreate a fresh one with proper labels.
	if existingDestObj.GetDeletionTimestamp() != nil {
		return nil
	}

	log.Warn("Adopting existing but mislabelled destination object…")

	// fetch the current state
	if err := dest.client.Get(ctx, ctrlruntimeclient.ObjectKeyFromObject(existingDestObj), existingDestObj); err != nil {
		return fmt.Errorf("failed to get current destination object: %w", err)
	}

	// Set (or replace!) the identification labels on the existing destination object;
	// if we did not guarantee that destination objects never collide, this could in theory "take away"
	// the destination object from another source object, which would then lead to the two source objects
	// "fighting" about the one destination object.
	ensureLabels(existingDestObj, sourceKey.Labels())
	ensureAnnotations(existingDestObj, sourceKey.Annotations())

	s.labelWithAgent(existingDestObj)

	if err := dest.client.Update(ctx, existingDestObj); err != nil {
		return fmt.Errorf("failed to upsert current destination object labels: %w", err)
	}

	return nil
}

func (s *objectSyncer) ensureNamespace(ctx context.Context, log *zap.SugaredLogger, client ctrlruntimeclient.Client, source syncSide, namespace string) error {
    // cluster-scoped objects do not need namespaces
    if namespace == "" {
        return nil
    }

    // Check if the destination namespace already exists
    destNs := &corev1.Namespace{}
    if err := client.Get(ctx, types.NamespacedName{Name: namespace}, destNs); ctrlruntimeclient.IgnoreNotFound(err) != nil {
        log.Errorw("Failed to fetch destination namespace", "namespace", namespace, "error", err)
        return fmt.Errorf("failed to check destination namespace %q: %w", namespace, err)
    }

    // Only build a new namespace if one doesn’t exist
    if destNs.Name == "" {
        // Check if permissions claims on source namespace allow us to access metadata
        sourceNs := &corev1.Namespace{}
        labels, annotations := map[string]string{}, map[string]string{}

        if err := source.client.Get(ctx, types.NamespacedName{Name: source.object.GetNamespace()}, sourceNs); err != nil {
            switch {
            case apierrors.IsForbidden(err):
                log.Warnw("Skipping namespace metadata: missing permission to read source namespace", "namespace", namespace)
            case apierrors.IsNotFound(err):
                log.Warnw("Source namespace not found, creating destination without metadata", "namespace", namespace)
            case meta.IsNoMatchError(err):
                log.Warnw("Skipping namespace metadata: APIExport does not expose Namespace", "namespace", namespace)
            default:
                log.Errorw("failed to fetch source namespace", "sourceNS", source.object.GetNamespace(), "error", err)
                return fmt.Errorf("failed to fetch source namespace %q: %w", source.object.GetNamespace(), err)
            }
        } else {
            labels = sourceNs.Labels
            annotations = sourceNs.Annotations
            log.Debugw("Fetched source namespace metadata", "labels", labels, "annotations", annotations)
        }

        ns := &corev1.Namespace{
            ObjectMeta: metav1.ObjectMeta{
                Name:        namespace,
                Labels:      labels,
                Annotations: annotations,
            },
        }

        log.Debugw("Creating namespace", "namespace", namespace)
        if err := client.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
            log.Errorw("Failed to create destination namespace", "namespace", namespace, "error", err)
            return fmt.Errorf("failed to create namespace %q: %w", namespace, err)
        }
        log.Debugw("Namespace created or already existed", "namespace", namespace)
    }

    return nil
}

func (s *objectSyncer) handleDeletion(ctx context.Context, log *zap.SugaredLogger, source, dest syncSide) (requeue bool, err error) {
	// if no finalizer was added, we can safely ignore this event
	if !s.blockSourceDeletion {
		return false, nil
	}

	// if the destination object still exists, delete it and wait for it to be cleaned up
	if dest.object != nil {
		if dest.object.GetDeletionTimestamp() == nil {
			log.Debugw("Deleting destination object…", "dest-object", newObjectKey(dest.object, dest.clusterName, logicalcluster.None))
			s.recordEvent(ctx, source, dest, corev1.EventTypeNormal, "ObjectCleanup", "Object deletion has been started and will progress in the background.")
			if err := dest.client.Delete(ctx, dest.object); err != nil {
				return false, fmt.Errorf("failed to delete destination object: %w", err)
			}
		}

		return true, nil
	}

	// the destination object is gone, we can release the source one
	updated, err := removeFinalizer(ctx, log, source.client, source.object, deletionFinalizer)
	if err != nil {
		return false, fmt.Errorf("failed to remove cleanup finalizer from source object: %w", err)
	}

	// if we just removed the finalizer, we can requeue the source object
	if updated {
		s.recordEvent(ctx, source, dest, corev1.EventTypeNormal, "ObjectDeleted", "Object deletion has been completed, finalizer has been removed.")
		return true, nil
	}

	// For now we do not delete related resources; since after this step the destination object is
	// gone already, the remaining syncer logic would fail if it attempts to sync relate objects.
	// For the MVP it's fine to just leave related resources around, but in the future this behaviour
	// might be configurable per PublishedResource, in which case this `return true` here would need
	// to go away and the cleanup in general would need to be rethought a bit (maybe owner refs would
	// be a good idea?).
	return true, nil
}

func (s *objectSyncer) removeSubresources(obj *unstructured.Unstructured) *unstructured.Unstructured {
	data := obj.UnstructuredContent()
	for _, key := range s.subresources {
		delete(data, key)
	}

	return obj
}

func (s *objectSyncer) createMergePatch(base, revision *unstructured.Unstructured) ([]byte, error) {
	base = s.removeSubresources(base.DeepCopy())
	revision = s.removeSubresources(revision.DeepCopy())

	baseJSON, err := base.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal base: %w", err)
	}

	revisionJSON, err := revision.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal revision: %w", err)
	}

	return jsonpatch.CreateMergePatch(baseJSON, revisionJSON)
}

func (s *objectSyncer) isIrrelevantTopLevelField(fieldName string) bool {
	return fieldName == "kind" || fieldName == "apiVersion" || fieldName == "metadata" || slices.Contains(s.subresources, fieldName)
}

func (s *objectSyncer) labelWithAgent(obj *unstructured.Unstructured) {
	if s.agentName != "" {
		ensureLabels(obj, map[string]string{agentNameLabel: s.agentName})
	}
}
