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

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/decls"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type celTransformer struct {
	path string
	prg  cel.Program
}

func NewCEL(mut *syncagentv1alpha1.ResourceCELMutation) (*celTransformer, error) {
	env, err := cel.NewEnv(cel.VariableDecls(
		decls.NewVariable("self", cel.DynType),
		decls.NewVariable("other", cel.DynType),
		decls.NewVariable("value", cel.DynType),
	))
	if err != nil {
		return nil, fmt.Errorf("failed to create CEL env: %w", err)
	}

	expr, issues := env.Compile(mut.Expression)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("failed to compile CEL expression: %w", issues.Err())
	}

	prg, err := env.Program(expr)
	if err != nil {
		return nil, fmt.Errorf("failed to create CEL program: %w", err)
	}

	return &celTransformer{
		path: mut.Path,
		prg:  prg,
	}, nil
}

func (m *celTransformer) Apply(toMutate *unstructured.Unstructured, otherObj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	encoded, err := EncodeObject(toMutate)
	if err != nil {
		return nil, fmt.Errorf("failed to JSON encode object: %w", err)
	}

	// get the current value at the path
	current := gjson.Get(encoded, m.path)

	input := map[string]any{
		"value": current.Value(),
		"self":  toMutate.Object,
		"other": nil,
	}
	if otherObj != nil {
		input["other"] = otherObj.Object
	}

	// evaluate the expression
	out, _, err := m.prg.Eval(input)
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate CEL expression: %w", err)
	}

	// update the object
	updated, err := sjson.Set(encoded, m.path, out)
	if err != nil {
		return nil, fmt.Errorf("failed to set updated value: %w", err)
	}

	return DecodeObject(updated)
}
