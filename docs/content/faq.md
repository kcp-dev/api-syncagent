# Frequently Asked Questions

## Can I run multiple Sync Agents on the same service cluster?

Yes, absolutely, however you must configure them properly:

A given `PublishedResource` must only ever be processed by a single Sync Agent Pod. The Helm chart
configures leader-election by default, so you can scale up to have Pods on stand-by if needed.

By default the Sync Agent will discover and process all `PublishedResources` in your cluster. Use
the `--published-resource-selector` (`publishedResourceSelector` in the Helm values.yaml) to
restrict an Agent to a subset of published resources.

## Can I synchronize multiple kcp setups onto the same service cluster?

Only if you have distinct API groups (and therefore also distinct `PublishedResources`) for them.
You cannot currently publish the same API group onto multiple kcp setups. See issue #13 for more
information.

## Can I have additional resources in APIExports, unmanaged by the Sync Agent?

Yes, you can. The agent will only ever change those resourceSchemas that match group/resource of
the configured `PublishedResources`. So if you configure the agent to publish
`cert-manager.io/Certificate`, this would "claim" all resource schemas ending in
`.certificates.cert-manager.io`. When updating the `APIExport`, the agent will only touch schemas
with this suffix and leave all others alone.

This is also used when a `PublishedResource` is deleted: Since the `APIResourceSchema` remains in kcp,
but is no longer configured in the agent, the agent will simply ignore the schema in the `APIExport`.
This allows for async cleanup processes to happen before an admin ultimately removes the old
schema from the `APIExport`.

## Does the Sync Agent handle permission claims?

Only those required for its own operation. The syncagent will add the following permission claims
to the APIExport it manages:

* `events` (always)
* `namespaces` (always)
* `core.kcp.io/logicalclusters` (if `enableWorkspacePaths` is set in a `PublishedResource`)
* any resource used as related resources (most often this means `configmaps` or `secrets`, but could
  be any resource)

The syncagent will always overwrite the entire list of permission claims, i.e. you cannot have custom
claims in an APIExport managed by the api-syncagent.

## I am seeing errors in the agent logs, what's going on?

Errors like

> reflector.go:561] k8s.io/client-go@v0.31.2/tools/cache/reflector.go:243: failed to list
> example.com/v1, Kind=Dummy: the server could not find the requested resource

or

> reflector.go:158] "Unhandled Error" err="k8s.io/client-go@v0.31.2/tools/cache/reflector.go:243:
> Failed to watch kcp.example.com/v1, Kind=Dummy: failed to list kcp.example.com/v1, Kind=Dummy:
> the server could not find the requested resource" logger="UnhandledError"

are typical when bootstrapping new APIExports in kcp. They are only cause for concern if they
persist after configuring all PublishedResources.
