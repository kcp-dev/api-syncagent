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
	"net"
	"net/url"
	"strings"

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

	// APIExportEndpointSliceRef references the APIExportEndpointSlice within a kcp workspace that this
	// Sync Agent should work with by name. The agent will automatically manage the resource schemas
	// in the APIExport referenced by this endpoint slice.
	APIExportEndpointSliceRef string

	PublishedResourceSelectorString string
	PublishedResourceSelector       labels.Selector

	KubeconfigHostOverride   string
	KubeconfigCAFileOverride string

	// APIExportHostPortOverrides allows overriding the host:port of URLs found in
	// APIExportEndpointSlice. Format: "original-host:port=new-host:port".
	// Can be specified multiple times.
	APIExportHostPortOverrides []string

	// parsedHostPortOverrides stores the parsed overrides (original -> new).
	parsedHostPortOverrides []hostPortOverride

	LogOptions log.Options

	MetricsAddr string
	HealthAddr  string

	DisabledControllers []string
}

type hostPortOverride struct {
	Original string
	New      string
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
	flags.StringVar(&o.APIExportEndpointSliceRef, "apiexportendpointslice-ref", o.APIExportEndpointSliceRef, "name of the APIExportEndpointSlice in kcp that this Sync Agent is powering")
	flags.StringVar(&o.PublishedResourceSelectorString, "published-resource-selector", o.PublishedResourceSelectorString, "restrict this Sync Agent to only process PublishedResources matching this label selector (optional)")
	flags.BoolVar(&o.EnableLeaderElection, "enable-leader-election", o.EnableLeaderElection, "whether to perform leader election")
	flags.StringVar(&o.KubeconfigHostOverride, "kubeconfig-host-override", o.KubeconfigHostOverride, "override the host configured in the local kubeconfig")
	flags.StringVar(&o.KubeconfigCAFileOverride, "kubeconfig-ca-file-override", o.KubeconfigCAFileOverride, "override the server CA file configured in the local kubeconfig")
	flags.StringSliceVar(&o.APIExportHostPortOverrides, "apiexport-hostport-override", o.APIExportHostPortOverrides, "override the host:port in APIExportEndpointSlice URLs (format: original-host:port=new-host:port, can be specified multiple times)")
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

	if len(o.APIExportEndpointSliceRef) == 0 {
		errs = append(errs, errors.New("--apiexportendpointslice-ref is required"))
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

	for i, s := range o.APIExportHostPortOverrides {
		if err := validateHostPortOverride(s); err != nil {
			errs = append(errs, fmt.Errorf("invalid --apiexport-hostport-override #%d %q: %w", i+1, s, err))
		}
	}

	return utilerrors.NewAggregate(errs)
}

func validateHostPortOverride(s string) error {
	parts := strings.Split(s, "=")
	if len(parts) != 2 {
		return errors.New("format must be 'original-host:port=new-host:port'")
	}

	// validate that both sides have valid host:port combination
	for i, part := range parts {
		if _, _, err := net.SplitHostPort(part); err != nil {
			return fmt.Errorf("part %d is not a valid host:port: %w", i+1, err)
		}
	}

	return nil
}

func (o *Options) Complete() error {
	errs := []error{}

	if len(o.AgentName) == 0 {
		o.AgentName = o.APIExportEndpointSliceRef + "-syncagent"
	}

	if s := o.PublishedResourceSelectorString; len(s) > 0 {
		selector, err := labels.Parse(s)
		if err != nil {
			errs = append(errs, fmt.Errorf("invalid --published-resource-selector %q: %w", s, err))
		}
		o.PublishedResourceSelector = selector
	}

	for _, s := range o.APIExportHostPortOverrides {
		parts := strings.Split(s, "=")
		o.parsedHostPortOverrides = append(o.parsedHostPortOverrides, hostPortOverride{
			Original: parts[0],
			New:      parts[1],
		})
	}

	return utilerrors.NewAggregate(errs)
}

// ApplyURLOverride applies the host:port overrides to the given URL if configured.
// It applies all configured overrides in order.
func (o *Options) ApplyURLOverride(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL, err
	}

	// Apply each override in order
	currentHost := parsed.Host
	for _, override := range o.parsedHostPortOverrides {
		if currentHost == override.Original {
			currentHost = override.New
		}
	}

	parsed.Host = currentHost

	return parsed.String(), nil
}
