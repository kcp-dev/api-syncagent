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
	"strings"

	"github.com/kcp-dev/kcp/pkg/crdpuller"

	"k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/endpoints/openapi"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/kube-openapi/pkg/util/proto"
)

type Client struct {
	discoveryClient discovery.DiscoveryInterface
}

func NewClient(config *rest.Config) (*Client, error) {
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, err
	}

	return &Client{
		discoveryClient: discoveryClient,
	}, nil
}

func (c *Client) RetrieveCRD(ctx context.Context, gvk schema.GroupVersionKind) (*apiextensionsv1.CustomResourceDefinition, error) {
	openapiSchema, err := c.discoveryClient.OpenAPISchema()
	if err != nil {
		return nil, err
	}

	// Most of this code follows the logic in kcp's crd-puller, but is slimmed down
	// to a) only support openapi and b) extract a specific version, not necessarily
	// the preferred version.

	models, err := proto.NewOpenAPIData(openapiSchema)
	if err != nil {
		return nil, err
	}
	modelsByGKV, err := openapi.GetModelsByGKV(models)
	if err != nil {
		return nil, err
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

	_, resourceLists, err := c.discoveryClient.ServerGroupsAndResources()
	if err != nil {
		return nil, err
	}

	var resource *metav1.APIResource
	allResourceNames := sets.New[string]()
	for _, resList := range resourceLists {
		for _, res := range resList.APIResources {
			allResourceNames.Insert(res.Name)

			// find the requested resource based on the Kind, but ensure that subresources
			// are not misinterpreted as the main resource by checking for "/"
			if resList.GroupVersion == gvk.GroupVersion().String() && res.Kind == gvk.Kind && !strings.Contains(res.Name, "/") {
				resource = &res
			}
		}
	}

	if resource == nil {
		return nil, fmt.Errorf("could not find %v in APIs", gvk)
	}

	hasSubResource := func(subResource string) bool {
		return allResourceNames.Has(resource.Name + "/" + subResource)
	}

	var statusSubResource *apiextensionsv1.CustomResourceSubresourceStatus
	if hasSubResource("status") {
		statusSubResource = &apiextensionsv1.CustomResourceSubresourceStatus{}
	}

	var scaleSubResource *apiextensionsv1.CustomResourceSubresourceScale
	if hasSubResource("scale") {
		scaleSubResource = &apiextensionsv1.CustomResourceSubresourceScale{
			SpecReplicasPath:   ".spec.replicas",
			StatusReplicasPath: ".status.replicas",
		}
	}

	scope := apiextensionsv1.ClusterScoped
	if resource.Namespaced {
		scope = apiextensionsv1.NamespaceScoped
	}

	crd := &apiextensionsv1.CustomResourceDefinition{
		TypeMeta: metav1.TypeMeta{
			Kind:       "CustomResourceDefinition",
			APIVersion: "apiextensions.k8s.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s.%s", resource.Name, gvk.Group),
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: gvk.Group,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name: gvk.Version,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: &schemaProps,
					},
					Subresources: &apiextensionsv1.CustomResourceSubresources{
						Status: statusSubResource,
						Scale:  scaleSubResource,
					},
					Served:  true,
					Storage: true,
				},
			},
			Scope: scope,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:     resource.Name,
				Kind:       resource.Kind,
				Categories: resource.Categories,
				ShortNames: resource.ShortNames,
				Singular:   resource.SingularName,
			},
		},
	}

	apiextensionsv1.SetDefaults_CustomResourceDefinition(crd)

	if apihelpers.IsProtectedCommunityGroup(gvk.Group) {
		crd.Annotations = map[string]string{
			apiextensionsv1.KubeAPIApprovedAnnotation: "https://github.com/kcp-dev/kubernetes/pull/4",
		}
	}

	return crd, nil
}
