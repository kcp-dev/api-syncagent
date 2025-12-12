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

package utils

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"k8s.io/client-go/rest"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func requiredEnv(t *testing.T, name string) string {
	t.Helper()

	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("No $%s environment variable specified.", name)
	}

	return value
}

func ArtifactsDirectory(t *testing.T) string {
	return requiredEnv(t, "ARTIFACTS")
}

func AgentBinary(t *testing.T) string {
	return requiredEnv(t, "AGENT_BINARY")
}

var nonalpha = regexp.MustCompile(`[^a-z0-9_-]`)
var testCounters = map[string]int{}

func uniqueLogfile(t *testing.T, basename string) string {
	testName := strings.ToLower(t.Name())
	testName = nonalpha.ReplaceAllLiteralString(testName, "_")
	testName = strings.Trim(testName, "_")

	if basename != "" {
		testName += "_" + basename
	}

	counter := testCounters[testName]
	testCounters[testName]++

	return fmt.Sprintf("%s_%02d.log", testName, counter)
}

func RunAgent(
	ctx context.Context,
	t *testing.T,
	name string,
	kcpKubeconfig string,
	localKubeconfig string,
	apiExportEndpointSlice string,
	labelSelector string,
) context.CancelFunc {
	t.Helper()

	t.Logf("Running agent %q…", name)

	args := []string{
		"--agent-name", name,
		"--apiexportendpointslice-ref", apiExportEndpointSlice,
		"--enable-leader-election=false",
		"--kubeconfig", localKubeconfig,
		"--kcp-kubeconfig", kcpKubeconfig,
		"--namespace", "kube-system",
		"--log-format", "Console",
		"--log-debug=true",
		"--health-address", "0",
		"--metrics-address", "0",
	}

	if labelSelector != "" {
		args = append(args, "--published-resource-selector", labelSelector)
	}

	logFile := filepath.Join(ArtifactsDirectory(t), uniqueLogfile(t, ""))
	log, err := os.Create(logFile)
	if err != nil {
		t.Fatalf("Failed to create logfile: %v", err)
	}

	localCtx, cancel := context.WithCancel(ctx)

	cmd := exec.CommandContext(localCtx, AgentBinary(t), args...)
	cmd.Stdout = log
	cmd.Stderr = log

	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start api-syncagent: %v", err)
	}

	cancelAndWait := func() {
		cancel()
		_ = cmd.Wait()

		log.Close()
	}

	t.Cleanup(cancelAndWait)

	return cancelAndWait
}

func RunEnvtest(t *testing.T, extraCRDs []string) (string, ctrlruntimeclient.Client, context.CancelFunc) {
	t.Helper()

	testEnv := &envtest.Environment{
		ErrorIfCRDPathMissing: true,
	}

	rootDirectory := requiredEnv(t, "ROOT_DIRECTORY")
	extraCRDs = append(extraCRDs, "deploy/crd/kcp.io")

	for _, extra := range extraCRDs {
		testEnv.CRDDirectoryPaths = append(testEnv.CRDDirectoryPaths, filepath.Join(rootDirectory, extra))
	}

	_, err := testEnv.Start()
	if err != nil {
		t.Fatalf("Failed to start envtest: %v", err)
	}

	adminKubeconfig, adminRestConfig := createEnvtestKubeconfig(t, testEnv)
	if err != nil {
		t.Fatal(err)
	}

	client, err := ctrlruntimeclient.New(adminRestConfig, ctrlruntimeclient.Options{
		Scheme: newScheme(t),
	})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	cancelAndWait := func() {
		_ = testEnv.Stop()
	}

	t.Cleanup(cancelAndWait)

	return adminKubeconfig, client, cancelAndWait
}

func createEnvtestKubeconfig(t *testing.T, env *envtest.Environment) (string, *rest.Config) {
	adminInfo := envtest.User{Name: "admin", Groups: []string{"system:masters"}}

	adminUser, err := env.ControlPlane.AddUser(adminInfo, nil)
	if err != nil {
		t.Fatal(err)
	}

	adminKubeconfig, err := adminUser.KubeConfig()
	if err != nil {
		t.Fatal(err)
	}

	kubeconfigFile, err := os.CreateTemp(os.TempDir(), "kubeconfig*")
	if err != nil {
		t.Fatalf("Failed to create envtest kubeconfig file: %v", err)
	}
	defer kubeconfigFile.Close()

	if _, err := kubeconfigFile.Write(adminKubeconfig); err != nil {
		t.Fatalf("Failed to write envtest kubeconfig file: %v", err)
	}

	return kubeconfigFile.Name(), adminUser.Config()
}
