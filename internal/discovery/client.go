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
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kcp-dev/kcp/pkg/crdpuller"

	"k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsv1client "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/apiserver/pkg/endpoints/openapi"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/kube-openapi/pkg/util/proto"
	"k8s.io/utils/ptr"
)

type Client struct {
	discoveryClient discovery.DiscoveryInterface
	crdClient       apiextensionsv1client.ApiextensionsV1Interface
}

func NewClient(config *rest.Config) (*Client, error) {
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, err
	}

	crdClient, err := apiextensionsv1client.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return &Client{
		discoveryClient: discoveryClient,
		crdClient:       crdClient,
	}, nil
}

func (c *Client) RetrieveCRD(ctx context.Context, gk schema.GroupKind) (*apiextensionsv1.CustomResourceDefinition, error) {
	////////////////////////////////////
	// Resolve GK into GR, because we need the resource name to construct
	// the full CRD name.

	_, resourceLists, err := c.discoveryClient.ServerGroupsAndResources()
	if err != nil {
		return nil, err
	}

	// resource is the resource described by gk in any of the found versions
	var resource *metav1.APIResource

	availableVersions := sets.New[string]()
	subresourcesPerVersion := map[string]sets.Set[string]{}

	for _, resList := range resourceLists {
		// .Group on an APIResource is empty for built-in resources, so we must
		// parse and check GroupVersion of the entire list.
		gv, err := schema.ParseGroupVersion(resList.GroupVersion)
		if err != nil {
			return nil, fmt.Errorf("Kubernetes reported invalid API group version %q: %w", resList.GroupVersion, err)
		}

		if gv.Group != gk.Group {
			continue
		}

		for _, res := range resList.APIResources {
			if res.Kind != gk.Kind {
				continue
			}

			// res could describe the main resource or one of its subresources.
			var subresource string
			if strings.Contains(res.Name, "/") {
				parts := strings.SplitN(res.Name, "/", 2)
				subresource = parts[1]
			}

			if subresource == "" {
				resource = &res
			} else {
				list, ok := subresourcesPerVersion[res.Version]
				if !ok {
					list = sets.New[string]()
				}
				list.Insert(subresource)
				subresourcesPerVersion[res.Version] = list
			}

			// res.Version is also empty for built-in resources
			availableVersions.Insert(gv.Version)
		}
	}

	if resource == nil {
		return nil, fmt.Errorf("could not find %v in APIs", gk)
	}

	// fill-in the missing Group for built-in resources
	resource.Group = gk.Group

	////////////////////////////////////
	// If possible, retrieve the GK as its original CRD, which is always preferred
	// because it's much more precise than what we can retrieve from the OpenAPI.
	// If no CRD can be found, fallback to the OpenAPI schema.

	crdName := resource.Name
	if gk.Group == "" {
		crdName += ".core"
	} else {
		crdName += "." + gk.Group
	}

	crd, err := c.crdClient.CustomResourceDefinitions().Get(ctx, crdName, metav1.GetOptions{})

	// Hooray, we found a CRD! There is so much goodness on a real CRD that instead
	// of re-creating it later on based on the openapi schema, we take the original
	// CRD and just strip it down to what we need.
	if err == nil {
		if apihelpers.IsCRDConditionTrue(crd, apiextensionsv1.NonStructuralSchema) {
			emptySchema := &apiextensionsv1.CustomResourceValidation{
				OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
					Type:                   "object",
					XPreserveUnknownFields: ptr.To(true),
				},
			}

			for i, version := range crd.Spec.Versions {
				if version.Schema == nil || version.Schema.OpenAPIV3Schema == nil {
					version.Schema = emptySchema
					crd.Spec.Versions[i] = version
				}
			}
		}

		crd.APIVersion = apiextensionsv1.SchemeGroupVersion.Identifier()
		crd.Kind = "CustomResourceDefinition"

		// cleanup object meta
		oldMeta := crd.ObjectMeta
		crd.ObjectMeta = metav1.ObjectMeta{
			Name:        oldMeta.Name,
			Annotations: filterAnnotations(oldMeta.Annotations),
			Generation:  oldMeta.Generation, // is stored as an annotation for convenience on the ARS
		}
		crd.Status.Conditions = []apiextensionsv1.CustomResourceDefinitionCondition{}

		// There is only ever one version, so conversion rules do not make sense
		// (and even if they did, the conversion webhook from the service cluster
		// would not be available in kcp anyway).
		crd.Spec.Conversion = &apiextensionsv1.CustomResourceConversion{
			Strategy: apiextensionsv1.NoneConverter,
		}

		return crd, nil
	}

	// any non-404 error is permanent
	if !apierrors.IsNotFound(err) {
		return nil, err
	}

	////////////////////////////////////
	// CRD not found, so fall back to using the OpenAPI schema

	openapiSchema, err := c.discoveryClient.OpenAPISchema()
	if err != nil {
		return nil, err
	}

	preferredVersion, err := c.getPreferredVersion(resource)
	if err != nil {
		return nil, err
	}

	if preferredVersion == "" {
		return nil, errors.New("cannot determine storage version because no preferred version exists in the schema")
	}

	models, err := proto.NewOpenAPIData(openapiSchema)
	if err != nil {
		return nil, err
	}

	modelsByGKV, err := openapi.GetModelsByGKV(models)
	if err != nil {
		return nil, err
	}

	// prepare an empty CRD
	scope := apiextensionsv1.ClusterScoped
	if resource.Namespaced {
		scope = apiextensionsv1.NamespaceScoped
	}

	out := &apiextensionsv1.CustomResourceDefinition{
		TypeMeta: metav1.TypeMeta{
			Kind:       "CustomResourceDefinition",
			APIVersion: apiextensionsv1.SchemeGroupVersion.Identifier(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: crdName,
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group:    gk.Group,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{},
			Scope:    scope,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:     resource.Name,
				Kind:       resource.Kind,
				Categories: resource.Categories,
				ShortNames: resource.ShortNames,
				Singular:   resource.SingularName,
			},
		},
	}

	// fill-in the schema for each version, making sure that versions are sorted
	// according to Kubernetes rules.
	sortedVersions := availableVersions.UnsortedList()
	slices.SortFunc(sortedVersions, version.CompareKubeAwareVersionStrings)

	for _, version := range sortedVersions {
		subresources := subresourcesPerVersion[version]
		gvk := schema.GroupVersionKind{
			Group:   gk.Group,
			Version: version,
			Kind:    gk.Kind,
		}

		protoSchema := modelsByGKV[gvk]
		if protoSchema == nil {
			return nil, fmt.Errorf("no models for %v", gvk)
		}

		var schemaProps apiextensionsv1.JSONSchemaProps
		errs := crdpuller.Convert(protoSchema, &schemaProps)
		if len(errs) > 0 {
			return nil, utilerrors.NewAggregate(errs)
		}

		var statusSubResource *apiextensionsv1.CustomResourceSubresourceStatus
		if subresources.Has("status") {
			statusSubResource = &apiextensionsv1.CustomResourceSubresourceStatus{}
		}

		var scaleSubResource *apiextensionsv1.CustomResourceSubresourceScale
		if subresources.Has("scale") {
			scaleSubResource = &apiextensionsv1.CustomResourceSubresourceScale{
				SpecReplicasPath:   ".spec.replicas",
				StatusReplicasPath: ".status.replicas",
			}
		}

		out.Spec.Versions = append(out.Spec.Versions, apiextensionsv1.CustomResourceDefinitionVersion{
			Name: version,
			Schema: &apiextensionsv1.CustomResourceValidation{
				OpenAPIV3Schema: &schemaProps,
			},
			Subresources: &apiextensionsv1.CustomResourceSubresources{
				Status: statusSubResource,
				Scale:  scaleSubResource,
			},
			Served:  true,
			Storage: version == preferredVersion,
		})
	}

	apiextensionsv1.SetDefaults_CustomResourceDefinition(out)

	if apihelpers.IsProtectedCommunityGroup(gk.Group) {
		out.Annotations = map[string]string{
			apiextensionsv1.KubeAPIApprovedAnnotation: "https://github.com/kcp-dev/kubernetes/pull/4",
		}
	}

	return out, nil
}

func (c *Client) getPreferredVersion(resource *metav1.APIResource) (string, error) {
	result, err := c.discoveryClient.ServerPreferredResources()
	if err != nil {
		return "", err
	}

	for _, resList := range result {
		// .Group on an APIResource is empty for built-in resources, so we must
		// parse and check GroupVersion of the entire list.
		gv, err := schema.ParseGroupVersion(resList.GroupVersion)
		if err != nil {
			return "", fmt.Errorf("Kubernetes reported invalid API group version %q: %w", resList.GroupVersion, err)
		}

		if gv.Group != resource.Group {
			continue
		}

		for _, res := range resList.APIResources {
			if res.Name == resource.Name {
				// res.Version is empty for built-in resources
				return gv.Version, nil
			}
		}
	}

	return "", nil
}

func filterAnnotations(ann map[string]string) map[string]string {
	allowlist := []string{
		apiextensionsv1.KubeAPIApprovedAnnotation,
	}

	out := map[string]string{}
	for k, v := range ann {
		if slices.Contains(allowlist, k) {
			out[k] = v
		}
	}

	return out
}
