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

package apiexport

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"

	"github.com/kcp-dev/api-syncagent/internal/metrics"
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	kcpapisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/record"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	fakectrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const testAPIExportName = "test-export"
const testAgentName = "test-agent"

func TestReconcileSetsPublishedResourceMetric(t *testing.T) {
	tests := []struct {
		name           string
		pubResources   []ctrlruntimeclient.Object
		expectedMetric float64
	}{
		{
			name:           "no PublishedResources",
			pubResources:   nil,
			expectedMetric: 0,
		},
		{
			name: "single PublishedResource",
			pubResources: []ctrlruntimeclient.Object{
				newPublishedResource("test-pr-1", "v1.widgets.example.com"),
			},
			expectedMetric: 1,
		},
		{
			name: "multiple PublishedResources",
			pubResources: []ctrlruntimeclient.Object{
				newPublishedResource("test-pr-1", "v1.widgets.example.com"),
				newPublishedResource("test-pr-2", "v1.gadgets.example.com"),
				newPublishedResource("test-pr-3", "v1.things.example.com"),
			},
			expectedMetric: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			utilruntime.Must(syncagentv1alpha1.AddToScheme(scheme))
			utilruntime.Must(kcpapisv1alpha1.AddToScheme(scheme))

			localClient := fakectrlruntimeclient.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.pubResources...).
				Build()

			apiExport := &kcpapisv1alpha1.APIExport{
				ObjectMeta: metav1.ObjectMeta{
					Name: testAPIExportName,
				},
			}
			kcpClient := fakectrlruntimeclient.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(apiExport).
				WithStatusSubresource(apiExport).
				Build()

			r := &Reconciler{
				localClient:   localClient,
				kcpClient:     kcpClient,
				log:           zap.NewNop().Sugar(),
				recorder:      record.NewFakeRecorder(99),
				apiExportName: testAPIExportName,
				agentName:     testAgentName,
				prFilter:      labels.Everything(), // the same filter we use by default in the controller
			}

			_, err := r.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: types.NamespacedName{Name: testAPIExportName},
			})
			if err != nil {
				t.Fatalf("unexpected reconcile error: %v", err)
			}

			got := testutil.ToFloat64(metrics.PublishedResourcesManaged)
			if got != tt.expectedMetric {
				t.Errorf("expected metric value %v, got %v", tt.expectedMetric, got)
			}
		})
	}
}

func newPublishedResource(name, schemaName string) *syncagentv1alpha1.PublishedResource {
	return &syncagentv1alpha1.PublishedResource{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: syncagentv1alpha1.PublishedResourceSpec{
			Resource: syncagentv1alpha1.SourceResourceDescriptor{
				APIGroup: "example.com",
				Version:  "v1",
				Kind:     name,
			},
		},
		Status: syncagentv1alpha1.PublishedResourceStatus{
			ResourceSchemaName: schemaName,
		},
	}
}
