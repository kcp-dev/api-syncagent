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

The renaming is configured in `spec.naming`. In there, renaming patterns are configured, where
pre-defined placeholders can be used, for example `foo-$placeholder`. The following placeholders
are available:

* `$remoteClusterName` – the workspace's cluster name (e.g. "1084s8ceexsehjm2")
* `$remoteNamespace` – the original namespace used by the consumer inside the workspace
* `$remoteNamespaceHash` – first 20 hex characters of the SHA-1 hash of `$remoteNamespace`
* `$remoteName` – the original name of the object inside the workspace (rarely used to construct
  local namespace names)
* `$remoteNameHash` – first 20 hex characters of the SHA-1 hash of `$remoteName`

If nothing is configured, the default ensures that no collisions will happen: Each workspace in
kcp will create a namespace on the local cluster, with a combination of namespace and name hashes
used for the actual resource names.

```yaml
apiVersion: syncagent.kcp.io/v1alpha1
kind: PublishedResource
metadata:
  name: publish-certmanager-certs # name can be freely chosen
spec:
  resource: ...
  naming:
    # This is the implicit default configuration.
    namespace: "$remoteClusterName"
    name: "cert-$remoteNamespaceHash-$remoteNameHash"
```

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
  template: "{{ .LocalObject.ObjectMeta.Namespace }}"
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

References can only ever select 1 related object. Their upside is that they are simple to understand
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
    name: "$remoteClusterName-$remoteNamespaceHash-$remoteNameHash"

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
        # Object can use either reference, labelSelector or expressions. In this
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
how crazy the underlying operators on the service clusters are.

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
    name: "$remoteClusterName-$remoteNamespaceHash-$remoteNameHash"

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

          # You also need to provide rules on how objects found by this selector
          # should be named on the destination side of the sync.
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

#### Templates

The third option to configure how to find/create related objects are templates. These are simple
Go template strings (like `{% raw %}{{ .Variable }}{% endraw %}`) that allow to easily configure static values with a
sprinkling of dynamic values.

This feature has not been fully implemented yet.

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
    name: "$remoteClusterName-$remoteNamespaceHash-$remoteNameHash"

  related:
    - origin: service # service or kcp
      kind: Secret # for now, only "Secret" and "ConfigMap" are supported;
                   # there is no GVK projection for related resources

      # configure where in the parent object we can find
      # the name/namespace of the related resource (the child)
      reference:
        name:
          # This path is evaluated in both the local and remote objects, to figure out
          # the local and remote names for the related object. This saves us from having
          # to remember mutated fields before their mutation (similar to the last-known
          # annotation).
          path: spec.secretName
        # namespace part is optional; if not configured,
        # Sync Agent assumes the same namespace as the owning resource
        # namespace:
        #   path: spec.secretName
        #   regex:
        #     pattern: '...'
        #     replacement: '...'
```

## Technical Details

The following sections go into more details of the behind the scenes magic.

### Synchronization

Even though the whole configuration is written from the standpoint of the service owner, the actual
synchronization logic considers the kcp side as the canonical source of truth. The Sync Agent
continuously tries to make the local objects look like the ones in kcp, while pushing status updates
back into kcp (if the given `PublishedResource` (i.e. CRD) has a `status` subresource enabled).

### Local <-> Remote Connection

The Sync Agent tries to keep sync-related metadata on the service cluster, away from the consumers.
This is both to prevent vandalism and to hide implementation details.

To ensure stability against future changes, once the Sync Agent has determined how a local object
should be named, it will remember this decision in the object's metadata. This is so that on future
reconciliations, the (potentially costly, but probably not) renaming logic does not need to be
applied again. This allows the Sync Agent to change defaults and also allows the service owner to make
changes to the naming rules without breaking existing objects.

Since we do not want to store metadata on the kcp side, we instead rely on label selectors on
the local objects. Each object on the service cluster has a label for the remote cluster name,
namespace and object name, and when trying to find the matching local object, the Sync Agent simply
does a label-based search.

There is currently no sync-related metadata available on source objects (in kcp workspaces), as this
would either be annotations (untyped strings...) or require schema changes to allow additional
fields in basically random CRDs.

Note that fields like `generation` or `resourceVersion` are not relevant for any of the sync logic.

### Reconcile Loop

The sync loop can be divided into 5 parts:

1. find the local object
2. handle deletion
3. ensure the destination object exists
4. ensure the destination object's content matches the source object
5. synchronize related resources the same way (repeat 1-4 for each related resource)

#### Phase 1: Find the Local Object

For this, as mentioned in the connection chapter above, the Sync Agent tries to follow label selectors
on the service cluster. This helps prevent cluttering with consumer workspaces with sync metadata.
If no object is found to match the labels, that's fine and the loop will continue with phase 2,
in which a possible Conflict error (if labels broke) is handled gracefully.

The remote object in the workspace becomes the `source object` and its local equivalent on the
service cluster is called the `destination object`.

#### Phase 2: Handle Deletion

A finalizer is used in the kcp workspaces to prevent orphans in the service cluster side. This
is the only real evidence in the kcp side that the Sync Agent is even doing things. When a remote
(source) object is deleted, the corresponding local object is deleted as well. Once the local object
is gone, the finalizer is removed from the source object.

#### Phase 3: Ensure Object Existence

We have a source object and now need to create the destination. This chart shows what's happening.

```mermaid
graph TB
    A(source object):::state --> B([cleanup if in deletion]):::step
    B --> C([ensure finalizer on source object]):::step
    C --> D{exists local object?}

    D -- yes --> I("continue with next phase…"):::state
    D -- no --> E([apply projection]):::step

    subgraph "ensure dest object exists"
    E --> G([ensure resulting namespace exists]):::step
    G --> H([create local object]):::step
    H --> H_err{Errors?}
    H_err -- Conflict --> J([attempt to adopt existing object]):::step
    end

    H_err -- success --> I
    J --> I

    classDef step color:#77F
    classDef state color:#F77
```

After we followed through with these steps, both the source and destination objects exists and we
can continue with phase 4.

Resource adoption happens when creation of the initial local object fails. This can happen when labels
get mangled. If such a conflict happens, the Sync Agent will "adopt" the existing local object by
adding / fixing the labels on it, so that for the next reconciliation it will be found and updated.

#### Phase 4: Content Synchronization

Content synchronization is rather simple, really.

First the source "spec" is used to patch the local object. Note that this step is called "spec", but
should actually be called "all top-level elements besides `apiVersion`, `kind`, `status` and
`metadata`, but still including some labels and annotations"; so if you were to publish RBAC objects,
the syncer would include `roleRef` field, for example).

To allow proper patch generation, the last known state is kept on the local object, similar to how
`kubectl` creates an annotation for it. This is required for the Sync Agent to properly detect changes
made by mutation webhooks on the service cluster.

If the published resource (CRD) has a `status` subresource enabled (not just a `status` field in its
scheme, it must be a real subresource), then the Sync Agent will copy the status from the local object
back up to the remote (source) object.

#### Phase 5: Sync Related Resources

The same logic for synchronizing the main published resource applies to their related resources as
well. The only difference is that the source side can be either remote (workspace) or local
(service cluster).

Since the Sync Agent tries its best to keep sync-related data out of kcp workspaces, the last known
state for related resources is _not_ kept together with the destination object in the kcp workspaces.
Instead all known states (from the main object and all related resources) is kept in a single Secret
on the service cluster side.
