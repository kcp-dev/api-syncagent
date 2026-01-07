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

package kubeconfig

import (
	"errors"
	"strings"

	"k8s.io/client-go/rest"
)

func Validate(c *rest.Config) error {
	if c == nil {
		return errors.New("no kubeconfig was found. Have you set current-context?")
	}

	if !strings.Contains(c.Host, "/clusters/") {
		return errors.New("kcp kubeconfig does not point to a specific workspace")
	}

	return nil
}
