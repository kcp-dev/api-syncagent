# Publishing Resources

The guide describes the process of making a resource (usually defined by a CustomResourceDefinition)
of one Kubernetes cluster (the "service cluster" or "local cluster") available for use in kcp. This
involves setting up an `APIExport` and then installing the Sync Agent and defining
`PublishedResources` in the local cluster.

All of the documentation and API types are worded and named from the perspective of a service owner,
the person(s) who own a service and want to make it available to consumers in kcp.

## High-level Overview

A "service" comprises a set of resources within a single Kubernetes API group. It doesn't need to be
_all_ of the resources in that group, service owners are free and encouraged to only make a subset
of resources (i.e. a subset of CRDs) available for use in kcp.

For each of the CRDs on the service cluster that should be published, the service owner creates a
`PublishedResource` object, which will contain both which CRD to publish, as well as numerous other
important settings that influence the behaviour around handling the CRD.

When publishing a resource (CRD), service owners can choose to restrict it to a subset of available
versions and even change API group, versions and names in transit (for example published a v1 from
the service cluster as v1beta1 within kcp). This process of changing the identity of a CRD is called
"projection" in the agent.

All published resources together form the APIExport. When a service is enabled in a workspace
(i.e. it is bound to it), users can manage objects for the projected resources described by the
published resources. These objects will be synced from the workspace onto the service cluster,
where they are meant to be processed in whatever way the service owners desire. Any possible
status information (in the `status` subresource) will in turn be synced back up into the workspace
where the user can inspect it.

Additionally, a published resource can describe additional so-called "related resources". These
usually originate on the service cluster and could be for example connection detail secrets created
by Crossplane, but could also originate in the user workspace and just be additional, auxiliary
resources that need to be synced down to the service cluster.

### `PublishedResource`

In its simplest form (which is rarely practical) a `PublishedResource` looks like this:

```yaml
apiVersion: syncagent.kcp.io/v1alpha1
kind: PublishedResource
metadata:
  name: publish-certmanager-certs # name can be freely chosen
spec:
  resource:
    kind: Certificate
    apiGroup: cert-manager.io
    versions: [v1]
```

However, you will most likely apply more configuration and use features described below.

You always have to select at least one version, and all selected versions must be marked as `served`
on the service cluster. If the storage version is selected to be published, it stays the storage
version in kcp. If no storage version is selected, the latest selected version becomes the storage
version.

For more information refer to the [API lifecycle](api-lifecycle.md).

### Filtering

The Sync Agent can be instructed to only work on a subset of resources in kcp. This can be restricted
by namespace and/or label selector.

```yaml
apiVersion: syncagent.kcp.io/v1alpha1
kind: PublishedResource
metadata:
  name: publish-certmanager-certs # name can be freely chosen
spec:
  resource: ...
  filter:
    namespace: my-app
    resource:
      matchLabels:
        foo: bar
```

The configuration above would mean the agent only synchronizes objects from `my-app` namespaces (in
each of the kcp workspaces) that also have a `foo=bar` label on them.

### Schema

**Warning:** The actual CRD schema is always copied verbatim. All projections, mutations etc. have
to take into account that the resource contents must be expressible without changes to the schema,
so you cannot define entirely new fields in an object that are not defined by the original CRD.

### Projection

For stronger separation of concerns and to enable whitelabelling of services, the type meta for CRDs
can be projected, i.e. changed between the local service cluster and kcp. You could for example
rename `Certificate` from cert-manager to `Sertifikat` inside kcp.

Note that the API group of all published resources is always changed to the one defined in the
APIExport object (meaning 1 Sync Agent serves all the selected published resources under the same
API group). That is why changing the API group cannot be configured in the projection.

Besides renaming the Kind and Version, dependent fields like Plural, ShortNames and Categories
can be adjusted to fit the desired naming scheme in kcp. The Plural name is computed
automatically, but can be overridden. ShortNames and Categories are copied unless overwritten in the
`PublishedResource`.

