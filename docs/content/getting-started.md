# Getting Started with the Sync Agent

All that is necessary to run the Sync Agent is a running Kubernetes cluster (for testing you can use
[kind][kind]) and a [kcp][kcp] installation.

## Prerequisites

- A running Kubernetes cluster to run the Sync Agent in.
- A running kcp installation as the source of truth.
- A kubeconfig with admin or comparable permissions in a specific kcp workspace.

## APIExport Setup

Before installing the Sync Agent it is necessary to create an `APIExport` on kcp. The `APIExport` should
be empty, because it is updated later by the Sync Agent, but it defines the new API group we're
introducing. An example file could look like this:

```yaml
apiVersion: apis.kcp.io/v1alpha1
kind: APIExport
metadata:
  name: test.example.com
spec: {}
```

Create a file with a similar content (you most likely want to change the name, as that is the API
group under which your published resources will be made available) and create it in a kcp workspace
of your choice:

```sh
# use the kcp kubeconfig
$ export KUBECONFIG=/path/to/kcp.kubeconfig

# nativagate to the workspace where the APIExport should exist
$ kubectl ws :workspace:you:want:to:create:it

# create it
$ kubectl create --filename apiexport.yaml
apiexport/test.example.com created
```

## Sync Agent Installation

The Sync Agent can be installed into any namespace, but in our example we are going with `kcp-system`.
It doesn't necessarily have to live in the same Kubernetes cluster where it is synchronizing data
to, but that is the common setup. Ultimately the Sync Agent synchronizes data between two kube
endpoints.

Now that the `APIExport` is created, switch to the Kubernetes cluster from which you wish to
[publish resources](./publish-resources/index.md). You will need to ensure that a kubeconfig with access to
the kcp workspace that the `APIExport` has been created in is stored as a `Secret` on this cluster.
Make sure that the kubeconfig points to the right workspace (not necessarily the `root` workspace).

This can be done via a command like this:

```sh
$ kubectl create secret generic kcp-kubeconfig \
  --namespace kcp-system \
  --from-file "kubeconfig=admin.kubeconfig"
```

### Helm Chart Setup

The Sync Agent is shipped as a Helm chart and to install it, the next step is preparing a `values.yaml`
file for the Sync Agent Helm chart. We need to pass the target `APIExport`, a name for the Sync Agent
itself and a reference to the kubeconfig secret we just created.

```yaml
# Required: the name of the APIExport in kcp that this Sync Agent is supposed to serve.
apiExportName: test.example.com

# Required: This Agent's public name, used to signal ownership over locally synced objects.
# This value must be a valid Kubernetes label value, see
# https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/#syntax-and-character-set
# for more information.
# Changing this value after the fact will make the agent ignore previously created objects,
# so beware and relabel if necessary.
agentName: unique-test

# Required: Name of the Kubernetes Secret that contains a "kubeconfig" key,
# with the kubeconfig provided by kcp to access it.
kcpKubeconfig: kcp-kubeconfig
```

Once this `values.yaml` file is prepared, install a recent development build of the Sync Agent:

```sh
helm repo add kcp https://kcp-dev.github.io/helm-charts
helm repo update

helm install kcp-api-syncagent kcp/api-syncagent \
  --values values.yaml \
  --namespace kcp-system
```

Two `kcp-api-syncagent` Pods should start in the `kcp-system` namespace. If they crash you will need to
identify the reason from container logs. A possible issue is that the provided kubeconfig does not
have permissions against the target kcp workspace.

### Service Cluster RBAC

The Sync Agent usually requires additional RBAC on the service cluster to function properly. The
Helm chart will automatically allow it to read CRDs, namespaces and Secrets, but depending on how
you configure your PublishedResources, additional permissions need to be created.

For example, if the Sync Agent is meant to create `Certificate` objects (defined by cert-manager),
you would need to grant it permissions on those:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: 'api-syncagent:unique-test'
rules:
  - apiGroups:
      - cert-manager.io
    resources:
      - certificates
    verbs:
      - get
      - list
      - watch
      - create
      - update

---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: 'api-syncagent:unique-test'
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: 'api-syncagent:unique-test'
subjects:
  - kind: ServiceAccount
    name: 'kcp-api-syncagent'
    namespace: kcp-system
```

**NB:** Even though the PublishedResources might only create/update Certificates in a single namespace,
due to the inner workings of the Agent they will still be watched (cached) cluster-wide. So you can
tighten permissions on `create`/`update` operations to certain namespaces, but `watch` permissions
need to be granted cluster-wide.

### kcp RBAC

The Helm chart is installed on the service cluster and so cannot provision the necessary RBAC for
the Sync Agent within kcp. Usually whoever creates the `APIExport` is also responsible for creating
the RBAC rules that grant the Agent access.

The Sync Agent needs to

* access the workspace of its `APIExport`,
* get the `LogicalCluster`,
* manage its `APIExport`,
* manage `APIResourceSchemas` and
* access the virtual workspace for its `APIExport`.

This can be achieved by applying RBAC like this _in the workspace where the `APIExport` resides_:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: api-syncagent-mango
rules:
  # get the LogicalCluster
  - apiGroups:
      - core.kcp.io
    resources:
      - logicalclusters
    resourceNames:
      - cluster
    verbs:
      - get
  # manage its APIExport
  - apiGroups:
      - apis.kcp.io
    resources:
      - apiexports
    resourceNames:
      - test.example.com
    verbs:
      - get
      - list
      - watch
      - patch
      - update
  # manage APIResourceSchemas
  - apiGroups:
      - apis.kcp.io
    resources:
      - apiresourceschemas
    verbs:
      - get
      - list
      - watch
      - create
  # access the virtual workspace
  - apiGroups:
      - apis.kcp.io
    resources:
      - apiexports/content
    resourceNames:
      - test.example.com
    verbs:
      - '*'

---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: api-syncagent-mango:system
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: api-syncagent-mango
subjects:
  - kind: User
    name: api-syncagent-mango

---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: api-syncagent-mango:access
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: system:kcp:workspace:access
subjects:
  - kind: User
    name: api-syncagent-mango
```

## Publish Resources

Once the Sync Agent Pods are up and running, you should be able to follow the
[Publishing Resources](./publish-resources/index.md) guide.

## Consume Service

Once resources have been published through the Sync Agent, they can be consumed on the kcp side (i.e.
objects on kcp will be synced back and forth with the service cluster). Follow the
guide to [consuming services](consuming-services.md).

[kind]: https://github.com/kubernetes-sigs/kind
[kcp]: https://kcp.io
