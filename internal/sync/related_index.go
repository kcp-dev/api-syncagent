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
	gosync "sync"

	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

type relatedObjectKey struct {
	cluster   string
	group     string
	resource  string
	namespace string
	name      string
}

// RelatedObjectIndex maps kcp-side related objects back to their owning primary objects.
// It is populated during reconciliation and used by watch handlers to trigger
// reconciliation when a related resource changes in kcp.
type RelatedObjectIndex struct {
	mu    gosync.RWMutex
	index map[relatedObjectKey]mcreconcile.Request
}

// NewRelatedObjectIndex creates a new empty RelatedObjectIndex.
func NewRelatedObjectIndex() *RelatedObjectIndex {
	return &RelatedObjectIndex{
		index: make(map[relatedObjectKey]mcreconcile.Request),
	}
}

// Set stores the mapping from a related object to its owning primary object.
func (i *RelatedObjectIndex) Set(cluster, group, resource, namespace, name string, primary mcreconcile.Request) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.index[relatedObjectKey{cluster, group, resource, namespace, name}] = primary
}

// Get looks up the primary object that owns the given related object.
func (i *RelatedObjectIndex) Get(cluster, group, resource, namespace, name string) (mcreconcile.Request, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	req, ok := i.index[relatedObjectKey{cluster, group, resource, namespace, name}]
	return req, ok
}
