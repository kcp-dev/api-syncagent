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

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	PlaceholderRemoteClusterName   = "$remoteClusterName"
	PlaceholderRemoteNamespace     = "$remoteNamespace"
	PlaceholderRemoteNamespaceHash = "$remoteNamespaceHash"
	PlaceholderRemoteName          = "$remoteName"
	PlaceholderRemoteNameHash      = "$remoteNameHash"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status

// PublishedResource describes how an API type (usually defined by a CRD)
// on the service cluster should be exposed in kcp workspaces. Besides
// controlling how namespaced and cluster-wide resources should be mapped,
// the GVK can also be transformed to provide a uniform, implementation-independent
// access to the APIs inside kcp.
type PublishedResource struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec PublishedResourceSpec `json:"spec"`

	// Status contains reconciliation information for the published resource.
	Status PublishedResourceStatus `json:"status,omitempty"`
}

// PublishedResourceSpec describes the desired resource publication from a service
// cluster to kcp.
type PublishedResourceSpec struct {
	// Describes the "source" Resource that exists on this, the service cluster,
	// that should be exposed in kcp workspaces. All fields have to be specified.
	Resource SourceResourceDescriptor `json:"resource"`

	// If specified, the filter will be applied to the resources in a workspace
	// and allow restricting which of them will be handled by the Sync Agent.
	Filter *ResourceFilter `json:"filter,omitempty"`

	// Naming can be used to control how the namespace and names for local objects
	// are formed. If not specified, the Sync Agent will use defensive defaults to
	// prevent naming collisions in the service cluster.
	// When configuring this, great care must be taken to not allow for naming
	// collisions to happen; keep in mind that the same name/namespace can exists in
	// many different kcp workspaces.
	Naming *ResourceNaming `json:"naming,omitempty"`

	// EnableWorkspacePaths toggles whether the Sync Agent will not just store the kcp
	// cluster name as a label on each locally synced object, but also the full workspace
	// path. This is optional because it requires additional requests to kcp and
	// should only be used if the workspace path is of interest on the
	// service cluster side.
	EnableWorkspacePaths bool `json:"enableWorkspacePaths,omitempty"`

	// Projection is used to change the GVK of a published resource within kcp.
	// This can be used to hide implementation details and provide a customized API
	// experience to the user.
	// All fields in the projection are optional. If a field is set, it will overwrite
	// that field in the GVK. The namespaced field can be set to turn a cluster-wide
	// resource namespaced or vice-versa.
	Projection *ResourceProjection `json:"projection,omitempty"`

	// Mutation allows to configure "rewrite rules" to modify the objects in both
	// directions during the synchronization.
	Mutation *ResourceMutationSpec `json:"mutation,omitempty"`

	Related []RelatedResourceSpec `json:"related,omitempty"`
}

// ResourceNaming describes how the names for local objects should be formed.
type ResourceNaming struct {
	// The name field allows to control the name the local objects created by the Sync Agent.
	// If left empty, "$remoteNamespaceHash-$remoteNameHash" is assumed. This guarantees unique
	// names as long as the cluster name ($remoteClusterName) is used for the local namespace
	// (the default unless configured otherwise).
	// This is a string with placeholders. The following placeholders can be used:
	//
	//   - $remoteClusterName   -- the kcp workspace's cluster name (e.g. "1084s8ceexsehjm2")
	//   - $remoteNamespace     -- the original namespace used by the consumer inside the kcp
	//                             workspace (if targetNamespace is left empty, it's equivalent
	//                             to setting "$remote_ns")
	//   - $remoteNamespaceHash -- first 20 hex characters of the SHA-1 hash of $remoteNamespace
	//   - $remoteName          -- the original name of the object inside the kcp workspace
	//                             (rarely used to construct local namespace names)
	//   - $remoteNameHash      -- first 20 hex characters of the SHA-1 hash of $remoteName
	//
	Name string `json:"name,omitempty"`

	// For namespaced resources, the this field allows to control where the local objects will
	// be created. If left empty, "$remoteClusterName" is assumed.
	// This is a string with placeholders. The following placeholders can be used:
	//
	//   - $remoteClusterName   -- the kcp workspace's cluster name (e.g. "1084s8ceexsehjm2")
	//   - $remoteNamespace     -- the original namespace used by the consumer inside the kcp
	//                             workspace (if targetNamespace is left empty, it's equivalent
	//                             to setting "$remote_ns")
	//   - $remoteNamespaceHash -- first 20 hex characters of the SHA-1 hash of $remoteNamespace
	//   - $remoteName          -- the original name of the object inside the kcp workspace
	//                             (rarely used to construct local namespace names)
	//   - $remoteNameHash      -- first 20 hex characters of the SHA-1 hash of $remoteName
	//
	Namespace string `json:"namespace,omitempty"`
}

