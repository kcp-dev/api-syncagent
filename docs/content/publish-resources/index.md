# Publishing Resources

The guide describes the process of making a resource (usually defined by a CustomResourceDefinition)
of one Kubernetes cluster (the "service cluster" or "local cluster") available for use in kcp. This
involves setting up an `APIExport`, potentially an `APIExportEndpointSlice` and then installing the
Sync Agent and defining `PublishedResources` in the local cluster.

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

All published resources together form the `APIExport`. When a service is enabled in a workspace
(i.e. it is bound to it), users can manage objects for the projected resources described by the
published resources. These objects will be synced from the workspace onto the service cluster,
where they are meant to be processed in whatever way the service owners desire. Any possible
status information (in the `status` subresource) will in turn be synced back up into the workspace
where the user can inspect it.

Additionally, a published resource can describe additional so-called "related resources". These
usually originate on the service cluster and could be for example connection detail secrets created
by Crossplane, but could also originate in the user workspace and just be additional, auxiliary
resources that need to be synced down to the service cluster.

## `PublishedResource`

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

{% raw %}
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
{% endraw %}

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

{% raw %}
| Deprecated Variable    | Go Template                                     | Description |
| ---------------------- | ----------------------------------------------- | ----------- |
| `$remoteClusterName`   | `{{ .ClusterName }}`                            | the workspace's cluster name (e.g. "1084s8ceexsehjm2") |
| `$remoteNamespace`     | `{{ .Object.metadata.namespace }}`              | the original namespace used by the consumer inside the workspace |
| `$remoteNamespaceHash` | `{{ .Object.metadata.namespace \| shortHash }}` | first 20 hex characters of the SHA-1 hash of `$remoteNamespace` |
| `$remoteName`          | `{{ .Object.metadata.name }}`                   | the original name of the object inside the workspace (rarely used to construct local namespace names) |
| `$remoteNameHash`      | `{{ .Object.metadata.name \| shortHash }}`      | first 20 hex characters of the SHA-1 hash of `$remoteName` |
{% endraw %}

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

More information is available in the [related resources guide](./related-resources.md).

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

{% raw %}
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
    # need to adjust it to prevent collisions (normally clustername is the namespace)
    name: "{{ .ClusterName }}-{{ .Object.metadata.namespace | sha3short }}-{{ .Object.metadata.name | sha3short }}"

  related:
    - identifier: tls-secrets
      origin: service # service or kcp
      group: ""
      version: v1
      resource: secrets

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
{% endraw %}
