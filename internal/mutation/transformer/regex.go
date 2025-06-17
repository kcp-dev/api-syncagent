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
	"regexp"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"
)

type regexTransformer struct {
	mut  *syncagentv1alpha1.ResourceRegexMutation
	expr *regexp.Regexp
}

func NewRegex(mut *syncagentv1alpha1.ResourceRegexMutation) (*regexTransformer, error) {
	var (
		expr *regexp.Regexp
		err  error
	)

	if mut.Pattern != "" {
		expr, err = regexp.Compile(mut.Pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid pattern %q: %w", mut.Pattern, err)
		}
	}

	return &regexTransformer{
		mut:  mut,
		expr: expr,
	}, nil
}

func (m *regexTransformer) ApplyJSON(toMutate string, _ string) (string, error) {
	if m.expr == nil {
		return sjson.Set(toMutate, m.mut.Path, m.mut.Replacement)
	}

	// this does apply some coalescing, like turning numbers into strings
	strVal := gjson.Get(toMutate, m.mut.Path).String()
	replacement := m.expr.ReplaceAllString(strVal, m.mut.Replacement)

	return sjson.Set(toMutate, m.mut.Path, replacement)
}