// ResourceMutationSpec allows to configure "rewrite rules" to modify the objects in both
// directions during the synchronization.
type ResourceMutationSpec struct {
	Spec   []ResourceMutation `json:"spec,omitempty"`
	Status []ResourceMutation `json:"status,omitempty"`
}

type ResourceMutation struct {
	// Must use exactly one of these options, never more, never fewer.
	// TODO: Add validation code for this somewhere.

	Delete   *ResourceDeleteMutation   `json:"delete,omitempty"`
	Regex    *ResourceRegexMutation    `json:"regex,omitempty"`
	Template *ResourceTemplateMutation `json:"template,omitempty"`
}

type ResourceDeleteMutation struct {
	Path string `json:"path"`
}

type ResourceRegexMutation struct {
	Path string `json:"path"`
	// Pattern can be left empty to simply replace the entire value with the
	// replacement.
	Pattern     string `json:"pattern,omitempty"`
	Replacement string `json:"replacement,omitempty"`
}

type ResourceTemplateMutation struct {
	Path     string `json:"path"`
	Template string `json:"template"`
}

type RelatedResourceSpec struct {
	// Identifier is a unique name for this related resource. The name must be unique within one
	// PublishedResource and is the key by which consumers (end users) can identify and consume the
	// related resource. Common names are "connection-details" or "credentials".
	// The identifier must be an alphanumeric string.
	Identifier string `json:"identifier"`

	// "service" or "kcp"
	Origin string `json:"origin"`

	// ConfigMap or Secret
	Kind string `json:"kind"`

	// Object describes how the related resource can be found on the origin side
	// and where it is to supposed to be created on the destination side.
	Object RelatedResourceObject `json:"object"`

	// Mutation configures optional transformation rules for the related resource.
	// Status mutations are only performed when the related resource originates in kcp.
	Mutation *ResourceMutationSpec `json:"mutation,omitempty"`
}

// RelatedResourceSource configures how the related resource can be found on the origin side
// and where it is to supposed to be created on the destination side.
type RelatedResourceObject struct {
	RelatedResourceObjectSpec `json:",inline"`

	// Namespace configures in what namespace the related object resides in. If
	// not specified, the same namespace as the main object is assumed. If the
	// main object is cluster-scoped, this field is required and an error will be
	// raised during syncing if the field is not specified.
	Namespace *RelatedResourceObjectSpec `json:"namespace,omitempty"`
}

// RelatedResourceObjectSpec configures different ways an object can be located.
// All fields are mutually exclusive.
type RelatedResourceObjectSpec struct {
	// Selector is a label selector that is useful if no reference is in the
	// main resource (i.e. if the related object links back to its parent, instead
	// of the parent pointing to the related object).
	Selector *RelatedResourceObjectSelector `json:"selector,omitempty"`
	// Reference points to a field inside the main object. This reference is
	// evaluated on both source and destination sides to find the related object.
	Reference *RelatedResourceObjectReference `json:"reference,omitempty"`
	// Template is a Go templated string that can make use of variables to
	// construct the resulting string.
	Template *TemplateExpression `json:"template,omitempty"`
}

// RelatedResourceObjectReference describes a path expression that is evaluated inside
// a JSON-marshalled Kubernetes object, yielding a string when evaluated.
type RelatedResourceObjectReference struct {
	// Path is a simplified JSONPath expression like "metadata.name". A reference
	// must always select at least _something_ in the object, even if the value
	// is discarded by the regular expression.
	Path string `json:"path"`
	// Regex is a Go regular expression that is optionally applied to the selected
	// value from the path.
	Regex *RegularExpression `json:"regex,omitempty"`
}

// RelatedResourceSelector is a dedicated struct in case we need additional options
// for evaluating the label selector.

