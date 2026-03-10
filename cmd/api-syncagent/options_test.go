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
	"testing"
)

func TestURLRewriter(t *testing.T) {
	tests := []struct {
		name             string
		overrides        []string
		inputURL         string
		expectedURL      string
		expectParseError bool
	}{
		{
			name:        "no override configured",
			overrides:   []string{},
			inputURL:    "https://kcp.example.com:6443",
			expectedURL: "https://kcp.example.com:6443",
		},
		{
			name:        "single override host and port",
			overrides:   []string{"kcp.example.com:6443=localhost:8443"},
			inputURL:    "https://kcp.example.com:6443/clusters/root",
			expectedURL: "https://localhost:8443/clusters/root",
		},
		{
			name:        "override only affects matching host:port",
			overrides:   []string{"kcp.example.com:6443=localhost:8443"},
			inputURL:    "https://other.example.com:6443/clusters/root",
			expectedURL: "https://other.example.com:6443/clusters/root",
		},
		{
			name:        "override with different port",
			overrides:   []string{"kcp.example.com:443=localhost:30443"},
			inputURL:    "https://kcp.example.com:443",
			expectedURL: "https://localhost:30443",
		},
		{
			name: "multiple overrides applied in order",
			overrides: []string{
				"kcp.example.com:6443=kcp-proxy:6443",
				"kcp-proxy:6443=localhost:8443",
			},
			inputURL:    "https://kcp.example.com:6443/clusters/root",
			expectedURL: "https://localhost:8443/clusters/root",
		},
		{
			name: "multiple independent overrides",
			overrides: []string{
				"kcp1.example.com:6443=localhost:8443",
				"kcp2.example.com:6443=localhost:9443",
			},
			inputURL:    "https://kcp2.example.com:6443/api",
			expectedURL: "https://localhost:9443/api",
		},
		{
			name:        "override with query parameters",
			overrides:   []string{"kcp.example.com:6443=localhost:8443"},
			inputURL:    "https://kcp.example.com:6443/api?param=value",
			expectedURL: "https://localhost:8443/api?param=value",
		},
		{
			name:        "override with fragment",
			overrides:   []string{"kcp.example.com:6443=localhost:8443"},
			inputURL:    "https://kcp.example.com:6443/api#section",
			expectedURL: "https://localhost:8443/api#section",
		},
		{
			name:        "override with IPv6",
			overrides:   []string{"[::1]:6443=localhost:8443"},
			inputURL:    "https://[::1]:6443/clusters/root",
			expectedURL: "https://localhost:8443/clusters/root",
		},
		{
			name:        "override to IPv6",
			overrides:   []string{"kcp.example.com:6443=[2001:db8::1]:8443"},
			inputURL:    "https://kcp.example.com:6443/clusters/root",
			expectedURL: "https://[2001:db8::1]:8443/clusters/root",
		},
		{
			name:        "override preserves scheme",
			overrides:   []string{"kcp.example.com:6443=localhost:8443"},
			inputURL:    "http://kcp.example.com:6443/api",
			expectedURL: "http://localhost:8443/api",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := NewOptions()
			opts.APIExportHostPortOverrides = tt.overrides

			// Complete to parse the overrides.
			if err := opts.Complete(); err != nil {
				if !tt.expectParseError {
					t.Errorf("Complete() failed: %v", err)
				}
				return
			}

			if tt.expectParseError {
				t.Error("Expected Complete() to fail but it succeeded")
				return
			}

			rewriter := NewURLRewriter(opts)

			result, err := rewriter(tt.inputURL)
			if err != nil {
				t.Fatalf("URLRewriter() returned unexpected error: %v", err)
			}

			if result != tt.expectedURL {
				t.Errorf("Expected %q, but got %q", tt.expectedURL, result)
			}
		})
	}
}

func TestValidateHostPortOverride(t *testing.T) {
	tests := []struct {
		name        string
		override    string
		expectError bool
	}{
		{
			name:        "valid override",
			override:    "kcp.example.com:6443=localhost:8443",
			expectError: false,
		},
		{
			name:        "valid IPv6 override",
			override:    "[::1]:6443=[2001:db8::1]:8443",
			expectError: false,
		},
		{
			name:        "valid IPv6 to IPv4 override",
			override:    "[::1]:6443=localhost:8443",
			expectError: false,
		},
		{
			name:        "missing equals sign",
			override:    "kcp.example.com:6443",
			expectError: true,
		},
		{
			name:        "missing port on left side",
			override:    "kcp.example.com=localhost:8443",
			expectError: true,
		},
		{
			name:        "missing port on right side",
			override:    "kcp.example.com:6443=localhost",
			expectError: true,
		},
		{
			name:        "too many equals signs",
			override:    "kcp.example.com:6443=localhost:8443=extra",
			expectError: true,
		},
		{
			name:        "IPv6 without brackets",
			override:    "::1:6443=localhost:8443",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateHostPortOverride(tt.override)
			if (err != nil) != tt.expectError {
				t.Fatalf("returned error = %v, expectError %v", err, tt.expectError)
			}
		})
	}
}
