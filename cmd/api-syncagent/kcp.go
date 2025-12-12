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

package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/kcp-dev/api-syncagent/internal/kcp"

	"github.com/kcp-dev/logicalcluster/v3"
	kcpapisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	kcpcore "github.com/kcp-dev/sdk/apis/core"
	kcpcorev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"

	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
)

// setupEndpointKcpCluster sets up a plain, non-kcp-aware ctrl-runtime Cluster object
// that is solvely used to watch whichever object holds the virtual workspace URLs,
// either the APIExport or the APIExportEndpointSlice.
func setupEndpointKcpCluster(endpointSlice qualifiedAPIExportEndpointSlice) (cluster.Cluster, error) {
	scheme := runtime.NewScheme()

	if err := kcpapisv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to register scheme %s: %w", kcpapisv1alpha1.SchemeGroupVersion, err)
	}

	if err := kcpcorev1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to register scheme %s: %w", kcpcorev1alpha1.SchemeGroupVersion, err)
	}

	// RBAC in kcp might be very tight and might not allow to list/watch all objects;
	// restrict the cache's selectors accordingly so we can still make use of caching.
	byObject := map[ctrlruntimeclient.Object]cache.ByObject{
		&kcpapisv1alpha1.APIExportEndpointSlice{}: {
			Field: fields.SelectorFromSet(fields.Set{"metadata.name": endpointSlice.Name}),
		},
	}

	return cluster.New(endpointSlice.Config, func(o *cluster.Options) {
		o.Scheme = scheme
		o.Cache = cache.Options{
			Scheme:   scheme,
			ByObject: byObject,
		}
	})
}

// setupManagedKcpCluster sets up a plain, non-kcp-aware ctrl-runtime Cluster object
// that is solvely used to manage the APIExport and APIResourceSchemas.
func setupManagedKcpCluster(apiExport qualifiedAPIExport) (cluster.Cluster, error) {
	scheme := runtime.NewScheme()

	if err := kcpapisv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to register scheme %s: %w", kcpapisv1alpha1.SchemeGroupVersion, err)
	}

	if err := kcpcorev1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to register scheme %s: %w", kcpcorev1alpha1.SchemeGroupVersion, err)
	}

	// RBAC in kcp might be very tight and might not allow to list/watch all objects;
	// restrict the cache's selectors accordingly so we can still make use of caching.
	byObject := map[ctrlruntimeclient.Object]cache.ByObject{
		&kcpapisv1alpha1.APIExport{}: {
			Field: fields.SelectorFromSet(fields.Set{"metadata.name": apiExport.Name}),
		},
	}

	return cluster.New(apiExport.Config, func(o *cluster.Options) {
		o.Scheme = scheme
		o.Cache = cache.Options{
			Scheme:   scheme,
			ByObject: byObject,
		}
	})
}

type qualifiedCluster struct {
	Cluster logicalcluster.Name
	Path    logicalcluster.Path
	Config  *rest.Config
}

type qualifiedAPIExport struct {
	*kcpapisv1alpha1.APIExport
	qualifiedCluster
}

type qualifiedAPIExportEndpointSlice struct {
	*kcpapisv1alpha1.APIExportEndpointSlice
	qualifiedCluster
}

type syncEndpoint struct {
	APIExport     qualifiedAPIExport
	EndpointSlice qualifiedAPIExportEndpointSlice
}

// resolveSyncEndpoint takes the user provided (usually via CLI flags) APIExportEndpointSliceRef and
// APIExportRef and resolves, returning a consistent SyncEndpoint. The initialRestConfig must point
// to the cluster where either of the two objects reside (i.e. if the APIExportRef is given, it
// must point to the cluster where the APIExport lives, and vice versa for the endpoint slice;
// however the endpoint slice references an APIExport in potentially another cluster, and for this
// case the initialRestConfig will be rewritten accordingly).
func resolveSyncEndpoint(ctx context.Context, initialRestConfig *rest.Config, endpointSliceRef string) (*syncEndpoint, error) {
	// construct temporary, uncached client
	scheme := runtime.NewScheme()
	if err := kcpcorev1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to register scheme %s: %w", kcpcorev1alpha1.SchemeGroupVersion, err)
	}
	if err := kcpapisv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to register scheme %s: %w", kcpapisv1alpha1.SchemeGroupVersion, err)
	}

	clientOpts := ctrlruntimeclient.Options{Scheme: scheme}
	client, err := ctrlruntimeclient.New(initialRestConfig, clientOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to create service reader: %w", err)
	}

	se := &syncEndpoint{}

	// First we find the APIExportEndpointSlice.
	endpointSlice, err := resolveAPIExportEndpointSlice(ctx, client, endpointSliceRef)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve APIExportEndpointSlice: %w", err)
	}
	endpointSlice.Config = initialRestConfig

	// Now we find the APIExport referenced in the APIExportEndpointSlice.
	restConfig, err := retargetRestConfig(initialRestConfig, endpointSlice.Spec.APIExport.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to re-target the given kubeconfig to cluster %q: %w", endpointSlice.Spec.APIExport.Path, err)
	}

	client, err = ctrlruntimeclient.New(restConfig, clientOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to create service reader: %w", err)
	}

	apiExport, err := resolveAPIExport(ctx, client, endpointSlice.Spec.APIExport.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve APIExport: %w", err)
	}
	apiExport.Config = restConfig

	se.APIExport = apiExport
	se.EndpointSlice = endpointSlice

	return se, nil
}

