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

	"github.com/kcp-dev/logicalcluster/v3"
	"go.uber.org/zap"

	"github.com/kcp-dev/api-syncagent/internal/mutation"
	"github.com/kcp-dev/api-syncagent/internal/projection"
	"github.com/kcp-dev/api-syncagent/internal/sync/templating"
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

type newObjectStateStoreFunc func(primaryObject, stateCluster syncSide) ObjectStateStore

type ResourceSyncer struct {
	log *zap.SugaredLogger

	localClient  ctrlruntimeclient.Client
	remoteClient ctrlruntimeclient.Client
	pubRes       *syncagentv1alpha1.PublishedResource
	localCRD     *apiextensionsv1.CustomResourceDefinition
	subresources []string

	destDummy *unstructured.Unstructured

	// cached mutators (for those transformers that are expensive to compile, like CEL)
	primaryMutator  mutation.Mutator
	relatedMutators map[string]mutation.Mutator

	agentName string

	// newObjectStateStore is used for testing purposes
	newObjectStateStore newObjectStateStoreFunc
}

type MutatorCreatorFunc func(*syncagentv1alpha1.ResourceMutationSpec) (mutation.Mutator, error)

func NewResourceSyncer(
	log *zap.SugaredLogger,
	localClient ctrlruntimeclient.Client,
	remoteClient ctrlruntimeclient.Client,
	pubRes *syncagentv1alpha1.PublishedResource,
	localCRD *apiextensionsv1.CustomResourceDefinition,
	mutatorCreator MutatorCreatorFunc,
	stateNamespace string,
	agentName string,
) (*ResourceSyncer, error) {
	// create a dummy that represents the type used on the local service cluster
	localGVK, err := projection.PublishedResourceSourceGVK(localCRD, pubRes)
	if err != nil {
		return nil, err
	}

	// create a dummy that represents the type used on the local service cluster
	localDummy := &unstructured.Unstructured{}
	localDummy.SetGroupVersionKind(localGVK)

	// create a dummy unstructured object with the projected GVK inside the workspace
	remoteGVK, err := projection.PublishedResourceProjectedGVK(localCRD, pubRes)
	if err != nil {
		return nil, err
	}

	// determine whether the CRD has a status subresource in the relevant version
	subresources := []string{}
	for _, version := range localCRD.Spec.Versions {
		if version.Name == localGVK.Version {
			if sr := version.Subresources; sr != nil {
				if sr.Scale != nil {
					subresources = append(subresources, "scale")
				}
				if sr.Status != nil {
					subresources = append(subresources, "status")
				}
			}
		}
	}

	primaryMutator, err := mutatorCreator(pubRes.Spec.Mutation)
	if err != nil {
		return nil, fmt.Errorf("failed to create primary object mutator: %w", err)
	}

	relatedMutators := map[string]mutation.Mutator{}
	for _, rr := range pubRes.Spec.Related {
		mutator, err := mutatorCreator(rr.Mutation)
		if err != nil {
			return nil, fmt.Errorf("failed to create related object %q mutator: %w", rr.Identifier, err)
		}

		relatedMutators[rr.Identifier] = mutator
	}

	return &ResourceSyncer{
		log:                 log.With("local-gvk", localGVK, "remote-gvk", remoteGVK),
		localClient:         localClient,
		remoteClient:        remoteClient,
		pubRes:              pubRes,
		localCRD:            localCRD,
		subresources:        subresources,
		destDummy:           localDummy,
		primaryMutator:      primaryMutator,
		relatedMutators:     relatedMutators,
		agentName:           agentName,
		newObjectStateStore: newKubernetesStateStoreCreator(stateNamespace),
	}, nil
}

