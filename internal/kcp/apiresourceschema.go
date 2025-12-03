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

package kcp

import (
	"fmt"

	"github.com/kcp-dev/api-syncagent/internal/crypto"
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	kcpdevv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func CreateAPIResourceSchema(crd *apiextensionsv1.CustomResourceDefinition, name string, agentName string) (*kcpdevv1alpha1.APIResourceSchema, error) {
	// prefix is irrelevant as the name is overridden later
	converted, err := kcpdevv1alpha1.CRDToAPIResourceSchema(crd, "irrelevant")
	if err != nil {
		return nil, fmt.Errorf("failed to convert CRD: %w", err)
	}

	ars := &kcpdevv1alpha1.APIResourceSchema{}
	ars.TypeMeta = metav1.TypeMeta{
		APIVersion: kcpdevv1alpha1.SchemeGroupVersion.String(),
		Kind:       "APIResourceSchema",
	}

	ars.Name = name
	ars.Annotations = map[string]string{
		syncagentv1alpha1.SourceGenerationAnnotation: fmt.Sprintf("%d", crd.Generation),
		syncagentv1alpha1.AgentNameAnnotation:        agentName,
	}
	ars.Labels = map[string]string{
		syncagentv1alpha1.AgentNameLabel: agentName,
	}
	ars.Spec.Group = converted.Spec.Group
	ars.Spec.Names = converted.Spec.Names
	ars.Spec.Scope = converted.Spec.Scope
	ars.Spec.Versions = converted.Spec.Versions

	if len(converted.Spec.Versions) > 1 {
		ars.Spec.Conversion = &kcpdevv1alpha1.CustomResourceConversion{
			// as of kcp 0.27, there is no constant for this
			Strategy: kcpdevv1alpha1.ConversionStrategyType("None"),
		}
	}

	return ars, nil
}

// GetAPIResourceSchemaName generates the name for the ARS in kcp. Note that
// kcp requires, just like CRDs, that ARS are named following a specific pattern.
func GetAPIResourceSchemaName(crd *apiextensionsv1.CustomResourceDefinition) string {
	crd = crd.DeepCopy()
	crd.Spec.Conversion = nil

	checksum := crypto.Hash(crd.Spec)

	// include a leading "v" to prevent SHA-1 hashes with digits to break the name
	return fmt.Sprintf("v%s.%s.%s", checksum[:8], crd.Spec.Names.Plural, crd.Spec.Group)
}
