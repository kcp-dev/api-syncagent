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
	"fmt"
	"log"

	"github.com/spf13/pflag"

	"github.com/kcp-dev/api-syncagent/internal/discovery"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"
)

var (
	kubeconfigPath string
)

func main() {
	ctx := context.Background()

	pflag.StringVar(&kubeconfigPath, "kubeconfig", "", "Path to the kubeconfig file to use (defaults to $KUBECONFIG)")
	pflag.Parse()

	if pflag.NArg() == 0 {
		log.Fatal("No argument given. Please specify a GroupKind in the form 'Kind.apigroup.com' (case-sensitive) to pull.")
	}

	gk := schema.ParseGroupKind(pflag.Arg(0))

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	loadingRules.ExplicitPath = kubeconfigPath

	startingConfig, err := loadingRules.GetStartingConfig()
	if err != nil {
		log.Fatalf("Failed to load Kubernetes configuration: %v.", err)
	}

	config, err := clientcmd.NewDefaultClientConfig(*startingConfig, nil).ClientConfig()
	if err != nil {
		log.Fatalf("Failed to load Kubernetes configuration: %v.", err)
	}

	discoveryClient, err := discovery.NewClient(config)
	if err != nil {
		log.Fatalf("Failed to create discovery client: %v.", err)
	}

	crd, err := discoveryClient.RetrieveCRD(ctx, gk)
	if err != nil {
		log.Fatalf("Failed to pull CRD: %v.", err)
	}

	enc, err := yaml.Marshal(crd)
	if err != nil {
		log.Fatalf("Failed to encode CRD as YAML: %v.", err)
	}

	fmt.Println(string(enc))
}
