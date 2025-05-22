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

package projection

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/version"
)

// PublishedResourceSourceGK returns the source GK of the local resources
// that are supposed to be published.
func PublishedResourceSourceGK(pubRes *syncagentv1alpha1.PublishedResource) schema.GroupKind {
	return schema.GroupKind{
		Group: pubRes.Spec.Resource.APIGroup,
		Kind:  pubRes.Spec.Resource.Kind,
	}
}

func PublishedResourceSourceGVK(crd *apiextensionsv1.CustomResourceDefinition, pubRes *syncagentv1alpha1.PublishedResource) (schema.GroupVersionKind, error) {
	storageVersion := getStorageVersion(crd)
	if storageVersion == "" {
		return schema.GroupVersionKind{}, errors.New("CRD does not contain a storage version")
	}

	sourceGV := PublishedResourceSourceGK(pubRes)

	return schema.GroupVersionKind{
		Group:   sourceGV.Group,
		Version: storageVersion,
		Kind:    sourceGV.Kind,
	}, nil
}

func getStorageVersion(crd *apiextensionsv1.CustomResourceDefinition) string {
	for _, version := range crd.Spec.Versions {
		if version.Storage {
			return version.Name
		}
	}

	return ""
}

// PublishedResourceProjectedGVK returns the effective GVK after the projection
// rules have been applied according to the PublishedResource.
func PublishedResourceProjectedGVK(originalCRD *apiextensionsv1.CustomResourceDefinition, pubRes *syncagentv1alpha1.PublishedResource) (schema.GroupVersionKind, error) {
	projectedCRD, err := ProjectCRD(originalCRD, pubRes)
	if err != nil {
		return schema.GroupVersionKind{}, fmt.Errorf("failed to project CRD: %w", err)
	}

	storageVersion := getStorageVersion(projectedCRD)
	if storageVersion == "" {
		return schema.GroupVersionKind{}, errors.New("projected CRD does not contain a storage version")
	}

	return schema.GroupVersionKind{
		Group:   projectedCRD.Spec.Group,
		Version: storageVersion,
		Kind:    projectedCRD.Spec.Names.Kind,
	}, nil
}

func ProjectCRD(crd *apiextensionsv1.CustomResourceDefinition, pubRes *syncagentv1alpha1.PublishedResource) (*apiextensionsv1.CustomResourceDefinition, error) {
	result := crd.DeepCopy()

	// reduce the CRD down to the selected versions
	result, err := stripUnwantedVersions(result, pubRes)
	if err != nil {
		return nil, err
	}

	// if there is no storage version left, we use the latest served version
	result, err = adjustStorageVersion(result)
	if err != nil {
		return nil, err
	}

	// now we get to actually project something, if desired
	result, err = projectCRDVersions(result, pubRes)
	if err != nil {
		return nil, err
	}

	result, err = projectCRDNames(result, pubRes)
	if err != nil {
		return nil, err
	}

	return result, nil
}

func stripUnwantedVersions(crd *apiextensionsv1.CustomResourceDefinition, pubRes *syncagentv1alpha1.PublishedResource) (*apiextensionsv1.CustomResourceDefinition, error) {
	src := pubRes.Spec.Resource

	//nolint:staticcheck
	if src.Version != "" && len(src.Versions) > 0 {
		return nil, errors.New("cannot configure both .version and .versions in as the source of a PublishedResource")
	}

	crd.Spec.Versions = slices.DeleteFunc(crd.Spec.Versions, func(ver apiextensionsv1.CustomResourceDefinitionVersion) bool {
		switch {
		//nolint:staticcheck
		case src.Version != "":
			//nolint:staticcheck
			return ver.Name != src.Version
		case len(src.Versions) > 0:
			return !slices.Contains(src.Versions, ver.Name)
		default:
			return false // i.e. keep all versions by default
		}
	})

	if len(crd.Spec.Versions) == 0 {
		switch {
		//nolint:staticcheck
		case src.Version != "":
			//nolint:staticcheck
			return nil, fmt.Errorf("CRD does not contain version %s", src.Version)
		case len(src.Versions) > 0:
			return nil, fmt.Errorf("CRD does not contain any of versions %v", src.Versions)
		default:
			return nil, errors.New("CRD contains no versions")
		}
	}

	return crd, nil
}

