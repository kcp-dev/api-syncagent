/*
Copyright 2025 The KCP Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package sync

import (
	"context"

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/client-go/tools/record"
)

type contextKey int

const (
	clusterContextKey contextKey = iota
	workspaceContextKey
	recorderContextKey
)

func WithClusterName(ctx context.Context, cluster logicalcluster.Name) context.Context {
	return context.WithValue(ctx, clusterContextKey, cluster)
}

func clusterFromContext(ctx context.Context) logicalcluster.Name {
	cluster, ok := ctx.Value(clusterContextKey).(logicalcluster.Name)
	if !ok {
		return ""
	}

	return cluster
}

func WithWorkspacePath(ctx context.Context, path logicalcluster.Path) context.Context {
	return context.WithValue(ctx, workspaceContextKey, path)
}

func workspacePathFromContext(ctx context.Context) logicalcluster.Path {
	path, ok := ctx.Value(workspaceContextKey).(logicalcluster.Path)
	if !ok {
		return logicalcluster.None
	}

	return path
}

// WithEventRecorder adds a event recorder to the context. The recorder must be configured
// for the kcp side of the synchronization as the agent never issues events on the local cluster.
func WithEventRecorder(ctx context.Context, recorder record.EventRecorder) context.Context {
	return context.WithValue(ctx, recorderContextKey, recorder)
}

func recorderFromContext(ctx context.Context) record.EventRecorder {
	cluster, ok := ctx.Value(recorderContextKey).(record.EventRecorder)
	if !ok {
		return nil
	}

	return cluster
}
