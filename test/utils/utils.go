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
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	kcpapisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	kcptenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	mcclient "github.com/kcp-dev/multicluster-provider/client"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer/yaml"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/scale/scheme"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func GetKcpAdminKubeconfig(t *testing.T) string {
	return requiredEnv(t, "KCP_KUBECONFIG")
}

func must(t *testing.T, err error) {
	t.Helper()

	if err != nil {
		t.Fatal(err)
	}
}

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	sc := runtime.NewScheme()
	must(t, scheme.AddToScheme(sc))
	must(t, corev1.AddToScheme(sc))
	must(t, rbacv1.AddToScheme(sc))
	must(t, kcptenancyv1alpha1.AddToScheme(sc))
	must(t, kcpapisv1alpha1.AddToScheme(sc))
	must(t, syncagentv1alpha1.AddToScheme(sc))
	must(t, apiextensionsv1.AddToScheme(sc))

	return sc
}

var clusterPathSuffix = regexp.MustCompile(`/clusters/[a-z0-9:*]+$`)

func GetKcpAdminClusterClient(t *testing.T) mcclient.ClusterClient {
	t.Helper()
	return GetClusterClient(t, GetKcpAdminKubeconfig(t))
}

func GetClusterClient(t *testing.T, kubeconfig string) mcclient.ClusterClient {
	t.Helper()

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatalf("Failed to get load kubeconfig %q: %v", kubeconfig, err)
	}

	// remove any pre-existing /clusters/... suffix, a cluster-aware client needs
	// to point to the base URL (either of kcp or a virtual workspace)
	config.Host = clusterPathSuffix.ReplaceAllLiteralString(config.Host, "")

	client, err := mcclient.New(config, ctrlruntimeclient.Options{
		Scheme: newScheme(t),
	})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	return client
}

func GetKcpAdminClient(t *testing.T) ctrlruntimeclient.Client {
	t.Helper()
	return GetClient(t, GetKcpAdminKubeconfig(t))
}

func GetClient(t *testing.T, kubeconfig string) ctrlruntimeclient.Client {
	t.Helper()

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatalf("Failed to get load kubeconfig %q: %v", kubeconfig, err)
	}

	client, err := ctrlruntimeclient.New(config, ctrlruntimeclient.Options{
		Scheme: newScheme(t),
	})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	return client
}

func CreateKcpAgentKubeconfig(t *testing.T, path string) string {
	t.Helper()

	agentToken := requiredEnv(t, "KCP_AGENT_TOKEN")

	kubeconfig, err := clientcmd.LoadFromFile(GetKcpAdminKubeconfig(t))
	if err != nil {
		t.Fatalf("Failed to load admin kcp kubeconfig: %v", err)
	}

	// drop everything but the currently selected context
	if err := clientcmdapi.MinifyConfig(kubeconfig); err != nil {
		t.Fatalf("Failed to minify admin kcp kubeconfig: %v", err)
	}

	// update server URL if desired
	if path != "" {
		for name, cluster := range kubeconfig.Clusters {
			parsed, err := url.Parse(cluster.Server)
			if err != nil {
				// Given how ultra lax url.Parse is, this basically never happens.
				t.Fatalf("Failed to parse %q as URL: %v", cluster.Server, err)
			}

			kubeconfig.Clusters[name].Server = fmt.Sprintf("%s://%s%s", parsed.Scheme, parsed.Host, path)
		}
	}

	// use the agent's token
	for name := range kubeconfig.AuthInfos {
		kubeconfig.AuthInfos[name].Token = agentToken
	}

	// write the kubeconfig to a temporary file
	encodedKubeconfig, err := clientcmd.Write(*kubeconfig)
	if err != nil {
		t.Fatalf("Failed to encode agent kubeconfig: %v", err)
	}

	kubeconfigFile, err := os.CreateTemp(os.TempDir(), "kubeconfig*")
	if err != nil {
		t.Fatalf("Failed to create agent kubeconfig file: %v", err)
	}
	defer kubeconfigFile.Close()

	if _, err := kubeconfigFile.Write(encodedKubeconfig); err != nil {
		t.Fatalf("Failed to write agent kubeconfig file: %v", err)
	}

	// ensure the kubeconfig is removed after the test
	t.Cleanup(func() {
		os.Remove(kubeconfigFile.Name())
	})

	return kubeconfigFile.Name()
}

func ToUnstructured(t *testing.T, obj any) *unstructured.Unstructured {
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		t.Fatalf("Failed to convert object to unstructurd: %v", err)
	}

	return &unstructured.Unstructured{Object: raw}
}

func YAMLToUnstructured(t *testing.T, data string) *unstructured.Unstructured {
	t.Helper()

	decoder := yamlutil.NewYAMLOrJSONDecoder(strings.NewReader(data), 100)

	var rawObj runtime.RawExtension
	if err := decoder.Decode(&rawObj); err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}

	obj, _, err := yaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme).Decode(rawObj.Raw, nil, nil)
	if err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}

	return ToUnstructured(t, obj)
}

func KCPMinor() int {
	version := os.Getenv("KCP_VERSION")
	if version == "" {
		panic("No $KCP_VERSION environment variable defined.")
	}

	parts := strings.SplitN(version, ".", 3)
	if len(parts) != 3 {
		panic("Invalid $KCP_VERSION, must be X.Y.Z.")
	}

	minor, err := strconv.ParseInt(parts[1], 10, 32)
	if err != nil {
		panic(fmt.Sprintf("Invalid $KCP_VERSION: not parseable: %v", err))
	}

	return int(minor)
}
