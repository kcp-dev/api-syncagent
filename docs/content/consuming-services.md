# Consuming Services

This document describes how to use (consume) services offered by a Sync Agent.

## Background

A "service" defines a unique Kubernetes API Group and offers a number of resources (types) to
use. A service could offer certificate management, databases, cloud infrastructure or any other set
of Kubernetes resources.

Services are provided by service owners, who run their own Kubernetes clusters and take care of the
maintenance and scaling tasks for the workload provisioned by all users of the service(s) they
offer.

A Service provided by a Sync Agent should not be confused with a Kubernetes Service. Internally, a
"Sync Agent Service" is ultimately translated into a kcp `APIExport` with a number of
`APIResourceSchemas` (which are more or less equivalent to CRDs).

## Consuming a Service

To consume a service (or to make use of an `APIExport`) you have to create an `APIBinding` object
in the kcp workspace where the service should be used. This section assumes that you are familiar
with kcp on the command line and have the kcp kubectl plugin installed.

First you need to get the kubeconfig for accessing your kcp workspaces. Once you have set your
kubeconfig up, make sure you're in the correct namespace by using
`kubectl ws <path to your workspace>`. Use `kubectl ws .` if you're unsure where you're at.

To enable a Service, use `kcp bind apiexport` and specify the path to and name of the `APIExport`.

```bash
# kubectl kcp bind apiexport <path to APIExport>:<API Group of the Service>
kubectl kcp bind apiexport :root:my-org:my.fancy.api
```

Without the plugin, you can create an `APIBinding` manually, simply `kubectl apply` this:

```yaml
apiVersion: apis.kcp.io/v1alpha1
kind: APIBinding
metadata:
  name: my.fancy.api
spec:
  reference:
    export:
      name: my.fancy.api
      path: root:my-org
```

Shortly after, the new API will be available in the workspace. Check via `kubectl api-resources`.
You can now create objects for types in that API group to your liking and they will be synced and
processed behind the scenes.

Note that a Service often has related resources, often Secrets and ConfigMaps. You must explicitly
allow the Service to access these in your workspace and this means editing/patching the `APIBinding`
object (the kcp kubectl plugin currently has no support for managing permission claims). For each of
the claimed resources, you have to accept or reject them:

```yaml
spec:
  permissionClaims:
    # Nearly all Sync Agents require access to namespaces, rejecting this will
    # most likely break the Service, even more than rejecting any other claim.
    - all: true
      resources: namespaces
      state: Accepted
    - all: true
      resources: secrets
      state: Accepted # or Rejected
```

Rejecting a claim will severely impact a Service, if not even break it. Consult with the Service's
documentation or the service owner if rejecting a claim is supported.

When you _change into_ (`kubctl ws …`) a different workspace, kubectl will inform you if there are
outstanding permission claims that you need to accept or reject.
