# Comparison with kube-bind

[kube-bind](https://github.com/kube-bind/kube-bind) is another Kubernetes-based project that is seemingly
all about synchronizing objects between clusters. This page is meant to explain where the two projects
compliment each other.

## api-syncagent

The api-syncagent is a kcp-specific agent **installed by service providers** to publish CRD-based APIs
from a "service cluster" to a central kcp instance. It consists of a single main component, the
agent itself.

* The **agent** pushes the configured APIs (CRDs) from the service cluster into a kcp APIExport, making
  them available for use in kcp workspaces. It then takes objects created with those APIs in kcp and
  syncs (copies) them into the service cluster (actually the synchronization is two-way: the intended
  state (usually the `spec` and `metadata`) are copied from kcp to the service cluster, and the `status`
  is copied back into kcp). This forms a 1:1 relationship between 1 kcp APIExport to 1 Kubernetes
  service cluster.

    The intention is that objects copied onto the service cluster are then reconciled and processed by
    3rd party operators to provision the actual workload.

Because api-syncagent is specific to kcp, its goal is to support kcp-specific features as closely as
possible. Some of those features include API conversion rules in APIConversions (currently not
functional in kcp) or the upcoming virtual resource feature. Most importantly, these features do not
exist in vanilla Kubernetes and can't be replicated easily.

## kube-bind

kube-bind is a vendor-agnostic component, **installed by consumers** and talking to a kube-bind
compatible backend. It consists of 3 components:

* **Konnector Agent** – a user user-installed agent which can talk to many providers and sync object
  statuses back. It is intended to be used in a 1:N relationship: one consumer cluster to many
  provider clusters.
* **Provider Cluster** – a Kubernetes or kcp cluster, from which the provider can serve objects to
  multiple consumers.
* **Backend** – a backend is used to establish trust by checking OIDC authorization flow and creating
  an access-scoped kubeconfig for konnector agents to use. This can be made and distributed in
  other ways and so there is no one-size-fits-all backend, but instead they are built for each
  specific usecase/provider.

## Simillarities

As part of their operation, both kube-bind and the api-syncagent do in fact synchronize Kubernetes
objects. Both components move the spec from the consumer to the producer and move the status back
in the opposite direction.

However in most other aspects, the two projects differ dramatically in scope and purpose.

## Differences

| Aspect | api-syncagent | kube-bind |
| ------ | ------------- | --------- |
| **Installation Location**<br><small>Where is the active part installed to?</small> | provider cluster | consumer cluster |
| **Compatibility / Scope**<br><small>What is this software meant to be used with?</small> | synchronize objects between kcp and Kubernetes | handshake between two parties;<br>synchronize between any two Kubernetes endpoints |
| **Cluster Access**<br><small>In which direction is the communication established?</small> | provider connects to kcp<br><small>(provider cluster can be firewalled off)</small> | consumer to provider<br><small>(provider cluster needs to be publicly accessible)</small> |
| **Scope Support**<br><small>What kind of resources are supported?</small> | cluster scoped,<br>namespaced | cluster scoped,<br>namespaced |
| **Mutations**<br><small>Changing an object's content in transit</small> | ✅ | ❌ |
| **Projections**<br><small>Changing an object's group, version or kind in transit</small> | ✅ | ❌ |
| **Auxiliary Objects**<br><small>Related objects (often credential Secrets) that originate on the provider side and are synced back to the consumer</small> | ✅ | ❌ |

---

One major difference between the two projects is how in kube-bind, the consumer has to perform an
explicit handshake with an arbitrary service provider to initiate a contract between them. During this
handshake not only authentication information is exchanged, but also the list of provider APIs is
made available and installed onto consumer clusters.

With the api-syncagent, there is no handshake anymore, as kcp already handles this part: providers
create their own APIExports in kcp and offer them to consumers, who them just have to bind to them
without opening an explicit contract/connection to the provider. So in a sense, the whole API catalog
(the sum of APIs available by all providers) is already known in kcp, but in kube-bind it's something
that is handled out between consumer and provider individually.

This also means that the api-syncagent works indiscriminately of consumers: It will process any
Kubernetes object it finds in the APIExport virtual workspace in kcp, regardless of the consumer. In
kube-bind however each new consumer is required to authenticate and handshake with the provider.

---

Another difference is how versioning is handled. Since in kube-bind, one single konnector agent is
installed on the consumer cluster, it needs to be compatible with all consumed services. However with
the api-syncagent, each provider installs their own agent on their own infrastructure, completely
independent from other providers.

Technically, the api-syncagent is only an implementation detail. You could provide services with your
own tooling to kcp, if you wanted to, without the consumers every really noticing. In kube-bind, the
concrete implementation of the konnector and kubectl plugins are a fixed part of the kube-bind
ecosystem, with only the backend being exchangeable.

---

One could wonder if after the kube-bind handshake is complete and the APIs are installed on the local
consumer cluster, whether one could not simply use the api-syncagent for the actual object
synchronization.

This is not possible because...

* the api-syncagent requires a virtual workspace on one of the sync sides, whereas kube-bind works
  with any Kubernetes endpoint,
* the api-syncagent is meant to run on the provider cluster and therefore considers the objects on
  the remote side to be authoritative (the source of truth), making the sync work in the opposite
  direction if consumers were to install the agent,
* information like projection/mutation is not available to the agent, so it would not know if and how
  to change object metadata during syncing.

In fact, the actual synchronization of Kubernetes objects between two clusters isn't really the
major feature each of the projects implement. It's more about all the workflows and circumstances
surrounding the sync than the sync itself. Copying Kubernetes objects back and forth is not the
challenge.
