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

package main

import (
	"testing"
	"time"

	"github.com/spf13/pflag"
)

func TestCacheSyncTimeoutFlag(t *testing.T) {
	testcases := []struct {
		name    string
		args    []string
		want    time.Duration
		wantErr bool
	}{
		{
			name: "default",
			want: 2 * time.Minute,
		},
		{
			name: "custom value",
			args: []string{"--cache-sync-timeout=10m"},
			want: 10 * time.Minute,
		},
		{
			name:    "zero is rejected",
			args:    []string{"--cache-sync-timeout=0s"},
			wantErr: true,
		},
		{
			name:    "negative is rejected",
			args:    []string{"--cache-sync-timeout=-1m"},
			wantErr: true,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			opts := NewOptions()
			opts.Namespace = "kube-system"
			opts.APIExportEndpointSliceRef = "example"
			opts.KcpKubeconfig = "kcp.kubeconfig"

			flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
			opts.AddFlags(flags)

			if err := flags.Parse(tc.args); err != nil {
				t.Fatalf("Failed to parse flags: %v", err)
			}

			err := opts.Validate()
			if tc.wantErr {
				if err == nil {
					t.Fatal("Expected validation error, but got none.")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected validation error: %v", err)
			}

			if opts.CacheSyncTimeout != tc.want {
				t.Fatalf("Expected cache sync timeout %v, got %v.", tc.want, opts.CacheSyncTimeout)
			}
		})
	}
}