It is also possible to change the scope of resources, i.e. turning a namespaced resource into a
cluster-wide. This should be used carefully and might require extensive mutations.

```yaml
apiVersion: syncagent.kcp.io/v1alpha1
kind: PublishedResource
metadata:
  name: publish-certmanager-certs # name can be freely chosen
spec:
  resource: ...
  projection:
    # all of these options are optional
    kind: Sertifikat
    plural: Sertifikater
    shortNames: [serts]
    versions:
      # old version => new version;
      # this must not map multiple versions to the same new version.
      v1: v1beta1
    # categories: [management]
    # scope: Namespaced # change only when you know what you're doing
```

Consumers (end users) in kcp would then ultimately see projected names only. Note that GVK
projection applies only to the synced object itself and has no effect on the contents of these
objects. To change the contents, use external solutions like Crossplane to transform objects.
To change the contents, use *Mutations*.

### (Re-)Naming

Since the Sync Agent ingests resources from many different Kubernetes clusters (workspaces) and combines
them onto a single cluster, resources have to be renamed to prevent collisions and also follow the
conventions of whatever tooling ultimately processes the resources locally.

This snippet shows the implicit default configuration:

```yaml
apiVersion: syncagent.kcp.io/v1alpha1
kind: PublishedResource
metadata:
  name: publish-certmanager-certs # name can be freely chosen
spec:
  resource: ...
  naming:
    namespace: '{{ .ClusterName }}'
    name: '{{ .Object.metadata.namespace | sha3short }}-{{ .Object.metadata.name | sha3short }}'
```

This configuration ensures that no collisions will happen: Each workspace in
kcp will create a namespace on the local cluster, with a combination of namespace and name hashes
used for the actual resource names.

You can override the name or namespaces rules, or both. It is your responsibility to ensure no
naming conflicts can happen on the service cluster, as the agent cannot determine this automatically.

#### Templating

