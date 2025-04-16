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

## What happens when CRDs are updated?

At the moment, nothing. `APIResourceSchemas` in kcp are immutable and the Sync Agent currently does
not attempt to update existing schemas in an `APIExport`. If you add a _new_ CRD that you want to
publish, that's fine, it will be added to the `APIExport`. But changes to existing CRDs require
manual work.

To trigger an update:

* remove the `APIResourceSchema` from the `latestResourceSchemas`,
* delete the `APIResourceSchema` object in kcp,
* restart the api-syncagent

## Does the Sync Agent handle permission claims?

Only those required for its own operation. If you configure a namespaced resource to sync, it will
automatically add a claim for `namespaces` in kcp, plus it will add either `configmaps` or `secrets`
if related resources are configured in a `PublishedResource`. But you cannot specify additional
permissions claims.

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