// RelatedResourceObjectSelector describes how to locate a related object based on
// labels. This is useful if the main resource has no and cannot construct a
// reference to the related object because its name/namespace might be randomized.
type RelatedResourceObjectSelector struct {
	metav1.LabelSelector `json:",inline"`

	Rewrite RelatedResourceSelectorRewrite `json:"rewrite"`
}

type RelatedResourceSelectorRewrite struct {
	// Regex is a Go regular expression that is optionally applied to the selected
	// value from the path.
	Regex    *RegularExpression  `json:"regex,omitempty"`
	Template *TemplateExpression `json:"template,omitempty"`
}

// RegularExpression models a Go regular expression string replacement. See
// https://pkg.go.dev/regexp/syntax for more information on the syntax.
type RegularExpression struct {
	// Pattern can be left empty to simply replace the entire value with the
	// replacement.
	Pattern string `json:"pattern,omitempty"`
	// Replacement is the string that the matched pattern is replaced with. It
	// can contain references to groups in the pattern by using \N.
	Replacement string `json:"replacement,omitempty"`
}

// TemplateExpression is a Go templated string that can make use of variables to
// construct the resulting string.
type TemplateExpression struct {
	Template string `json:"template,omitempty"`
}

// SourceResourceDescriptor and ResourceProjection are very similar, but as we do not
// want to burden service clusters with validation webhooks, it's easier to split them
// into 2 structs here and rely on the schema for validation.

// SourceResourceDescriptor uniquely describes a resource type in the cluster.
type SourceResourceDescriptor struct {
	// The API group of a resource, for example "storage.initroid.com".
	APIGroup string `json:"apiGroup"`
	// The API version, for example "v1beta1".
	Version string `json:"version"`
	// The resource Kind, for example "Database".
	Kind string `json:"kind"`
}

// ResourceScope is an enum defining the different scopes available to a custom resource.
// This ENUM matches apiextensionsv1.ResourceScope, but was copied here to avoid a costly
// dependency and since the ENUM will unlikely be extended/changed in future Kubernetes
// releases.
type ResourceScope string

const (
	ClusterScoped   ResourceScope = "Cluster"
	NamespaceScoped ResourceScope = "Namespaced"
)

// ResourceProjection describes how the source GVK should be modified before it's published in kcp.
type ResourceProjection struct {
	// The API group, for example "myservice.example.com".
	Group string `json:"group,omitempty"`
	// The API version, for example "v1beta1".
	Version string `json:"version,omitempty"`
	// Whether or not the resource is namespaced.
	// +kubebuilder:validation:Enum=Cluster;Namespaced
	Scope ResourceScope `json:"scope,omitempty"`
	// The resource Kind, for example "Database". Setting this field will also overwrite
	// the singular name by lowercasing the resource kind. In addition, if this is set,
	// the plural name will also be updated by taking the lowercased kind name and appending
	// an "s". If this would yield an undesirable name, use the plural field to explicitly
	// give the plural name.
	Kind string `json:"kind,omitempty"`
	// When overwriting the Kind, it can be necessary to also override the plural name in
	// case of more complex pluralization rules.
	Plural string `json:"plural,omitempty"`
	// ShortNames can be used to overwrite the original short names for a resource, usually
	// when the Kind is remapped, new short names are also in order. Set this to an empty
	// list to remove all short names.
	// +optional
	ShortNames []string `json:"shortNames"` // not omitempty because we need to distinguish between [] and nil
	// Categories can be used to overwrite the original categories a resource was in. Set
	// this to an empty list to remove all categories.
	// +optional
	Categories []string `json:"categories"` // not omitempty because we need to distinguish between [] and nil
}

// ResourceFilter can be used to limit what resources should be included in an operation.
type ResourceFilter struct {
	// When given, the namespace filter will be applied to a resource's namespace.
	Namespace *metav1.LabelSelector `json:"namespace,omitempty"`
	// When given, the resource filter will be applied to a resource itself.
	Resource *metav1.LabelSelector `json:"resource,omitempty"`
}

// PublishedResourceStatus stores status information about a published resource.
type PublishedResourceStatus struct {
	ResourceSchemaName string `json:"resourceSchemaName,omitempty"`
}

// +kubebuilder:object:root=true

// PublishedResourceList contains a list of PublishedResources.
type PublishedResourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PublishedResource `json:"items"`
}