func resolveAPIExportEndpointSlice(ctx context.Context, client ctrlruntimeclient.Client, ref string) (qualifiedAPIExportEndpointSlice, error) {
	endpointSlice := &kcpapisv1alpha1.APIExportEndpointSlice{}
	key := types.NamespacedName{Name: ref}
	if err := client.Get(ctx, key, endpointSlice); err != nil {
		return qualifiedAPIExportEndpointSlice{}, fmt.Errorf("failed to get APIExportEndpointSlice %q: %w", ref, err)
	}

	lcName, lcPath, err := resolveCurrentCluster(ctx, client)
	if err != nil {
		return qualifiedAPIExportEndpointSlice{}, fmt.Errorf("failed to resolve APIExportEndpointSlice cluster: %w", err)
	}

	return qualifiedAPIExportEndpointSlice{
		APIExportEndpointSlice: endpointSlice,
		qualifiedCluster: qualifiedCluster{
			Cluster: lcName,
			Path:    lcPath,
		},
	}, nil
}

func resolveAPIExport(ctx context.Context, client ctrlruntimeclient.Client, ref string) (qualifiedAPIExport, error) {
	apiExport := &kcpapisv1alpha1.APIExport{}
	key := types.NamespacedName{Name: ref}
	if err := client.Get(ctx, key, apiExport); err != nil {
		return qualifiedAPIExport{}, fmt.Errorf("failed to get APIExport %q: %w", ref, err)
	}

	lcName, lcPath, err := resolveCurrentCluster(ctx, client)
	if err != nil {
		return qualifiedAPIExport{}, fmt.Errorf("failed to resolve APIExport cluster: %w", err)
	}

	return qualifiedAPIExport{
		APIExport: apiExport,
		qualifiedCluster: qualifiedCluster{
			Cluster: lcName,
			Path:    lcPath,
		},
	}, nil
}

func resolveCurrentCluster(ctx context.Context, client ctrlruntimeclient.Client) (logicalcluster.Name, logicalcluster.Path, error) {
	lc := &kcpcorev1alpha1.LogicalCluster{}
	if err := client.Get(ctx, types.NamespacedName{Name: kcp.IdentityClusterName}, lc); err != nil {
		return "", logicalcluster.None, fmt.Errorf("failed to resolve current workspace: %w", err)
	}

	lcName := logicalcluster.From(lc)
	lcPath := logicalcluster.NewPath(lc.Annotations[kcpcore.LogicalClusterPathAnnotationKey])

	return lcName, lcPath, nil
}

var clusterFinder = regexp.MustCompile(`/clusters/([^/]+)`)

func retargetRestConfig(cfg *rest.Config, destination string) (*rest.Config, error) {
	// no change desired (use current cluster implicitly)
	if destination == "" {
		return cfg, nil
	}

	matches := clusterFinder.FindAllStringSubmatch(cfg.Host, -1)
	if len(matches) == 0 {
		return nil, errors.New("URL must point to a cluster/workspace")
	}
	if len(matches) > 1 {
		return nil, errors.New("invalid URL: URL contains more than one cluster path")
	}

	current := matches[0][1]
	if current == destination {
		return cfg, nil
	}

	newCluster := fmt.Sprintf("/clusters/%s", destination)

	newConfig := rest.CopyConfig(cfg)
	newConfig.Host = clusterFinder.ReplaceAllString(cfg.Host, newCluster)

	return newConfig, nil
}
