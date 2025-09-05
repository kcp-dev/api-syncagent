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
	"flag"
	"fmt"
	golog "log"
	"slices"
	"strings"

	"github.com/go-logr/zapr"
	"github.com/spf13/pflag"
	"go.uber.org/zap"
	reconcilerlog "k8c.io/reconciler/pkg/log"

	"github.com/kcp-dev/api-syncagent/internal/controller/apiexport"
	"github.com/kcp-dev/api-syncagent/internal/controller/apiresourceschema"
	"github.com/kcp-dev/api-syncagent/internal/controller/syncmanager"
	syncagentlog "github.com/kcp-dev/api-syncagent/internal/log"
	"github.com/kcp-dev/api-syncagent/internal/version"
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	kcpdevv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrlruntime "sigs.k8s.io/controller-runtime"
	ctrlruntimelog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var availableControllers = sets.New("apiexport", "apiresourceschema", "sync")

func main() {
	ctx := context.Background()

	opts := NewOptions()
	opts.AddFlags(pflag.CommandLine)

	// ctrl-runtime will have added its --kubeconfig to Go's flag set
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()

	if err := opts.Validate(); err != nil {
		golog.Fatalf("Invalid command line: %v", err)
	}

	log := syncagentlog.NewFromOptions(opts.LogOptions)

	if err := opts.Complete(); err != nil {
		log.With(zap.Error(err)).Fatal("Invalid command line")
	}

	sugar := log.Sugar()

	// set the logger used by sigs.k8s.io/controller-runtime
	ctrlruntimelog.SetLogger(zapr.NewLogger(log.WithOptions(zap.AddCallerSkip(1))))
	reconcilerlog.SetLogger(sugar)

	if err := run(ctx, sugar, opts); err != nil {
		sugar.Fatalw("Sync Agent has encountered an error", zap.Error(err))
	}
}