// Process is the primary entrypoint for object synchronization. This function will create/update
// the local primary object (i.e. the copy of the remote object), sync any local status back to the
// remote object and then also synchronize all related resources. It also handles object deletion
// and will clean up the local objects when a remote object is gone.
// Each of these steps can potentially end the current processing and return (true, nil). In this
// case, the caller should re-fetch the remote object and call Process() again (most likely in the
// next reconciliation). Only when (false, nil) is returned is the entire process finished.
// The context must contain a cluster name and event recorder, optionally a workspace path.
func (s *ResourceSyncer) Process(ctx context.Context, remoteObj *unstructured.Unstructured) (requeue bool, err error) {
	clusterName := clusterFromContext(ctx)
	workspacePath := workspacePathFromContext(ctx)
	objectKey := newObjectKey(remoteObj, clusterName, workspacePath)

	log := s.log.With("source-object", objectKey)

	// find the local equivalent object in the local service cluster
	localObj, err := s.findLocalObject(ctx, objectKey)
	if err != nil {
		return false, fmt.Errorf("failed to find local equivalent: %w", err)
	}

	// Do not add local-object to the log here,
	// instead each further function will fine tune the log context.

	// Prepare object sync sides.

	sourceSide := syncSide{
		clusterName:   clusterName,
		workspacePath: workspacePath,
		client:        s.remoteClient,
		object:        remoteObj,
	}

	destSide := syncSide{
		client: s.localClient,
		object: localObj,
	}

	// create a state store, which we will use to remember the last known (i.e. the current)
	// object state; this allows the code to create meaningful patches and not overwrite
	// fields that were defaulted by the kube-apiserver or a mutating webhook
	stateStore := s.newObjectStateStore(sourceSide, destSide)

	syncer := objectSyncer{
		// The primary object should be labelled with the agent name.
		agentName:    s.agentName,
		subresources: s.subresources,
		// use the projection and renaming rules configured in the PublishedResource
		destCreator: s.newLocalObjectCreator(clusterName, workspacePath),
		// for the main resource, status subresource handling is enabled (this
		// means _allowing_ status back-syncing, it still depends on whether the
		// status subresource even exists whether an update happens)
		syncStatusBack: true,
		// perform cleanup on the service cluster side when the source object
		// in kcp is deleted
		blockSourceDeletion: true,
		// use the configured mutations from the PublishedResource
		mutator: s.primaryMutator,
		// make sure the syncer can remember the current state of any object
		stateStore: stateStore,
		// For the main resource, we need to store metadata on the destination copy
		// (i.e. on the service cluster), so that the original and copy are linked
		// together and can be found.
		metadataOnDestination: true,
		eventObjSide:          syncSideSource,
	}

	requeue, err = syncer.Sync(ctx, log, sourceSide, destSide)
	if err != nil {
		return false, err
	}

	// the patch above would trigger a new reconciliation anyway
	if requeue {
		return true, nil
	}

	// Now the main object is fully synced and up-to-date on both sides;
	// we can now begin to look at related resources and synchronize those
	// as well.
	// NB: This relies on syncObject always returning requeue=true when
	// it modifies the state of the world, otherwise the objects in
	// source/dest.object might be ouf date.

	return s.processRelatedResources(ctx, log, stateStore, sourceSide, destSide)
}

func (s *ResourceSyncer) findLocalObject(ctx context.Context, objectKey objectKey) (*unstructured.Unstructured, error) {
	localSelector := labels.SelectorFromSet(objectKey.Labels())

	localObjects := &unstructured.UnstructuredList{}
	localObjects.SetAPIVersion(s.destDummy.GetAPIVersion())
	localObjects.SetKind(s.destDummy.GetKind() + "List")

	if err := s.localClient.List(ctx, localObjects, &ctrlruntimeclient.ListOptions{
		LabelSelector: localSelector,
		Limit:         2, // 2 in order to detect broken configurations
	}); err != nil {
		return nil, fmt.Errorf("failed to find local equivalent: %w", err)
	}

	switch len(localObjects.Items) {
	case 0:
		return nil, nil
	case 1:
		return &localObjects.Items[0], nil
	default:
		return nil, fmt.Errorf("expected 1 object matching %s, but found %d", localSelector, len(localObjects.Items))
	}
}

func (s *ResourceSyncer) newLocalObjectCreator(clusterName logicalcluster.Name, workspacePath logicalcluster.Path) objectCreatorFunc {
	return func(remoteObj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
		// map from the remote API into the actual, local API group
		destObj := remoteObj.DeepCopy()
		destObj.SetGroupVersionKind(s.destDummy.GroupVersionKind())

		// change scope if desired
		destScope := syncagentv1alpha1.ResourceScope(s.localCRD.Spec.Scope)

		// map namespace/name
		mappedName, err := templating.GenerateLocalObjectName(s.pubRes, remoteObj, clusterName, workspacePath)
		if err != nil {
			return nil, fmt.Errorf("failed to generate local object name: %w", err)
		}

		switch destScope {
		case syncagentv1alpha1.ClusterScoped:
			destObj.SetNamespace("")
			destObj.SetName(mappedName.Name)

		case syncagentv1alpha1.NamespaceScoped:
			destObj.SetNamespace(mappedName.Namespace)
			destObj.SetName(mappedName.Name)
		}

		return destObj, nil
	}
}
