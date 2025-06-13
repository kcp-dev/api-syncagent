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

package main

import (
	"context"
	"os"

	"github.com/spf13/cobra"

	crdcommand "github.com/kcp-dev/api-syncagent/cmd/pubres-toolkit/cmd/crd"

	"k8s.io/component-base/cli"
	"k8s.io/component-base/version"
)

func main() {
	ctx := context.Background()
	cmd := &cobra.Command{
		Use:           "pubres-toolkit",
		Short:         "Toolkit for PublishedResources",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.AddCommand(crdcommand.NewCommand(ctx))

	if v := version.Get().String(); len(v) == 0 {
		cmd.Version = "<unknown>"
	} else {
		cmd.Version = v
	}

	os.Exit(cli.Run(cmd))
}
