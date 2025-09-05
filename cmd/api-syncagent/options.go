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
	"errors"
	"fmt"

	"github.com/spf13/pflag"

	"github.com/kcp-dev/api-syncagent/internal/log"

	"k8s.io/apimachinery/pkg/labels"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
)

type Options struct {
	// NB: Not actually defined here, as ctrl-runtime registers its
	// own --kubeconfig flag that is required to make its GetConfigOrDie()
	// work.
	// KubeconfigFile string

	// KcpKubeconfig is the kubeconfig that gives access to kcp. This
	// kubeconfig's cluster URL has to point to the workspace where the APIExport
	// referenced via APIExportRef lives.
	KcpKubeconfig string

	// Namespace is the namespace that the Sync Agent runs in.
	Namespace string

	// Whether or not to perform leader election (requires permissions to
	// manage coordination/v1 leases)
	EnableLeaderElection bool

	// AgentName can be used to give this Sync Agent instance a custom name. This name is used
	// for the Sync Agent resource inside kcp. This value must not be changed after a Sync Agent
	// has registered for the first time in kcp.
	// If not given, defaults to "<service ref>-syncagent".
	AgentName string

	// APIExportRef references the APIExport within a kcp workspace that this
	// Sync Agent should work with by name. The APIExport has to already exist, but it must not have
	// any pre-existing resource schemas configured, the agent will fill them in based on
	// PublishedResources.
	//
	// Deprecated: Use APIExportEndpointSliceRef instead. If an APIExport is referenced, the agent
	// will attempt to find and use an endpoint slice of the same name.
	APIExportRef string

	// APIExportEndpointSliceRef references the APIExportEndpointSlice within a kcp workspace that this
	// Sync Agent should work with by name. The agent will automatically manage the resource schemas
	// in the APIExport referenced by this endpoint slice.
	APIExportEndpointSliceRef string

	PublishedResourceSelectorString string
	PublishedResourceSelector       labels.Selector

	KubeconfigHostOverride   string
	KubeconfigCAFileOverride string

	LogOptions log.Options

	MetricsAddr string
	HealthAddr  string

	DisabledControllers []string
}

func NewOptions() *Options {
	return &Options{
		LogOptions:                log.NewDefaultOptions(),
		PublishedResourceSelector: labels.Everything(),
		MetricsAddr:               "127.0.0.1:8085",
	}
}

func (o *Options) AddFlags(flags *pflag.FlagSet) {
	o.LogOptions.AddPFlags(flags)

	flags.StringVar(&o.KcpKubeconfig, "kcp-kubeconfig", o.KcpKubeconfig, "kubeconfig file of kcp")
	flags.StringVar(&o.Namespace, "namespace", o.Namespace, "Kubernetes namespace the Sync Agent is running in")
	flags.StringVar(&o.AgentName, "agent-name", o.AgentName, "name of this Sync Agent, must not be changed after the first run, can be left blank to auto-generate a name")
	flags.StringVar(&o.APIExportRef, "apiexport-ref", o.APIExportRef, "name of the APIExport in kcp that this Sync Agent is powering (deprecated, use --apiexportendpointslice-ref instead)")
	flags.StringVar(&o.APIExportEndpointSliceRef, "apiexportendpointslice-ref", o.APIExportEndpointSliceRef, "name of the APIExportEndpointSlice in kcp that this Sync Agent is powering")
	flags.StringVar(&o.PublishedResourceSelectorString, "published-resource-selector", o.PublishedResourceSelectorString, "restrict this Sync Agent to only process PublishedResources matching this label selector (optional)")
	flags.BoolVar(&o.EnableLeaderElection, "enable-leader-election", o.EnableLeaderElection, "whether to perform leader election")
	flags.StringVar(&o.KubeconfigHostOverride, "kubeconfig-host-override", o.KubeconfigHostOverride, "override the host configured in the local kubeconfig")
	flags.StringVar(&o.KubeconfigCAFileOverride, "kubeconfig-ca-file-override", o.KubeconfigCAFileOverride, "override the server CA file configured in the local kubeconfig")
	flags.StringVar(&o.MetricsAddr, "metrics-address", o.MetricsAddr, "host and port to serve Prometheus metrics via /metrics (HTTP)")
	flags.StringVar(&o.HealthAddr, "health-address", o.HealthAddr, "host and port to serve probes via /readyz and /healthz (HTTP)")
	flags.StringSliceVar(&o.DisabledControllers, "disabled-controllers", o.DisabledControllers, fmt.Sprintf("comma-separated list of controllers (out of %v) to disable (can be given multiple times)", sets.List(availableControllers)))
}

func (o *Options) Validate() error {
	errs := []error{}

	if err := o.LogOptions.Validate(); err != nil {
		errs = append(errs, err)
	}

	if len(o.Namespace) == 0 {
		errs = append(errs, errors.New("--namespace is required"))
	}

	if len(o.AgentName) > 0 {
		if e := validation.IsDNS1035Label(o.AgentName); len(e) > 0 {
			errs = append(errs, fmt.Errorf("--agent-name is invalid: %v", e))
		}
	}

	if len(o.APIExportRef) == 0 && len(o.APIExportEndpointSliceRef) == 0 {
		errs = append(errs, errors.New("either --apiexportendpointslice-ref or --apiexport-ref is required"))
	}

	if len(o.APIExportRef) != 0 && len(o.APIExportEndpointSliceRef) != 0 {
		errs = append(errs, errors.New("--apiexportendpointslice-ref and --apiexport-ref are mutually exclusive"))
	}

	if len(o.KcpKubeconfig) == 0 {
		errs = append(errs, errors.New("--kcp-kubeconfig is required"))
	}

	if s := o.PublishedResourceSelectorString; len(s) > 0 {
		if _, err := labels.Parse(s); err != nil {
			errs = append(errs, fmt.Errorf("invalid --published-resource-selector %q: %w", s, err))
		}
	}

	disabled := sets.New(o.DisabledControllers...)
	unknown := disabled.Difference(availableControllers)

	if unknown.Len() > 0 {
		errs = append(errs, fmt.Errorf("unknown controller(s) %v, mut be any of %v", sets.List(unknown), sets.List(availableControllers)))
	}

	return utilerrors.NewAggregate(errs)
}

func (o *Options) Complete() error {
	errs := []error{}

	if len(o.AgentName) == 0 {
		o.AgentName = o.APIExportRef + "-syncagent"
	}

	if s := o.PublishedResourceSelectorString; len(s) > 0 {
		selector, err := labels.Parse(s)
		if err != nil {
			errs = append(errs, fmt.Errorf("invalid --published-resource-selector %q: %w", s, err))
		}
		o.PublishedResourceSelector = selector
	}

	return utilerrors.NewAggregate(errs)
}