In `spec.naming`, [Go template expressions](https://pkg.go.dev/text/template) are used to construct
the desired name of the object's copy. In the templates used here, the following data is injected by
the agent:

```go
type localObjectNamingContext struct {
	// Object is the full remote object found in a kcp workspace.
	Object map[string]any
	// ClusterName is the internal cluster identifier (e.g. "34hg2j4gh24jdfgf").
	ClusterName logicalcluster.Name
	// ClusterPath is the workspace path (e.g. "root:customer:projectx").
	ClusterPath logicalcluster.Path
}
```

For more details about the templating, see the [Templating](templating.md) documentation.

#### Legacy Naming Rules

Go templates for naming rules have been added in v0.3 of the agent. Previous versions used a
`$variable`-based approach, which since has been deprecated. You are encouraged to migrate your
PublishedResources over to Go templates.

The following table shows the available variables and their modern replacements:

| Deprecated Variable    | Go Template                                     | Description |
| ---------------------- | ----------------------------------------------- | ----------- |
| `$remoteClusterName`   | `{{ .ClusterName }}`                            | the workspace's cluster name (e.g. "1084s8ceexsehjm2") |
| `$remoteNamespace`     | `{{ .Object.metadata.namespace }}`              | the original namespace used by the consumer inside the workspace |
| `$remoteNamespaceHash` | `{{ .Object.metadata.namespace \| shortHash }}` | first 20 hex characters of the SHA-1 hash of `$remoteNamespace` |
| `$remoteName`          | `{{ .Object.metadata.name }}`                   | the original name of the object inside the workspace (rarely used to construct local namespace names) |
| `$remoteNameHash`      | `{{ .Object.metadata.name \| shortHash }}`      | first 20 hex characters of the SHA-1 hash of `$remoteName` |

Note that `ClusterPath` was never available in `$variable` form.

Note also that the `shortHash` function exists only for backwards compatibility with the old
`$variable` syntax. The new default is to use SHA-3 instead (via the `sha3short` function). When
migrating from the old syntax, you can use the `shortHash` function to ensure new objects are placed
in the old locations. New setups should however use explicitly named functions for hashing, like
`sha3sum` or `sha3short`. `sha3short` takes an optional length parameter that defaults to 20.

### Mutation

Besides projecting the type meta, changes to object contents are also nearly always required.
These can be configured in a number of way in the `PublishedResource`.

Configuration happens `spec.mutation` and there are two fields:

* `spec` contains the mutation rules when syncing the desired state (often in `spec`, but can also
  be other top-level fields) from the remote side to the local side. Use this to apply defaulting,
  normalising, and enforcing rules.
* `status` contains the mutation rules when syncing the `status` subresource back from the local
  cluster up into kcp. Use this to normalize names and values (e.g. if you rewrote
  `.spec.secretName` from `"foo"` to `"dfkbssbfh"`, make sure the status does not "leak" this name
  by accident).

Mutation is always done as a series of steps. Each step does exactly one thing and only one must
be configured per step.

```yaml
apiVersion: syncagent.kcp.io/v1alpha1
kind: PublishedResource
metadata:
  name: publish-certmanager-certs # name can be freely chosen
spec:
  resource: ...
  mutation:
    spec:
      # choose one per step
      - regex: ...
        template: ...
        delete: ...
```

#### Regex

```yaml
regex:
  path: "json.path[expression]"
  pattern: "(.+)"
  replacement: "foo-\\1"
```

This mutation applies a regular expression to a single value inside the document. JSON path is the
usual path, without a leading dot.

#### Template

{% raw %}
```yaml
template:
  path: "json.path[expression]"
  template: "{{ .LocalObject.metadata.namespace }}"
```
{% endraw %}

This mutation applies a Go template expression to a single value inside the document. JSON path is the
usual path, without a leading dot.

#### Delete

```yaml
delete:
  path: "json.path[expression]"
```

This mutation simply removes the value at the given path from the document. JSON path is the
usual path, without a leading dot.

### Related Resources

The processing of resources on the service cluster often leads to additional resources being
created, like a `Secret` for each cert-manager `Certificate` or a connection detail secret created
by Crossplane. These need to be made available to the user in their workspaces.

Likewise it's possible for auxiliary resources having to be created by the user, for example when
the user has to provide credentials.

To handle these cases, a `PublishedResource` can define multiple "related resources". Each related
resource represents usually one, but can be multiple objects to synchronize between user workspace
and service cluster. While the main published resource sync is always workspace->service cluster,
related resources can originate on either side and so either can work as the source of truth.

At the moment, only `ConfigMaps` and `Secrets` are allowed related resource kinds.

For each related resource, the Sync Agent needs to be told how to find the object on the origin side
and where to create it on the destination side. There are multiple options that you can choose from.

By default all related objects live in the same namespace as the primary object (their owner/parent).
If the primary object is cluster scoped, admins must configure additional rules to specify what
namespace the ConfigMap/Secret shall be read from and created in.

Related resources are always optional. Even if references (see below) are used and their path
expression points to a non-existing field in the primary object (e.g. `spec.secretName` is configured,
but that field does not exist in Certificate object), this will simply be treated as "not _yet_
existing" and not create an error.

#### References

A reference is a JSONPath-like expression that are evaluated on both sides of the synchronization.
You configure a single path expression (like `spec.secretName`) and the sync agent will evaluate it
in the original primary object (in kcp) and again in the copied primary object (on the service
cluster). Since the primary object has already been mutated, the `spec.secretName` is already
rewritten/adjusted to work on the service cluster (for example it was changed from `my-secret` to
`jk23h4wz47329rz2r72r92-secret` on the service cluster side). By doing it this way, admins only have
to think about mutations and rewrites once (when configuring the primary object in the
PublishedResource) and the path will yield 2 ready to use values (`my-secret` and the computed value).

The value selected by the path expression must be a string (or number, but it will be coalesced into
a string) and can then be further adjusted by applying a regular expression to it.

References can only ever select one related object. Their upside is that they are simple to understand
and easy to use, but require a "link" in the primary object that would point to the related object.

Here's an example on how to use references to locate the related object.

```yaml
apiVersion: syncagent.kcp.io/v1alpha1
kind: PublishedResource
metadata:
  name: publish-certmanager-certs
spec:
  resource:
    kind: Certificate
    apiGroup: cert-manager.io
    versions: [v1]

  naming:
    # this is where our CA and Issuer live in this example
    namespace: kube-system
    # need to adjust it to prevent collions (normally clustername is the namespace)
    name: "{{ .ClusterName }}-{{ .Object.metadata.namespace | sha3short }}-{{ .Object.metadata.name | sha3short }}"

  related:
    - # unique name for this related resource. The name must be unique within
      # one PublishedResource and is the key by which consumers (end users)
      # can identify and consume the related resource. Common names are
      # "connection-details" or "credentials".
      identifier: tls-secret

      # "service" or "kcp"
      origin: service

      # for now, only "Secret" and "ConfigMap" are supported;
      # there is no GVK projection for related resources
      kind: Secret

      # configure where in the parent object we can find the child object
      object:
        # Object can use either reference, labelSelector or template. In this
        # example we use references.
        reference:
          # This path is evaluated in both the local and remote objects, to figure out
          # the local and remote names for the related object. This saves us from having
          # to remember mutated fields before their mutation (similar to the last-known
          # annotation).
          path: spec.secretName

        # namespace part is optional; if not configured,
        # Sync Agent assumes the same namespace as the owning resource
        # namespace:
        #   reference:
        #     path: spec.secretName
        #     regex:
        #       pattern: '...'
        #       replacement: '...'
```

#### Templates

Similar to references, [Go templates](https://pkg.go.dev/text/template) can also be used to determine
the names of related objects on both sides of the sync. In fact, templates can be thought of as more
powerful references since they allow for minimal logic to be embedded in them. Templates also do not
necessarily have to select a value from the object (like a reference does), but can use any kind of
logic to determine the names.

Like references, templates can also only be used to select a single object per related resource.

A template gets the following data injected into it:

```go
type localObjectNamingContext struct {
	// Side is set to either one of the possible origin values to indicate for
	// which cluster the template is currently being evaluated for.
	Side syncagentv1alpha1.RelatedResourceOrigin
	// Object is the primary object belonging to the related object. Since related
	// object templates are evaluated twice (once for the origin side and once
	// for the destination side), object is the primary object on the side the
	// template is evaluated for.
	Object map[string]any
	// ClusterName is the internal cluster identifier (e.g. "34hg2j4gh24jdfgf")
	// of the kcp workspace that the synchronization is currently processing. This
	// value is set for both evaluations, regardless of side.
	ClusterName logicalcluster.Name
	// ClusterPath is the workspace path (e.g. "root:customer:projectx"). This
	// value is set for both evaluations, regardless of side.
	ClusterPath logicalcluster.Path
}
```

In the simplest form, a template can replace a reference:

* reference: `.spec.secretName`
* Go template: `{{ .Object.spec.secretName }}`

Just like with references, the configured template is evaluated twice, once for each side of the
synchronization. You can use the `Side` variable to allow for fully customized names on each side:

```yaml
spec:
  ...
  related:
    - identifier: tls-secret
      # ..omitting other fields..
      object:
        template:
          template: `{{ if eq .Side "kcp" }}name-in-kcp{{ else }}name-on-service-cluster{{ end }}`
```

See [Templating](templating.md) for more information on how to use templates in PublishedResources.

#### Label Selectors

In some cases, the primary object does not have a link to its child/children objects. In these cases,
a label selector can be used. This allows to configure the labels that any related object must have
to be included.

Notably, this allows for _multiple_ objects that are synced for a single configured related resource.
The sync agent will not prevent misconfigurations, so great care must be taken when configuring
selectors to not accidentally include too many objects.

Additionally, it is assumed that

* Primary objects synced from kcp to a service cluster will be renamed, to prevent naming collisions.
* The renamed objects on the service cluster might contain private, sensitive information that should
  not be leaked into kcp workspaces.
* When there is no explicit name being requested (like by setting `spec.secretName`), it can be
  assumed that the operator on the service cluster that is actually processing the primary object
  will use the primary object's name (at least in parts) to construct the names of related objects,
  for example a Certificate `yaddasupersecretyadda` might automatically get a Secret created named
  `yaddasupersecretyadda-secret`.

Since the name of the related object must not leak into a kcp workspace, admins who configure a
label selector also always have to provide a naming scheme for the copies of the related objects on
the destination side.

Namespaces work the same as with references, i.e. by default the same namespace as the primary object
is assumed. However you can actually also use label selectors to find the origin _namespaces_
dynamically. So you can configure two label selectors, and then agent will first use the namespace
selector to find all applicable namespaces, and then use the other label selector _in each of the
applicable namespaces_ to finally locate the related objects. How useful this is depends a lot on
how peculiar the underlying operators on the service clusters are.

Here is an example on how to use label selectors:

```yaml
apiVersion: syncagent.kcp.io/v1alpha1
kind: PublishedResource
metadata:
  name: publish-certmanager-certs
spec:
  resource:
    kind: Certificate
    apiGroup: cert-manager.io
    versions: [v1]

  naming:
    namespace: kube-system
    name: "{{ .ClusterName }}-{{ .Object.metadata.namespace | sha3short }}-{{ .Object.metadata.name | sha3short }}"

  related:
    - identifier: tls-secrets

      # "service" or "kcp"
      origin: service

      # for now, only "Secret" and "ConfigMap" are supported;
      # there is no GVK projection for related resources
      kind: Secret

      # configure where in the parent object we can find the child object
      object:
        # A selector is a standard Kubernetes label selector, supporting
        # matchLabels and matchExpressions.
        selector:
          matchLabels:
            my-key: my-value
            another: pair
            # Within matchLabels, keys and values are treated as Go templates.
            # In this example, since the Secret originates on the service cluster
            # (see "origin" above), we use LocalObject to determine the value
            # for the selector. In case the object was heavily mutated during the
            # sync, this will give access to the mutated values on the service
            # cluster side.
            '{{ shasum "test" }}': '{{ .LocalObject.spec.username }}'

          # You also need to provide rules on how objects found by this selector
          # should be named on the destination side of the sync. You can choose
          # to define a rewrite rule that keeps the original name from the origin
          # side, but this may leak undesirable internals to the users.
          # Rewrites are either using regular expressions or templated strings,
          # never both.
          # The rewrite config is applied to each individual found object.
          rewrite:
            regex:
              pattern: "foo-(.+)"
              replacement: "bar-\\1"

            # or
{% raw %}
            template:
              template: "{{ .Name }}-foo"
{% endraw %}

        # Like with references, the namespace can (or must) be configured explicitly.
        # You do not need to also use label selectors here, you can mix and match
        # freely.
        # namespace:
        #   reference:
        #     path: metadata.namespace
        #     regex:
        #       pattern: '...'
        #       replacement: '...'
```

There are two possible usages of Go templates when using label selectors. See [Templating](templating.md)
for more information on how to use templates in PublishedResources in general.

##### Selector Templates

Each template rendered as part of a `matchLabels` selector gets the following data injected:

```go
type relatedObjectLabelContext struct {
	// LocalObject is the primary object copy on the local side of the sync
	// (i.e. on the service cluster).
	LocalObject map[string]any
	// RemoteObject is the primary object original, in kcp.
	RemoteObject map[string]any
	// ClusterName is the internal cluster identifier (e.g. "34hg2j4gh24jdfgf")
	// of the kcp workspace that the synchronization is currently processing
	// (where the remote object exists).
	ClusterName logicalcluster.Name
	// ClusterPath is the workspace path (e.g. "root:customer:projectx").
	ClusterPath logicalcluster.Path
}
```

Note that in contrast to the `template` way of selecting objects, the templates here in the label
selector are only evaluated once, on the origin side of the sync. The names of the destination side
are determined using the rewrite mechanism (which might also be a Go template, see next section).

##### Rewrite Rules

Each found related object on the origin side needs to have its own name on the destination side. To
map from the origin to the destination side, regular expressions (see example snippet) or Go
templatess can be used.

If a template is configured, it is evaluated once for every found related object. The template gets
the following data injected into it:

```go
type relatedObjectLabelRewriteContext struct {
	// Value is either the a found namespace name (when a label selector was
	// used to select the source namespaces for related objects) or the name of
	// a found object (when a label selector was used to find objects). In the
	// former case, the template should return the new namespace to use on the
	// destination side, in the latter case it should return the new object name
	// to use on the destination side.
	Value string
	// When a rewrite is used to rewrite object names, RelatedObject is the
	// original related object (found on the origin side). This enables you to
	// ignore the given Value entirely and just select anything from the object
	// itself.
	// RelatedObject is nil when the rewrite is performed for a namespace.
	RelatedObject map[string]any
	// LocalObject is the primary object copy on the local side of the sync
	// (i.e. on the service cluster).
	LocalObject map[string]any
	// RemoteObject is the primary object original, in kcp.
	RemoteObject map[string]any
	// ClusterName is the internal cluster identifier (e.g. "34hg2j4gh24jdfgf")
	// of the kcp workspace that the synchronization is currently processing
	// (where the remote object exists).
	ClusterName logicalcluster.Name
	// ClusterPath is the workspace path (e.g. "root:customer:projectx").
	ClusterPath logicalcluster.Path
}
```

Regarding `Value`: The agent allows to individually configure rules for finding object _names_ and
object _namespaces_. Often the namespace is not configured because the related objects live in the
same namespace as their owning, primary object.

When a label selector is configured to find namespaces, the rewrite template will be evaluated once
for each found namespace. In this case the `.Value` is the name of the found namespace. Remember, the
template's job is to map the found namespace to the new namespace on the destination side of the sync.

Once the namespaces have been determined, the agent will look for matching objects in each namespace
individually. For each namespace it will again follow the configured source, may it be a selector,
template or reference. If again a label selector is used, it will be applied in each namespace and
the configured rewrite rule will be evaluated once per found object. In this case, `.Value` is the
name of found object.

## Examples

### Provide Certificates

This combination of `APIExport` and `PublishedResource` make cert-manager certificates available in
kcp. The `APIExport` needs to be created in a workspace, most likely in an organization workspace.
The `PublishedResource` is created wherever the Sync Agent and cert-manager are running.

```yaml
apiVersion: apis.kcp.io/v1alpha1
kind: APIExport
metadata:
  name: certificates.example.corp
spec: {}
```

```yaml
apiVersion: syncagent.kcp.io/v1alpha1
kind: PublishedResource
metadata:
  name: publish-certmanager-certs
spec:
  resource:
    kind: Certificate
    apiGroup: cert-manager.io
    versions: [v1]

  naming:
    # this is where our CA and Issuer live in this example
    namespace: kube-system
    # need to adjust it to prevent collions (normally clustername is the namespace)
    name: "{{ .ClusterName }}-{{ .Object.metadata.namespace | sha3short }}-{{ .Object.metadata.name | sha3short }}"

  related:
    - origin: service # service or kcp
      kind: Secret    # for now, only "Secret" and "ConfigMap" are supported;
                      # there is no GVK projection for related resources

      # configure where in the parent object we can find
      # the name/namespace of the related resource (the child)
      object:
        # This template is evaluated in both the local and remote objects, to figure out
        # the local and remote names for the related object. This saves us from having
        # to remember mutated fields before their mutation (similar to the last-known
        # annotation).
        template:
          template: '{{ .Object.spec.secretName }}'
```
