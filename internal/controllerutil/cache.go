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

package controllerutil

import (
	"context"
	"time"

	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// HandlerRemovingCache is meant for controller-runtime's Kind sources, which never remove
// their event handlers from the shared informers. It removes each handler once the context
// that its informer was retrieved with ends, which for Kind sources is the context they
// were started with. Without this, every stopped sync controller would leave its handlers
// (and their goroutines) behind on the local manager's informers.
type HandlerRemovingCache struct {
	cache.Cache
}

func (c HandlerRemovingCache) GetInformer(ctx context.Context, obj ctrlruntimeclient.Object, opts ...cache.InformerGetOption) (cache.Informer, error) {
	informer, err := c.Cache.GetInformer(ctx, obj, opts...)
	if err != nil {
		return nil, err
	}

	return handlerRemovingInformer{Informer: informer, ctx: ctx}, nil
}

type handlerRemovingInformer struct {
	cache.Informer
	ctx context.Context
}

func (i handlerRemovingInformer) AddEventHandler(handler toolscache.ResourceEventHandler) (toolscache.ResourceEventHandlerRegistration, error) {
	return i.removeOnDone(i.Informer.AddEventHandler(handler))
}

func (i handlerRemovingInformer) AddEventHandlerWithResyncPeriod(handler toolscache.ResourceEventHandler, resyncPeriod time.Duration) (toolscache.ResourceEventHandlerRegistration, error) {
	return i.removeOnDone(i.Informer.AddEventHandlerWithResyncPeriod(handler, resyncPeriod))
}

func (i handlerRemovingInformer) AddEventHandlerWithOptions(handler toolscache.ResourceEventHandler, options toolscache.HandlerOptions) (toolscache.ResourceEventHandlerRegistration, error) {
	return i.removeOnDone(i.Informer.AddEventHandlerWithOptions(handler, options))
}

func (i handlerRemovingInformer) removeOnDone(registration toolscache.ResourceEventHandlerRegistration, err error) (toolscache.ResourceEventHandlerRegistration, error) {
	if err != nil {
		return nil, err
	}

	context.AfterFunc(i.ctx, func() {
		_ = i.RemoveEventHandler(registration)
	})

	return registration, nil
}
