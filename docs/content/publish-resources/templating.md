# Templating

{% raw %}
`PublishedResources` allow to use [Go templates](https://pkg.go.dev/text/template) in a number of
places. A simple template could look like `{{ .Object.spec.secretName | sha3sum }}`.
{% endraw %}

## General Usage

Users are encouraged to get familiar with the [Go documentation](https://pkg.go.dev/text/template)
on templates.

Specifically within the agent, the following rules apply when a template is evaluated:

* All templates must evaluate successfully. Any error will cancel the synchronization process for
  that object, potentially leaving it in a half-finished overall state.
* Templates should not output random values, as those can lead to reconcile loops and higher load
  on the service cluster.
* Any leading and trailing whitespace will be automatically trimmed from the template's output.
* All "objects" mentioned in this documentation refer technically to an `unstructured.Unstructured`
  value's `.Object` field, i.e. the JSON-decoded representation of a Kubernetes object.

## Functions

Templates can make use of all functions provided by [sprig/v3](https://masterminds.github.io/sprig/),
for example `join` or `b64enc`. The agent then adds the following functions:

* `sha3sum STRING`<br>Returns the hex-encoded SHA3-256 hash (32 characters long).
* `sha3short STRING [LENGTH=20]`<br>Returns the first `LENGTH` characters of the hex-encoded SHA3-256 hash.
* <del>`shortHash STRING`</del><br>Returns the first 20 characters of the hex-encoded SHA-1 hash.
  This function is only available for backwards compatibility when migrating `$variable`-based
  naming rules to use Go templates. New setups should not use this function, but one of the explicitly
  named ones, like `sha256sum` or `sha3sum`.

## Context

Depending on where a template is used, different data is available inside the template. The following
is a summary of those different values:

### Primary Object Naming Rules

This is for templates used in `.spec.naming`:

| Name          | Type                  | Description |
| ------------- | --------------------- | ----------- |
| `Object`      | `map[string]any`      | the full remote object found in a kcp workspace |
| `ClusterName` | `logicalcluster.Name` | the internal cluster identifier (e.g. "34hg2j4gh24jdfgf") |
| `ClusterPath` | `logicalcluster.Path` | the workspace path (e.g. "root:customer:projectx") |

### Related Object Template Source

This is for templates used in `.spec.related[*].object.template` and
`.spec.related[*].object.namespace.template`:

| Name          | Type                  | Description |
| ------------- | --------------------- | ----------- |
| `Side`        | `string`              | set to either one of the possible origin values (`kcp` or `origin`) to indicate for which cluster the template is currently being evaluated for |
| `Object`      | `map[string]any`      | the primary object belonging to the related object. Since related object templates are evaluated twice (once for the origin side and once for the destination side), object is the primary object on the side the template is evaluated for |
| `ClusterName` | `logicalcluster.Name` | the internal cluster identifier (e.g. "34hg2j4gh24jdfgf") of the kcp workspace that the synchronization is currently processing; this value is set for both evaluations, regardless of side |
| `ClusterPath` | `logicalcluster.Path` | the workspace path (e.g. "root:customer:projectx"); this value is set for both evaluations, regardless of side |

These templates are evaluated once on each side of the synchronization.

### Related Object Label Selectors

This is for templates used in `.spec.related[*].object.selector.matchLabels` and
`.spec.related[*].object.namespace.selector.matchLabels`, both keys and values:

| Name           | Type                  | Description |
| -------------- | --------------------- | ----------- |
| `LocalObject`  | `map[string]any`      | the primary object copy on the local side of the sync (i.e. on the service cluster) |
| `RemoteObject` | `map[string]any`      | the primary object original, in kcp |
| `ClusterName`  | `logicalcluster.Name` | the internal cluster identifier (e.g. "34hg2j4gh24jdfgf") of the kcp workspace that the synchronization is currently processing (where the remote object exists) |
| `ClusterPath`  | `logicalcluster.Path` | the workspace path (e.g. "root:customer:projectx") |

If a template for a key evaluates to an empty string, the key-value combination will be omitted from
the final selector. Empty values however are allowed.

### Related Object Label Selector Rewrites

This is for templates used in `.spec.related[*].object.selector.rewrite.template` and
`.spec.related[*].object.namespace.selector.rewrite.template`:

| Name            | Type                  | Description |
| --------------- | --------------------- | ----------- |
| `Value`         | `string`              | Either the a found namespace name (when a label selector was used to select the source namespaces for related objects) or the name of a found object (when a label selector was used to find objects). In the former case, the template should return the new namespace to use on the destination side, in the latter case it should return the new object name to use on the destination side. |
| `RelatedObject` | `map[string]any`      | When a rewrite is used to rewrite object names, RelatedObject is the original related object (found on the origin side). This enables you to ignore the given Value entirely and just select anything from the object itself. RelatedObject is `nil` when the rewrite is performed for a namespace. |
| `LocalObject`   | `map[string]any`      | the primary object copy on the local side of the sync (i.e. on the service cluster) |
| `RemoteObject`  | `map[string]any`      | the primary object original, in kcp |
| `ClusterName`   | `logicalcluster.Name` | the internal cluster identifier (e.g. "34hg2j4gh24jdfgf") of the kcp workspace that the synchronization is currently processing (where the remote object exists) |
| `ClusterPath`   | `logicalcluster.Path` | the workspace path (e.g. "root:customer:projectx") |
