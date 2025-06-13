/*
Copyright 2022 The KCP Authors.

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
	"fmt"
	"os"

	"github.com/spf13/pflag"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
)

type options struct {
	KubeconfigFile string
	Context        string

	NoProjection      bool
	APIResourceSchema bool
}

func newDefaultOptions() *options {
	return &options{}
}

func (o *options) AddFlags(flags *pflag.FlagSet) {
	flags.BoolVar(&o.NoProjection, "no-projection", o.NoProjection, "Disables projecting the GVK of the selected CRD")
	flags.BoolVar(&o.APIResourceSchema, "ars", o.APIResourceSchema, "Outputs an APIResourceSchema instead of a CustomResourceDefinition")

	flags.StringVar(&o.Context, "context", o.Context, "Name of the context in the kubeconfig file to use")
	flags.StringVar(&o.KubeconfigFile, "kubeconfig", o.KubeconfigFile, "The kubeconfig file of the cluster to read CRDs from (defaults to $KUBECONFIG).")
}

func (o *options) Complete() error {
	errs := []error{}

	if len(o.KubeconfigFile) == 0 {
		o.KubeconfigFile = os.Getenv("KUBECONFIG")
	}

	return utilerrors.NewAggregate(errs)
}

func (o *options) Validate() error {
	errs := []error{}

	if len(o.KubeconfigFile) == 0 {
		errs = append(errs, fmt.Errorf("--kubeconfig or $KUBECONFIG are required for this command"))
	}

	return utilerrors.NewAggregate(errs)
}
