# CRD Puller

The `crd-puller` can be used for testing and development in order to export a
CustomResourceDefinition for any Group/Kind (GK) in a Kubernetes cluster.

The main difference between this and kcp's own `crd-puller` is that this one
works based on GKs and not resources (i.e. on `apps/Deployment` instead of
`apps.deployments`). This is more useful since a PublishedResource publishes a
specific Kind and version. Also, this puller pulls all available versions, not
just the preferred version.

## Usage

```shell
export KUBECONFIG=/path/to/kubeconfig

./crd-puller Deployment.apps.k8s.io
```
