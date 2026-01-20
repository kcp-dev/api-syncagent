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
	"strings"

	"github.com/kcp-dev/api-syncagent/internal/crypto"

	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

type ObjectStateStore interface {
	Get(ctx context.Context, source syncSide) (*unstructured.Unstructured, error)
	Put(ctx context.Context, obj *unstructured.Unstructured, clusterName logicalcluster.Name, subresources []string) error
}

// objectStateStore is capable of creating/updating a target Kubernetes object
// based on a source object. It keeps track of the source's state so that fields
// that are changed after/outside of the Sync Agent are not undone by accident.
// This is the same logic as kubectl has using its last-known annotation.
type objectStateStore struct {
	backend backend
}

func newObjectStateStore(backend backend) ObjectStateStore {
	return &objectStateStore{
		backend: backend,
	}
}

func newKubernetesStateStoreCreator(namespace string) newObjectStateStoreFunc {
	return func(primaryObject, stateCluster syncSide) ObjectStateStore {
		return newObjectStateStore(newKubernetesBackend(namespace, primaryObject, stateCluster))
	}
}

func (op *objectStateStore) Get(ctx context.Context, source syncSide) (*unstructured.Unstructured, error) {
	data, err := op.backend.Get(ctx, source.object, source.clusterName)
	if err != nil {
		return nil, err
	}

	lastKnown := &unstructured.Unstructured{}
	if err := lastKnown.UnmarshalJSON(data); err != nil {
		// if no last-known-state annotation exists or it's defective, the destination object is
		// technically broken and we have to fall back to a full update
		return nil, nil
	}

	return lastKnown, nil
}

func (op *objectStateStore) Put(ctx context.Context, obj *unstructured.Unstructured, clusterName logicalcluster.Name, subresources []string) error {
	encoded, err := op.snapshotObject(obj, subresources)
	if err != nil {
		return err
	}

	return op.backend.Put(ctx, obj, clusterName, []byte(encoded))
}

func (op *objectStateStore) snapshotObject(obj *unstructured.Unstructured, subresources []string) (string, error) {
	obj = obj.DeepCopy()
	if err := stripMetadata(obj); err != nil {
		return "", err
	}

	// besides metadata, we also do not care about the object's subresources
	data := obj.UnstructuredContent()
	for _, key := range subresources {
		delete(data, key)
	}

	marshalled, err := obj.MarshalJSON()
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(marshalled)), nil
}

type backend interface {
	Get(ctx context.Context, obj *unstructured.Unstructured, clusterName logicalcluster.Name) ([]byte, error)
	Put(ctx context.Context, obj *unstructured.Unstructured, clusterName logicalcluster.Name, data []byte) error
}

type kubernetesBackend struct {
	secretName   types.NamespacedName
	labels       labels.Set
	stateCluster syncSide
}

func hashObject(obj *unstructured.Unstructured) string {
	return crypto.ShortHash(map[string]any{
		"apiVersion": obj.GetAPIVersion(),
		"kind":       obj.GetKind(),
		"namespace":  obj.GetNamespace(),
		"name":       obj.GetName(),
	})
}

func newKubernetesBackend(namespace string, primaryObject, stateCluster syncSide) *kubernetesBackend {
	shortKeyHash := hashObject(primaryObject.object)

	secretLabels := newObjectKey(primaryObject.object, primaryObject.clusterName, primaryObject.workspacePath).Labels()
	secretLabels[objectStateLabelName] = objectStateLabelValue

	return &kubernetesBackend{
		secretName: types.NamespacedName{
			// trim hash down; 20 was chosen at random
			Name:      fmt.Sprintf("obj-state-%s-%s", primaryObject.clusterName, shortKeyHash),
			Namespace: namespace,
		},
		labels:       secretLabels,
		stateCluster: stateCluster,
	}
}

func (b *kubernetesBackend) Get(ctx context.Context, obj *unstructured.Unstructured, clusterName logicalcluster.Name) ([]byte, error) {
	secret := corev1.Secret{}
	if err := b.stateCluster.client.Get(ctx, b.secretName, &secret); ctrlruntimeclient.IgnoreNotFound(err) != nil {
		return nil, err
	}

	sourceKey := newObjectKey(obj, clusterName, logicalcluster.None).Key()
	data, ok := secret.Data[sourceKey]
	if !ok {
		return nil, nil
	}

	return data, nil
}

func (b *kubernetesBackend) Put(ctx context.Context, obj *unstructured.Unstructured, clusterName logicalcluster.Name, data []byte) error {
	secret := corev1.Secret{}
	if err := b.stateCluster.client.Get(ctx, b.secretName, &secret); ctrlruntimeclient.IgnoreNotFound(err) != nil {
		return err
	}

	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}

	sourceKey := newObjectKey(obj, clusterName, logicalcluster.None).Key()
	secret.Data[sourceKey] = data
	secret.Labels = b.labels

	var err error

	if secret.Namespace == "" {
		secret.Name = b.secretName.Name
		secret.Namespace = b.secretName.Namespace

		err = b.stateCluster.client.Create(ctx, &secret)
	} else {
		err = b.stateCluster.client.Update(ctx, &secret)
	}

	return err
}
