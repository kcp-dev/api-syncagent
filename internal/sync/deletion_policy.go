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
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	deletionPropagationPolicyAnnotation = "syncagent.kcp.io/deletion-propagation-policy"
	deletionPropagationBackground       = "background"
	deletionPropagationForeground       = "foreground"
	deletionPropagationOrphan           = "orphan"
)

// deletionPropagationPolicy resolves the policy specified for the
// service-cluster copy. Missing or unsupported values mean background deletion.
func deletionPropagationPolicy(obj metav1.Object) metav1.DeletionPropagation {
	switch obj.GetAnnotations()[deletionPropagationPolicyAnnotation] {
	case deletionPropagationForeground:
		return metav1.DeletePropagationForeground
	case deletionPropagationOrphan:
		return metav1.DeletePropagationOrphan
	default:
		return metav1.DeletePropagationBackground
	}
}

func relatedDeletionPropagationPolicy(
	origin syncagentv1alpha1.RelatedResourceOrigin,
	obj metav1.Object,
) metav1.DeletionPropagation {
	if origin != syncagentv1alpha1.RelatedResourceOriginKcp {
		return metav1.DeletePropagationBackground
	}

	return deletionPropagationPolicy(obj)
}

func normalizeDeletionPropagationPolicy(policy metav1.DeletionPropagation) metav1.DeletionPropagation {
	switch policy {
	case metav1.DeletePropagationForeground, metav1.DeletePropagationOrphan:
		return policy
	default:
		return metav1.DeletePropagationBackground
	}
}
