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

// Code generated by applyconfiguration-gen. DO NOT EDIT.

package v1alpha1

import (
	v1 "k8s.io/client-go/applyconfigurations/meta/v1"
)

// RelatedResourceObjectSelectorApplyConfiguration represents a declarative configuration of the RelatedResourceObjectSelector type for use
// with apply.
type RelatedResourceObjectSelectorApplyConfiguration struct {
	v1.LabelSelectorApplyConfiguration `json:",inline"`
	Rewrite                            *RelatedResourceSelectorRewriteApplyConfiguration `json:"rewrite,omitempty"`
}

// RelatedResourceObjectSelectorApplyConfiguration constructs a declarative configuration of the RelatedResourceObjectSelector type for use with
// apply.
func RelatedResourceObjectSelector() *RelatedResourceObjectSelectorApplyConfiguration {
	return &RelatedResourceObjectSelectorApplyConfiguration{}
}

// WithMatchLabels puts the entries into the MatchLabels field in the declarative configuration
// and returns the receiver, so that objects can be build by chaining "With" function invocations.
// If called multiple times, the entries provided by each call will be put on the MatchLabels field,
// overwriting an existing map entries in MatchLabels field with the same key.
func (b *RelatedResourceObjectSelectorApplyConfiguration) WithMatchLabels(entries map[string]string) *RelatedResourceObjectSelectorApplyConfiguration {
	if b.MatchLabels == nil && len(entries) > 0 {
		b.MatchLabels = make(map[string]string, len(entries))
	}
	for k, v := range entries {
		b.MatchLabels[k] = v
	}
	return b
}

// WithMatchExpressions adds the given value to the MatchExpressions field in the declarative configuration
// and returns the receiver, so that objects can be build by chaining "With" function invocations.
// If called multiple times, values provided by each call will be appended to the MatchExpressions field.
func (b *RelatedResourceObjectSelectorApplyConfiguration) WithMatchExpressions(values ...*v1.LabelSelectorRequirementApplyConfiguration) *RelatedResourceObjectSelectorApplyConfiguration {
	for i := range values {
		if values[i] == nil {
			panic("nil value passed to WithMatchExpressions")
		}
		b.MatchExpressions = append(b.MatchExpressions, *values[i])
	}
	return b
}

// WithRewrite sets the Rewrite field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Rewrite field is set to the value of the last call.
func (b *RelatedResourceObjectSelectorApplyConfiguration) WithRewrite(value *RelatedResourceSelectorRewriteApplyConfiguration) *RelatedResourceObjectSelectorApplyConfiguration {
	b.Rewrite = value
	return b
}