func run(ctx context.Context, log *zap.SugaredLogger, opts *Options) error {
	v := version.NewAppVersion()
	log.With(
		"version", v.GitVersion,
		"name", opts.AgentName,
		"apiexport", opts.APIExportRef,
	).Info("Moin, I'm the kcp Sync Agent")

	// create the ctrl-runtime manager
	mgr, err := setupLocalManager(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to setup local manager: %w", err)
	}

	// load the kcp kubeconfig
	kcpRestConfig, err := loadKubeconfig(opts.KcpKubeconfig)
	if err != nil {
		return fmt.Errorf("failed to load kcp kubeconfig: %w", err)
	}

	// sanity check
	if !strings.Contains(kcpRestConfig.Host, "/clusters/") {
		return fmt.Errorf("kcp kubeconfig does not point to a specific workspace")
	}

	// We check if the APIExport/APIExportEndpointSlice exists and extract information we need to set up our kcpCluster.
	endpoint, err := resolveSyncEndpoint(ctx, kcpRestConfig, opts.APIExportEndpointSliceRef, opts.APIExportRef)
	if err != nil {
		return fmt.Errorf("failed to resolve APIExport/EndpointSlice: %w", err)
	}

	log.Infow("Resolved APIExport", "workspace", endpoint.APIExport.Path, "logicalcluster", endpoint.APIExport.Cluster)

	if s := endpoint.EndpointSlice; s != nil {
		log.Infow("Using APIExportEndpointSlice", "workspace", s.Path, "logicalcluster", s.Cluster)
	}

	// init the "permanent" kcp cluster connections

	// always need the managedKcpCluster
	managedKcpCluster, err := setupManagedKcpCluster(endpoint)
	if err != nil {
		return fmt.Errorf("failed to initialize managed kcp cluster: %w", err)
	}

	// start the kcp cluster caches when the manager boots up
	// (happens regardless of leader election status)
	if err := mgr.Add(managedKcpCluster); err != nil {
		return fmt.Errorf("failed to add managed kcp cluster runnable: %w", err)
	}

	// the endpoint cluster can be nil
	endpointKcpCluster, err := setupEndpointKcpCluster(endpoint)
	if err != nil {
		return fmt.Errorf("failed to initialize endpoint kcp cluster: %w", err)
	}

	if endpointKcpCluster != nil {
		if err := mgr.Add(endpointKcpCluster); err != nil {
			return fmt.Errorf("failed to add endpoint kcp cluster runnable: %w", err)
		}
	}

	startController := func(name string, creator func() error) error {
		if slices.Contains(opts.DisabledControllers, name) {
			log.Infof("Not starting %s controller because it is disabled.", name)
			return nil
		}

		if err := creator(); err != nil {
			return fmt.Errorf("failed to add %s controller: %w", name, err)
		}

		return nil
	}

	if err := startController("apiresourceschema", func() error {
		return apiresourceschema.Add(mgr, managedKcpCluster, endpoint.APIExport.Cluster, log, 4, opts.AgentName, opts.PublishedResourceSelector)
	}); err != nil {
		return err
	}

	if err := startController("apiexport", func() error {
		return apiexport.Add(mgr, managedKcpCluster, endpoint.APIExport.Cluster, log, opts.APIExportRef, opts.AgentName, opts.PublishedResourceSelector)
	}); err != nil {
		return err
	}

	// This controller is called "sync" because it makes the most sense to the users, even though internally the relevant
	// controller is the syncmanager (which in turn would start/stop the sync controllers).
	if err := startController("sync", func() error {
		cluster := endpointKcpCluster
		if cluster == nil {
			cluster = managedKcpCluster
		}

		var endpointSlice *kcpdevv1alpha1.APIExportEndpointSlice
		if endpoint.EndpointSlice != nil {
			endpointSlice = endpoint.EndpointSlice.APIExportEndpointSlice
		}

		// It doesn't matter which rest config we specify, as the URL will be overwritten with the
		// virtual workspace URL anyway.

		return syncmanager.Add(ctx, mgr, cluster, kcpRestConfig, log, endpoint.APIExport.APIExport, endpointSlice, opts.PublishedResourceSelector, opts.Namespace, opts.AgentName)
	}); err != nil {
		return err
	}

	log.Info("Starting kcp Sync Agent…")

	return mgr.Start(ctx)
}

func setupLocalManager(ctx context.Context, opts *Options) (manager.Manager, error) {
	scheme := runtime.NewScheme()
	restConfig := ctrlruntime.GetConfigOrDie()

	if opts.KubeconfigHostOverride != "" {
		restConfig.Host = opts.KubeconfigHostOverride
	}

	if opts.KubeconfigCAFileOverride != "" {
		// override the caData if it exists.
		if len(restConfig.TLSClientConfig.CAData) > 0 {
			restConfig.TLSClientConfig.CAData = nil
		}
		restConfig.TLSClientConfig.CAFile = opts.KubeconfigCAFileOverride
	}

	mgr, err := manager.New(restConfig, manager.Options{
		Scheme: scheme,
		BaseContext: func() context.Context {
			return ctx
		},
		Metrics:                 metricsserver.Options{BindAddress: opts.MetricsAddr},
		LeaderElection:          opts.EnableLeaderElection,
		LeaderElectionID:        "syncagent." + opts.AgentName,
		LeaderElectionNamespace: opts.Namespace,
		HealthProbeBindAddress:  opts.HealthAddr,
	})
	if err != nil {
		return nil, err
	}

	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to register local scheme %s: %w", corev1.SchemeGroupVersion, err)
	}

	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to register local scheme %s: %w", apiextensionsv1.SchemeGroupVersion, err)
	}

	if err := syncagentv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to register local scheme %s: %w", syncagentv1alpha1.SchemeGroupVersion, err)
	}

	return mgr, nil
}

func loadKubeconfig(filename string) (*rest.Config, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	loadingRules.ExplicitPath = filename

	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, nil).ClientConfig()
}
