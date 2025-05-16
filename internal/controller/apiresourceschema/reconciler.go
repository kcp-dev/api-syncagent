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

package apiresourceschema

import (
	"github.com/kcp-dev/api-syncagent/internal/resources/reconciling"
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	kcpapisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	kcpdevv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

func (r *Reconciler) createAPIConversionReconciler(name string, agentName string, rules []kcpapisv1alpha1.APIVersionConversion) reconciling.NamedAPIConversionReconcilerFactory {
	return func() (string, reconciling.APIConversionReconciler) {
		return name, func(existing *kcpdevv1alpha1.APIConversion) (*kcpdevv1alpha1.APIConversion, error) {
			if existing.Annotations == nil {
				existing.Annotations = map[string]string{}
			}
			existing.Annotations[syncagentv1alpha1.AgentNameAnnotation] = agentName

			existing.Spec = kcpdevv1alpha1.APIConversionSpec{
				Conversions: rules,
			}

			return existing, nil
		}
	}
}
