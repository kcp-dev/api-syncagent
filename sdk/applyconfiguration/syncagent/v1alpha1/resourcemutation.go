/*
Copyright The KCP Authors.

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

// ResourceMutationApplyConfiguration represents a declarative configuration of the ResourceMutation type for use
// with apply.
type ResourceMutationApplyConfiguration struct {
	Delete   *ResourceDeleteMutationApplyConfiguration   `json:"delete,omitempty"`
	Regex    *ResourceRegexMutationApplyConfiguration    `json:"regex,omitempty"`
	Template *ResourceTemplateMutationApplyConfiguration `json:"template,omitempty"`
}

// ResourceMutationApplyConfiguration constructs a declarative configuration of the ResourceMutation type for use with
// apply.
func ResourceMutation() *ResourceMutationApplyConfiguration {
	return &ResourceMutationApplyConfiguration{}
}

// WithDelete sets the Delete field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Delete field is set to the value of the last call.
func (b *ResourceMutationApplyConfiguration) WithDelete(value *ResourceDeleteMutationApplyConfiguration) *ResourceMutationApplyConfiguration {
	b.Delete = value
	return b
}

// WithRegex sets the Regex field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Regex field is set to the value of the last call.
func (b *ResourceMutationApplyConfiguration) WithRegex(value *ResourceRegexMutationApplyConfiguration) *ResourceMutationApplyConfiguration {
	b.Regex = value
	return b
}

// WithTemplate sets the Template field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Template field is set to the value of the last call.
func (b *ResourceMutationApplyConfiguration) WithTemplate(value *ResourceTemplateMutationApplyConfiguration) *ResourceMutationApplyConfiguration {
	b.Template = value
	return b
}
