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
	"github.com/kcp-dev/logicalcluster/v3"
)

type clusterInfo struct {
	clusterName   logicalcluster.Name
	workspacePath logicalcluster.Path
}

func NewClusterInfo(clusterName logicalcluster.Name) clusterInfo {
	return clusterInfo{
		clusterName: clusterName,
	}
}

func (c *clusterInfo) WithWorkspacePath(path logicalcluster.Path) clusterInfo {
	return clusterInfo{
		clusterName:   c.clusterName,
		workspacePath: path,
	}
}
