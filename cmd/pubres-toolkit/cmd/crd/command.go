/*
Copyright 2021 The KCP Authors.

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

package crd

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/kcp-dev/api-syncagent/cmd/pubres-toolkit/util"
	"github.com/kcp-dev/api-syncagent/internal/discovery"
	"github.com/kcp-dev/api-syncagent/internal/kcp"
	"github.com/kcp-dev/api-syncagent/internal/projection"

	"sigs.k8s.io/yaml"
)

func NewCommand(ctx context.Context) *cobra.Command {
	opts := newDefaultOptions()

	cmd := &cobra.Command{
		Use:   "crd [--no-projection] [--ars] pubres.yaml",
		Short: "Outputs a CRD based on a PublishedResource",
		RunE: func(c *cobra.Command, args []string) error {
			if err := opts.Complete(); err != nil {
				return err
			}

			if err := opts.Validate(); err != nil {
				return err
			}

			if len(args) != 1 {
				return errors.New("expected exactly one argument")
			}

			return run(ctx, opts, args[0])
		},
	}

	opts.AddFlags(cmd.Flags())

	return cmd
}

func run(ctx context.Context, o *options, pubResFile string) error {
	// load the given PubRes
	pubResource, err := util.ReadPublishedResourceFile(pubResFile)
	if err != nil {
		return fmt.Errorf("failed to read %q: %w", pubResFile, err)
	}

	// parse kubeconfig
	kubeconfig, err := util.ReadKubeconfig(o.KubeconfigFile, o.Context)
	if err != nil {
		return fmt.Errorf("invalid kubeconfig: %w", err)
	}

	clientConfig, err := kubeconfig.ClientConfig()
	if err != nil {
		return err
	}

	client, err := discovery.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf("failed to create discovery client: %w", err)
	}

	// find the CRD that the PublishedResource is referring to
	localGK := projection.PublishedResourceSourceGK(pubResource)

	// fetch the original, full CRD from the cluster
	crd, err := client.RetrieveCRD(ctx, localGK)
	if err != nil {
		return fmt.Errorf("failed to discover CRD defined in PublishedResource: %w", err)
	}

	// project the CRD (i.e. strip unwanted versions, rename values etc.)
	if !o.NoProjection {
		crd, err = projection.ProjectCRD(crd, pubResource)
		if err != nil {
			return fmt.Errorf("failed to apply projection rules: %w", err)
		}
	}

	// output the CRD right away if desired
	if !o.APIResourceSchema {
		enc, err := yaml.Marshal(crd)
		if err != nil {
			return fmt.Errorf("failed to encode CRD as YAML: %w", err)
		}

		fmt.Println(string(enc))
		return nil
	}

	// convert to APIResourceSchema otherwise
	arsName := kcp.GetAPIResourceSchemaName(crd)
	ars, err := kcp.CreateAPIResourceSchema(crd, arsName, "pubres-toolkit")
	if err != nil {
		return fmt.Errorf("failed to create APIResourceSchema: %w", err)
	}

	enc, err := yaml.Marshal(ars)
	if err != nil {
		return fmt.Errorf("failed to encode APIResourceSchema as YAML: %w", err)
	}

	fmt.Println(string(enc))

	return nil
}
