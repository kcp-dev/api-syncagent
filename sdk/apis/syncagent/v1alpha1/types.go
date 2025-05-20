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

package v1alpha1

const (
	// AgentNameAnnotation records which Sync Agent has created an APIResourceSchema.
	AgentNameAnnotation = "syncagent.kcp.io/agent-name"

	// AgentNameLabel records which Sync Agent has created an APIResourceSchema.
	AgentNameLabel = "syncagent.kcp.io/agent-name"

	// SourceGenerationAnnotation is the annotation on APIResourceSchemas that tells us
	// what generation of the CRD it was based on. This can be helpful in debugging,
	// as ARS resources cannot be updated, i.e. changes to CRDs are not reflected in ARS.
	SourceGenerationAnnotation = "syncagent.kcp.io/source-generation"
)
