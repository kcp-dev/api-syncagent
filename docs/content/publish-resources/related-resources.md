# Related Resources

This document describes the configuration of related resources. These are additional resources,
configured inside a `PublishedResource`, that are synced along a primary object.

## Background

The processing of resources on the service cluster often leads to additional resources being
created, like a `Secret` for each cert-manager `Certificate` or a connection detail secret created
by Crossplane. These need to be made available to the user in their workspaces.

Likewise it's possible for auxiliary resources having to be created by the user, for example when
the user has to provide credentials.

To handle these cases, a `PublishedResource` can define multiple "related resources". Each related
resource represents usually one, but can be multiple objects to synchronize between user workspace
and service cluster. While the main published resource sync is always workspace->service cluster,
related resources can originate on either side and so either can work as the source of truth.

Related resources can be

* core resources available in basically any Kubernetes cluster, like `ConfigMaps` or `Secrets`,
* resources contained in the same `APIExport` that is managed by the syncagent or
* resources contained in other `APIExports` not managed by the syncagent (in this case, the
  identity hash of the target `APIExport` must be configured)

For each related resource, the Sync Agent needs to be told how to find the object on the origin side
and where to create it on the destination side. There are multiple options that you can choose from.

By default all related objects live in the same namespace as the primary object (their owner/parent).
If the primary object is cluster scoped, a namespace must be configured to specify which namespaces
the resource shall be read from and written to.

Related resources are always optional. Even if references (see below) are used and their path
expression points to a non-existing field in the primary object (e.g. `spec.secretName` is configured,
but that field does not exist in a Certificate object), this will simply be treated as "not _yet_
existing" and not create an error.

## Permission Claims

Regardless of resource selection method chosen, each related resource will automatically become its
own permission claim in the `APIExport` and will need to be accepted by each consumer. The syncagent
assumes it has permissions and will report an error if it cannot access a related resources.

## Resource Selection

This section describes how the Kubernetes resource (like `secrets`) used as a related resource can
be configured. In all scenarios, the `group`, `version` and `resource` (GVR) will have to be set
correctly, maybe even the `identityHash`.

The configured GVR will be used on both sides of the sync, i.e. in kcp and on the service cluster,
unless additional projection rules (like are possible for the primary published resource) are
configured. Using these projections, one could for example turn a `ConfigMap` into a `Secret` (as
an academic example).

### Core Resources

Core resources are built-in to every Kubernetes cluster and exist for example in the API group
`core/v1`, but RBAC could technically also be considered a core resource in `rbac.authorization.k8s.io`.
"Core" here really only means that the resource exists in every kcp workspace and on the service
cluster without additional CRDs/APIExports.

For these built-in resources, all you need to do is set their GVR:

```yaml
apiVersion: syncagent.kcp.io/v1alpha1
kind: PublishedResource
metadata:
  name: publish-certmanager-certs
spec:
  # ...

  related:
    - # ...

      # sync regular ol' Kubernetes Secrets
      group: "" # for core/v1, leave this empty, else put the API group
      version: v1
      resource: secrets

      # alternatively, for example
      # group: "rbac.authorization.k8s.io"
      # version: v1
      # resource: roles
```

In older version of syncagent, an alternative syntax was supported, where only the `kind` was
specified since only `ConfigMap` and `Secret` were allowed. This configuration (the `kind` field in
its entirety) is deprecated and will be removed in a future syncagent release.

### Owned Resources

"Owned" refers to all resources that are defined in the syncagent-managed `APIExport`. For configuring
related resources, it does not matter where the resource in the `APIExport` came from (whether the
agent added it based on a `PublishedResource` or whether it already existed in the `APIExport`).

!!! note
    If you use a `PublishedResource` to make a Kubernetes resource available in the `APIExport`, and
    then intend to use this resource as a related resource, you must ensure that you set
    `spec.synchronization.enabled=false` or else the syncagent could end up in a loop, synchronizing
    the same objects back and forth. See further down for more information.

Since your `APIExport` "owns" the related resource, configuration is identical to how core resources
are configured: Simply provide the GVR:

```yaml
apiVersion: syncagent.kcp.io/v1alpha1
kind: PublishedResource
metadata:
  name: publish-certmanager-certs
spec:
  # ...

  related:
    - # ...

      # assume that this PublishedResource belongs to the APIExport that provides
      # a resourceSchema for UserDatabases in myapi.example.com/v1
      group: "myapi.example.com"
      version: v1beta1
      resource: userdatabases
```

The configured resource here will be used on both sides of the sync, as with any other related
resource type. If the primary resource in the `PublishedResource` makes use of projection to change
the API groups or versions between kcp and the service cluster, you will most likely also want to
project the GVR of related resources.

Also take note that if you use projection for related resources, the original GVR of the related
resource is used on the origin side, and the projected GVR value is used on the destination side. So
depending on your `origin` value, you either to project or "undo" the projection.

Take this example, where we use two `PublishedResources` to make two CRDs available in kcp, then use
one of them as a related resource of the other.

