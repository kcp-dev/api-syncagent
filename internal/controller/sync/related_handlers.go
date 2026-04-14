/*
Copyright 2026 The KCP Authors.

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

	"go.uber.org/zap"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// byOwnerEventHandler enqueues the primary object by inspecting the OwnerReferences
// of the changed related object and finding one matching the configured GVK.
type byOwnerEventHandler struct {
	clusterName multicluster.ClusterName
	ownerGVK    schema.GroupVersionKind
}

func (h *byOwnerEventHandler) Create(_ context.Context, evt event.TypedCreateEvent[*unstructured.Unstructured], q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
	h.enqueue(evt.Object, q)
}

func (h *byOwnerEventHandler) Update(_ context.Context, evt event.TypedUpdateEvent[*unstructured.Unstructured], q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
	h.enqueue(evt.ObjectNew, q)
}

func (h *byOwnerEventHandler) Delete(_ context.Context, evt event.TypedDeleteEvent[*unstructured.Unstructured], q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
	h.enqueue(evt.Object, q)
}

func (h *byOwnerEventHandler) Generic(_ context.Context, evt event.TypedGenericEvent[*unstructured.Unstructured], q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
	h.enqueue(evt.Object, q)
}

func (h *byOwnerEventHandler) enqueue(obj *unstructured.Unstructured, q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
	for _, ref := range obj.GetOwnerReferences() {
		refGV, err := schema.ParseGroupVersion(ref.APIVersion)
		if err != nil {
			continue
		}
		if refGV.Group == h.ownerGVK.Group && refGV.Version == h.ownerGVK.Version && ref.Kind == h.ownerGVK.Kind {
			q.Add(mcreconcile.Request{
				ClusterName: h.clusterName,
				Request: reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: obj.GetNamespace(),
						Name:      ref.Name,
					},
				},
			})
			return
		}
	}
}

// bySelectorEventHandler enqueues primary objects by listing primaries matching the configured
// label selector whenever a related object changes.
type bySelectorEventHandler struct {
	clusterName   multicluster.ClusterName
	client        ctrlruntimeclient.Client
	primaryDummy  *unstructured.Unstructured
	labelSelector *metav1.LabelSelector
	log           *zap.SugaredLogger
}

func (h *bySelectorEventHandler) Create(ctx context.Context, evt event.TypedCreateEvent[*unstructured.Unstructured], q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
	h.enqueue(ctx, evt.Object, q)
}

func (h *bySelectorEventHandler) Update(ctx context.Context, evt event.TypedUpdateEvent[*unstructured.Unstructured], q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
	h.enqueue(ctx, evt.ObjectNew, q)
}

func (h *bySelectorEventHandler) Delete(ctx context.Context, evt event.TypedDeleteEvent[*unstructured.Unstructured], q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
	h.enqueue(ctx, evt.Object, q)
}

func (h *bySelectorEventHandler) Generic(ctx context.Context, evt event.TypedGenericEvent[*unstructured.Unstructured], q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
	h.enqueue(ctx, evt.Object, q)
}

func (h *bySelectorEventHandler) enqueue(ctx context.Context, _ *unstructured.Unstructured, q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
	selector, err := metav1.LabelSelectorAsSelector(h.labelSelector)
	if err != nil {
		h.log.Warnw("Failed to convert bySelector selector", "error", err)
		return
	}

	// List primary objects matching the label selector.
	primaryList := &unstructured.UnstructuredList{}
	primaryList.SetAPIVersion(h.primaryDummy.GetAPIVersion())
	primaryList.SetKind(h.primaryDummy.GetKind() + "List")

	if err := h.client.List(ctx, primaryList, &ctrlruntimeclient.ListOptions{LabelSelector: selector}); err != nil {
		h.log.Warnw("Failed to list primary objects for bySelector watch", "selector", fmt.Sprintf("%v", selector), "error", err)
		return
	}

	for i := range primaryList.Items {
		primary := &primaryList.Items[i]
		q.Add(mcreconcile.Request{
			ClusterName: h.clusterName,
			Request: reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: primary.GetNamespace(),
					Name:      primary.GetName(),
				},
			},
		})
	}
}
