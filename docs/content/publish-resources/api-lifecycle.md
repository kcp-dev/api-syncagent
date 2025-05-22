# API Lifecycle

In only the rarest of cases will the first version of a CRD be also its final version. Instead usually
CRDs evolve over time and Kubernetes has strong, though sometimes hard to use, support for managing
different versions of CRDs and their resources.

To understand how CRDs work in the context of the Sync Agent, it's important to first get familiar
with the [regular Kubernetes behaviour](https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definition-versioning/)
regarding CRD versioning.

## Basics

The Sync Agent will, whenever a published CRD changes (this can also happen when the projection rules
inside a `PublishedResource` are updated), create a new `APIResourceSchema` (ARS) in kcp. The name and
version of this ARS are based on a hash of the projected CRD. Undoing a change would make the agent
re-use the previously created ARS (ARS are immutable).

After every reconciliation, the list of latest resource schemas in the configured `APIExport` is
updated. For this the agent will find all ARS that belong to it (based on an ownership label) and
then merge them into the `APIExport`. Resource schemas for unknown group/resource combinations are
left untouched, so admins are free to add additional resource schemas to an `APIExport`.

This means that every change to a CRD on the service cluster is applied practically immediately in
each workspace that consumes the `APIExport`. Administrators are wise to act carefully when working
with their CRDs on their service cluster. Sometimes it can make sense to turn-off the agent before
testing new CRDs, even though this will temporarily suspend the synchronization.

## Single-Version CRDs

A very common scenario is to only ever have a single version inside each CRD and keeping this version
perpetually backwards-compatible. As long as all consumers are aware that certain fields might not
be set yet in older objects, this scheme works out generally fine.

The agent will handle this scenario just fine by itself. Whenever a CRD is updated, it will reflect
those changes back into a new `APIResourceSchema` and update the `APIExport`, making the changes
immediately available to all consumers. Since the agent itself doesn't much care for the contents of
objects, it itself is not affected by any structural changes in CRDs, as long as it is able to apply
them on the underlying Kubernetes cluster.

## Multi-Version CRDs

Having multiple versions in a single CRD is immediately much more work, since in Kubernetes all
versions of a CRD must be _losslessly_ convertible to every other version. Without CEL expressions
or a dedicated conversion webhook this is practically impossible to achieve.

At the moment kcp does not support CEL-based conversions, and there is no support for configuring a
conversion webhook inside the Sync Agent either. This is because such a webhook would need to run
very close to the kcp shards and it's simply out of scope for such a component to be described and
deployed by the Sync Agent, let alone a trust nightmare for the kcp operators who would have to run
foreign webhooks on their cluster.

Since both conversion mechanisms are not usable in the current state of kcp and the Sync Agent,
having multiple versions in a CRD can be difficult to manage.

Generally the Sync Agent itself does not care much about the schemas of each CRD version or the
convertibility between them. The synchronization works by using unstructured clients to the storage
versison of the CRD on both sides (in kcp and on the service cluster). Which version is the storage
version is up to the CRD author.

When publishing multiple versions of a CRD

* only those versions marked as `served` can be picked and
* if no `storage` version is picked, the latest (highest) version will be chosen automatically as
  the storage version in kcp.