```yaml
# This PublishedResource is only meant as a helper to "ship" our CRD to kcp and
# project its GVR into a nice-looking, public API group. No UserDatabase object
# should ever be synced on its own, they all always belong to a Queue object.

apiVersion: syncagent.kcp.io/v1alpha1
kind: PublishedResource
metadata:
  name: publish-userdatabases
spec:
  # describe the CRD on the service cluster
  resource:
    kind: UserDatabase
    apiGroup: poc.corporate.example.dev
    versions: [v1alpha1]

  # make it look more professional in kcp
  projection:
    apiGroup: api.initech.example.com
    versions:
      v1alpha1: v2

  # IMPORTANT: Disable synchronizing the resource, otherwise objects that are
  # meant to be related to Queue objects would suddenly be treated as standalone
  # objects meant to be synced on their own.
  synchronization:
    enabled: false

---

# This PR is our focus, users are meant to create Queue objects for which we
# then on the provide side provision, among other things, a UserDatabase object
# that is then meant to be synced back to the user.

apiVersion: syncagent.kcp.io/v1alpha1
kind: PublishedResource
metadata:
  name: publish-queues
spec:
  # describe the CRD on the service cluster
  resource:
    kind: Queue
    apiGroup: poc.corporate.example.dev
    versions: [v1alpha1]

  # make it look more professional in kcp
  projection:
    apiGroup: api.initech.example.com
    versions:
      v1alpha1: v2

  related:
    - id: userdb
      origin: service

      # Since the UserDatabase originates on the service cluster, we must use
      # the GVR that is present on the service cluster, not the projected one.
      group: poc.corporate.example.dev
      version: v1alpha1
      resource: userdatabases

      # However, since we do use projection, we must make sure the syncagent
      # also projects this GVR properly. So here we provide the same rules as
      # for the primary resource:
      projection:
        group: api.initech.example.com
        version: v2

      # --- OR, if the related resource originates in kcp ---

      # origin: kcp
      #
      # group: api.initech.example.com
      # version: v2
      # resource: userdatabases
      #
      # # revert the projection
      # projection:
      #   group: poc.corporate.example.dev
      #   version: v1alpha1
```

### Foreign Resources

Any non-built-in and non-owned resource is considered a "foreign" resource and are provided by
other `APIExports` in kcp. To refer to them as related resources, the `identityHash` of the owning
`APIExport` must be configured as well. This hash can be obtained from an `APIExport`'s
`status.identityHash` field.

It is in this case up to you to make sure the necessary resource is available on the service cluster.
Since the GVR can be projected, you can use a custom name on the service cluster if needed.

```yaml
apiVersion: syncagent.kcp.io/v1alpha1
kind: PublishedResource
metadata:
  name: publish-certmanager-certs
spec:
  # ...

  related:
    - # ...

      group: "othercorp.example.com"
      version: v2beta1
      resource: things
      # hash taken from the other APIExport's status.identityHash field
      identityHash: 423ui5gfr8f237g49238hrfg7wtref132g82zv43u21
```

## Object Selection

Object selection is the configuration to tell the syncagent which Kubernetes objects it should
consider belonging to the primary object. The idea here is that you always tell the relationship
starting from the primary object (like if the primary object had a `secretName` field).

In the synchronization loop, the syncagent will always start with the primary object and then find
the related objects for it. It never, for example, looks at a `Secret` on the service cluster and
has to determine what `Certificate` it belongs to. As of now, the syncagent does not even watch
related resources at all, because it currently cannot efficiently answer exactly that question.
Instead the agent, for any given `Secret`, would have to list _all_ primary objects and for each of
them, resolve all matching related objects and then finally check if the `Secret` is among them.
See [issue #118](https://github.com/kcp-dev/api-syncagent/issues/118) for more information.

### References

A reference is a JSONPath-like expression (more precisely, it follows the [gjson syntax](https://github.com/tidwall/gjson?tab=readme-ov-file#path-syntax))
that are evaluated on both sides of the synchronization. You configure a single path expression
(like `spec.secretName`) and the sync agent will evaluate it in the original primary object (in kcp)
and again in the copied primary object (on the service cluster). Since the primary object has already
been mutated, the `spec.secretName` is already rewritten/adjusted to work on the service cluster
(for example it was changed from `my-secret` to `jk23h4wz47329rz2r72r92-secret` on the service
cluster side). By doing it this way, admins only have to think about mutations and rewrites once
(when configuring the primary object in the PublishedResource) and the path will yield 2 ready to
use values (`my-secret` and the computed value).

References can either return a single scalar (strings or integers that will be auto-converted to a
string) (like in `spec.secretName`) or a list of strings/numbers (like `spec.users.#.name`). A
reference must return the same number of items on both the local and remote object, otherwise the
agent will not be able to map local related names to remote related names correctly.

A regular expression can be configured to be applied to each found value (i.e. if the reference returns
a list of values, the regular expression is applied to each individual value).

Here's an example on how to use references to locate the related object.

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
    - # unique name for this related resource. The name must be unique within
      # one PublishedResource and is the key by which consumers (end users)
      # can identify and consume the related resource. Common names are
      # "connection-details" or "credentials".
      identifier: tls-secret

      # "service" or "kcp"
      origin: service

      # the GVR of the related resource
      group: ""
      version: v1
      resource: secrets

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
{% endraw %}

### Templates

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
* Go template: {% raw %}`{{ .Object.spec.secretName }}`{% endraw %}

Just like with references, the configured template is evaluated twice, once for each side of the
synchronization. You can use the `Side` variable to allow for fully customized names on each side:

{% raw %}
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
{% endraw %}

See [Templating](templating.md) for more information on how to use templates in PublishedResources.

### Label Selectors

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
    namespace: kube-system
    name: "{{ .ClusterName }}-{{ .Object.metadata.namespace | sha3short }}-{{ .Object.metadata.name | sha3short }}"

  related:
    - identifier: tls-secrets

      # "service" or "kcp"
      origin: service

      group: ""
      version: v1
      resource: secrets

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
            template:
              template: "{{ .Value }}-foo"

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
{% endraw %}

There are two possible usages of Go templates when using label selectors. See [Templating](templating.md)
for more information on how to use templates in PublishedResources in general.

#### Selector Templates

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

#### Rewrite Rules

Each found related object on the origin side needs to have its own name on the destination side. To
map from the origin to the destination side, regular expressions (see example snippet) or Go
templates can be used.

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
