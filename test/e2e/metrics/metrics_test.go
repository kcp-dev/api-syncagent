//go:build e2e

/*
Copyright 2026 The KCP Authors.

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

package metrics

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"

	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"
	"github.com/kcp-dev/api-syncagent/test/utils"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrlruntime "sigs.k8s.io/controller-runtime"
)

func TestPublishedResourceMetric(t *testing.T) {
	const (
		apiExportName = "kcp.example.com"
	)

	metricsAddr := freeAddr(t)

	ctx := t.Context()
	ctrlruntime.SetLogger(logr.Discard())

	// setup a test environment in kcp
	orgKubeconfig := utils.CreateOrganization(t, ctx, "metrics-test", apiExportName)

	// start a service cluster
	envtestKubeconfig, envtestClient, _ := utils.RunEnvtest(t, []string{
		"test/crds/crontab.yaml",
		"test/crds/backup.yaml",
	})

	// publish CronTabs
	prCrontabs := &syncagentv1alpha1.PublishedResource{
		ObjectMeta: metav1.ObjectMeta{
			Name: "publish-crontabs",
		},
		Spec: syncagentv1alpha1.PublishedResourceSpec{
			Resource: syncagentv1alpha1.SourceResourceDescriptor{
				APIGroup: "example.com",
				Version:  "v1",
				Kind:     "CronTab",
			},
		},
	}

	if err := envtestClient.Create(ctx, prCrontabs); err != nil {
		t.Fatalf("Failed to create PublishedResource: %v", err)
	}

	// start agent with a known metrics address
	utils.RunAgentWithMetrics(ctx, t, "bob", orgKubeconfig, envtestKubeconfig, apiExportName, "", metricsAddr)

	// wait for the metric to appear and equal 1
	assertMetricValue(t, ctx, metricsAddr, "sync_agent_published_resource", 1)

	// add a second PublishedResource
	prBackups := &syncagentv1alpha1.PublishedResource{
		ObjectMeta: metav1.ObjectMeta{
			Name: "publish-backups",
		},
		Spec: syncagentv1alpha1.PublishedResourceSpec{
			Resource: syncagentv1alpha1.SourceResourceDescriptor{
				APIGroup: "eksempel.no",
				Version:  "v1",
				Kind:     "Backup",
			},
		},
	}

	if err := envtestClient.Create(ctx, prBackups); err != nil {
		t.Fatalf("Failed to create PublishedResource: %v", err)
	}

	// wait for metric to update to 2
	assertMetricValue(t, ctx, metricsAddr, "sync_agent_published_resource", 2)

	// delete first PublishedResource
	if err := envtestClient.Delete(ctx, prCrontabs); err != nil {
		t.Fatalf("Failed to delete PublishedResource: %v", err)
	}

	// wait for metric to drop back to 1
	assertMetricValue(t, ctx, metricsAddr, "sync_agent_published_resource", 1)
}

func assertMetricValue(t *testing.T, ctx context.Context, addr, metricName string, expected int) {
	t.Helper()

	url := fmt.Sprintf("http://%s/metrics", addr)
	expectedLine := fmt.Sprintf("%s %d", metricName, expected)

	err := wait.PollUntilContextTimeout(ctx, 1*time.Second, 1*time.Minute, false, func(ctx context.Context) (bool, error) {
		resp, err := http.Get(url) //nolint:gosec // test-only, fixed local address
		if err != nil {
			return false, nil // retry on connection errors
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return false, nil
		}

		for _, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(line, metricName+" ") {
				return line == expectedLine, nil
			}
		}

		return false, nil
	})
	if err != nil {
		t.Fatalf("Metric %s did not reach expected value %d within timeout: %v", metricName, expected, err)
	}

	t.Logf("✓ %s = %d", metricName, expected)
}

func freeAddr(t *testing.T) string {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to find free port: %v", err)
	}
	addr := l.Addr().String()
	l.Close()

	return addr
}
