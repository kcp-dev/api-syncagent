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

package transformer

import (
	"fmt"

	"github.com/tidwall/sjson"

	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"
)

type deleteTransformer struct {
	mut *syncagentv1alpha1.ResourceDeleteMutation
}

func NewDelete(mut *syncagentv1alpha1.ResourceDeleteMutation) (*deleteTransformer, error) {
	return &deleteTransformer{
		mut: mut,
	}, nil
}

func (m *deleteTransformer) ApplyJSON(toMutate string, _ string) (string, error) {
	jsonData, err := sjson.Delete(toMutate, m.mut.Path)
	if err != nil {
		return "", fmt.Errorf("failed to delete value @ %s: %w", m.mut.Path, err)
	}

	return jsonData, nil
}
