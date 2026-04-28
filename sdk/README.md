# API Sync-Agent SDK

This directory contains the syncagent's SDK: re-usable Go API types and generated functions for
integrating the syncagent into 3rd-party applications.

## Usage

To install the SDK, simply `go get` it:

```bash
go get github.com/kcp-dev/api-syncagent/sdk@latest
```

and then in your code import the desired types:

```go
package main

import apisyncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

func createPublishedResource() *apisyncagentv1alpha1.PublishedResource {
   pr := &apisyncagentv1alpha1.PublishedResource{}
   pr.Name = "publish-crontabs"
   pr.Namespace = "default"

   return pr
}
```

## SDK Design

The SDK comes as a standalone Go module: `github.com/kcp-dev/api-syncagent/sdk`

The module reduces the transitive dependencies that consumers have to worry about when they want to
integrate the syncagent. To that end, the SDK is meant to provide the broadest possible
compatibility: dependencies are on the *lowest* version that is usable by the syncagent. This drift
between the syncagent's dependencies and those of the SDK is an intended feature of the SDK.

The actual dependency versions used in the syncagent binaries are controlled exclusively via the
root directory's `go.mod`. Specifically, the SDK is not meant to propagate security fixes to
consumers and force them to upgrade when it might be inconvenient to them.

## Development Guidelines

* Do not update the `go` constraint in the `go.mod` file manually, let `go mod tidy` update it only
  when necessary. The `go` constraint has no influence on what Go version the syncagent is actually
  built with. It can, however, cause serious annoyances for downstream consumers.
* Likewise, only bump dependencies to keep the SDK compatible with the main module.
