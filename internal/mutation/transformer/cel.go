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
	"reflect"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/decls"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
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

	// convert the result to its native go representation
	value, err := celToNative(out)
	if err != nil {
		return nil, fmt.Errorf("failed to convert CEL result to native value: %w", err)
	}

	// update the object
	updated, err := sjson.Set(encoded, m.path, value)
	if err != nil {
		return nil, fmt.Errorf("failed to set updated value: %w", err)
	}

	return DecodeObject(updated)
}

// celToNative recursively converts a given value to its native Go
// representation according to the reflected type description, or error if the
// conversion is not feasible.
func celToNative(value ref.Val) (any, error) {
	switch value.Type() {
	case types.ListType:
		l, err := value.ConvertToNative(reflect.TypeFor[[]ref.Val]())
		if err != nil {
			return nil, err
		}
		list := l.([]ref.Val)

		result := make([]any, len(list))
		for i, item := range list {
			result[i], err = celToNative(item)
			if err != nil {
				return nil, err
			}
		}
		return result, nil

	case types.MapType:
		m, err := value.ConvertToNative(reflect.TypeFor[map[ref.Val]ref.Val]())
		if err != nil {
			return nil, err
		}
		mmap := m.(map[ref.Val]ref.Val)

		result := make(map[string]any, len(mmap))
		for key, item := range mmap {
			k, err := key.ConvertToNative(reflect.TypeFor[string]())
			if err != nil {
				return nil, err
			}

			v, err := celToNative(item)
			if err != nil {
				return nil, err
			}

			result[k.(string)] = v
		}
		return result, nil

	default:
		return value.ConvertToNative(reflect.TypeFor[any]())
	}
}
