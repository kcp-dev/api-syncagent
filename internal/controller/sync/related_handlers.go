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

	"go.uber.org/zap"

	"github.com/kcp-dev/api-syncagent/internal/sync/templating"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// byOwnerEventHandler enqueues the primary object by inspecting the OwnerReferences
// of the changed related object and finding one with the configured Kind.
type byOwnerEventHandler struct {
	clusterName string
	ownerKind   string
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
		if ref.Kind == h.ownerKind {
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

// byLabelEventHandler enqueues primary objects by evaluating label templates against
// the changed related object and listing primaries matching the resulting label selector.
type byLabelEventHandler struct {
	clusterName    string
	client         ctrlruntimeclient.Client
	primaryDummy   *unstructured.Unstructured
	labelTemplates map[string]string
	log            *zap.SugaredLogger
}

func (h *byLabelEventHandler) Create(ctx context.Context, evt event.TypedCreateEvent[*unstructured.Unstructured], q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
	h.enqueue(ctx, evt.Object, q)
}

func (h *byLabelEventHandler) Update(ctx context.Context, evt event.TypedUpdateEvent[*unstructured.Unstructured], q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
	h.enqueue(ctx, evt.ObjectNew, q)
}

func (h *byLabelEventHandler) Delete(ctx context.Context, evt event.TypedDeleteEvent[*unstructured.Unstructured], q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
	h.enqueue(ctx, evt.Object, q)
}

func (h *byLabelEventHandler) Generic(ctx context.Context, evt event.TypedGenericEvent[*unstructured.Unstructured], q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
	h.enqueue(ctx, evt.Object, q)
}

func (h *byLabelEventHandler) enqueue(ctx context.Context, obj *unstructured.Unstructured, q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
	// Build the template context using the changed related object.
	data := map[string]any{
		"watchObject": map[string]any{
			"name":      obj.GetName(),
			"namespace": obj.GetNamespace(),
			"labels":    obj.GetLabels(),
		},
	}

	// Evaluate each label template to build the selector.
	matchingLabels := ctrlruntimeclient.MatchingLabels{}
	for key, tpl := range h.labelTemplates {
		value, err := templating.Render(tpl, data)
		if err != nil {
			h.log.Warnw("Failed to evaluate byLabel template", "key", key, "template", tpl, "error", err)
			return
		}
		matchingLabels[key] = value
	}

	// List primary objects matching the derived label selector.
	primaryList := &unstructured.UnstructuredList{}
	primaryList.SetAPIVersion(h.primaryDummy.GetAPIVersion())
	primaryList.SetKind(h.primaryDummy.GetKind() + "List")

	if err := h.client.List(ctx, primaryList, matchingLabels); err != nil {
		h.log.Warnw("Failed to list primary objects for byLabel watch", "selector", matchingLabels, "error", err)
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
