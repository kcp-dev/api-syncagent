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

package discovery

import (
	"context"
	"fmt"

	kcpapisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

type ResourceProber struct {
	name   string
	config *rest.Config
	client ctrlruntimeclient.Client
}

func NewResourceProber(endpointSliceWorkspaceConfig *rest.Config, endpointSliceWorkspaceClient ctrlruntimeclient.Client, endpointSliceName string) *ResourceProber {
	return &ResourceProber{
		name:   endpointSliceName,
		config: endpointSliceWorkspaceConfig,
		client: endpointSliceWorkspaceClient,
	}
}

func (p *ResourceProber) HasGVR(ctx context.Context, gvr schema.GroupVersionResource) (bool, error) {
	return p.hasAPIThing(ctx, func(apiGroup, version, resource, _ string) bool {
		return apiGroup == gvr.Group && version == gvr.Version && resource == gvr.Resource
	})
}

func (p *ResourceProber) HasGVK(ctx context.Context, gvk schema.GroupVersionKind) (bool, error) {
	return p.hasAPIThing(ctx, func(apiGroup, version, _, kind string) bool {
		return apiGroup == gvk.Group && version == gvk.Version && kind == gvk.Kind
	})
}

func (p *ResourceProber) hasAPIThing(ctx context.Context, match matchFunc) (bool, error) {
	endpointSlice := &kcpapisv1alpha1.APIExportEndpointSlice{}
	if err := p.client.Get(ctx, types.NamespacedName{Name: p.name}, endpointSlice); err != nil {
		return false, fmt.Errorf("failed to get APIExportEndpointSlice: %w", err)
	}

	// connect to each of the endpoints and check if the resource is available there
	for _, endpoint := range endpointSlice.Status.APIExportEndpoints {
		has, err := p.hasAPIThingInEndpoint(endpoint.URL, match)
		if err != nil || !has {
			return has, err
		}
	}

	return true, nil
}

type matchFunc func(apiGroup, version, resource, kind string) bool

func (p *ResourceProber) hasAPIThingInEndpoint(endpoint string, match matchFunc) (bool, error) {
	endpointConfig := rest.CopyConfig(p.config)
	endpointConfig.Host = endpoint + "/clusters/*"

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(endpointConfig)
	if err != nil {
		return false, fmt.Errorf("failed to create discovery client: %w", err)
	}

	_, resourceLists, err := discoveryClient.ServerGroupsAndResources()
	if err != nil {
		return false, fmt.Errorf("failed to discover APIs: %w", err)
	}

	for _, resList := range resourceLists {
		// .Group on an APIResource is empty for built-in resources, so we must
		// parse and check GroupVersion of the entire list.
		gv, err := schema.ParseGroupVersion(resList.GroupVersion)
		if err != nil {
			return false, fmt.Errorf("invalid API group version %q reported by Kubernetes: %w", resList.GroupVersion, err)
		}

		for _, res := range resList.APIResources {
			// res.Version is empty for built-in resources
			if match(gv.Group, gv.Version, res.Name, res.Kind) {
				return true, nil
			}
		}
	}

	return false, nil
}