func adjustStorageVersion(crd *apiextensionsv1.CustomResourceDefinition) (*apiextensionsv1.CustomResourceDefinition, error) {
	var hasStorage bool
	latestServed := -1
	for i, v := range crd.Spec.Versions {
		if v.Storage {
			hasStorage = true
		}
		if v.Served {
			latestServed = i
		}
	}

	if latestServed < 0 {
		return nil, errors.New("no CRD version selected that is marked as served")
	}

	if !hasStorage {
		crd.Spec.Versions[latestServed].Storage = true
	}

	return crd, nil
}

func projectCRDVersions(crd *apiextensionsv1.CustomResourceDefinition, pubRes *syncagentv1alpha1.PublishedResource) (*apiextensionsv1.CustomResourceDefinition, error) {
	projection := pubRes.Spec.Projection
	if projection == nil {
		return crd, nil
	}

	// We already validated that Version and Versions can be set at the same time.

	//nolint:staticcheck
	if projection.Version != "" {
		if size := len(crd.Spec.Versions); size != 1 {
			return nil, fmt.Errorf("cannot project CRD version to a single version %q because it contains %d versions", projection.Version, size)
		}

		//nolint:staticcheck
		crd.Spec.Versions[0].Name = projection.Version
	} else if len(projection.Versions) > 0 {
		for idx, version := range crd.Spec.Versions {
			oldVersion := version.Name

			if newVersion := projection.Versions[oldVersion]; newVersion != "" {
				crd.Spec.Versions[idx].Name = newVersion
			}
		}

		// ensure we ended up with a unique set of versions
		knownVersions := sets.New[string]()
		for _, version := range crd.Spec.Versions {
			if knownVersions.Has(version.Name) {
				return nil, fmt.Errorf("CRD contains multiple entries for %s after applying mutation rules", version.Name)
			}
			knownVersions.Insert(version.Name)
		}

		// ensure proper Kubernetes-style version order
		slices.SortFunc(crd.Spec.Versions, func(a, b apiextensionsv1.CustomResourceDefinitionVersion) int {
			return version.CompareKubeAwareVersionStrings(a.Name, b.Name)
		})
	}

	return crd, nil
}

func projectCRDNames(crd *apiextensionsv1.CustomResourceDefinition, pubRes *syncagentv1alpha1.PublishedResource) (*apiextensionsv1.CustomResourceDefinition, error) {
	projection := pubRes.Spec.Projection
	if projection == nil {
		return crd, nil
	}

	if projection.Group != "" {
		crd.Spec.Group = projection.Group
	}

	if projection.Kind != "" {
		crd.Spec.Names.Kind = projection.Kind
		crd.Spec.Names.ListKind = projection.Kind + "List"

		crd.Spec.Names.Singular = strings.ToLower(crd.Spec.Names.Kind)
		crd.Spec.Names.Plural = crd.Spec.Names.Singular + "s"
	}

	if projection.Plural != "" {
		crd.Spec.Names.Plural = projection.Plural
	}

	if projection.Scope != "" {
		crd.Spec.Scope = apiextensionsv1.ResourceScope(projection.Scope)
	}

	if projection.Categories != nil {
		crd.Spec.Names.Categories = projection.Categories
	}

	if projection.ShortNames != nil {
		crd.Spec.Names.ShortNames = projection.ShortNames
	}

	// re-calculate CRD name
	crd.Name = fmt.Sprintf("%s.%s", crd.Spec.Names.Plural, crd.Spec.Group)

	return crd, nil
}
