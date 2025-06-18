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

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type objectTransformer interface {
	Apply(toMutate *unstructured.Unstructured, otherObj *unstructured.Unstructured) (*unstructured.Unstructured, error)
}

type jsonTransformer interface {
	ApplyJSON(toMutate string, otherObj string) (string, error)
}

// AggregateTransformer calls multiple other aggregates in sequence. A nil AggregateTransformer
// is supported and will simply not do anything.
type AggregateTransformer struct {
	transformers []any
}

func NewAggregate() *AggregateTransformer {
	return &AggregateTransformer{
		transformers: []any{},
	}
}

func (m *AggregateTransformer) Add(transformer any) {
	m.transformers = append(m.transformers, transformer)
}

func (m *AggregateTransformer) Apply(toMutate *unstructured.Unstructured, otherObj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	if m == nil {
		return toMutate, nil
	}

	for _, transformer := range m.transformers {
		switch asserted := transformer.(type) {
		case objectTransformer:
			var err error
			toMutate, err = asserted.Apply(toMutate, otherObj)
			if err != nil {
				return nil, err
			}

		case jsonTransformer:
			encodedToMutate, err := EncodeObject(toMutate)
			if err != nil {
				return nil, fmt.Errorf("failed to JSON encode object: %w", err)
			}

			encodedOtherObj, err := EncodeObject(otherObj)
			if err != nil {
				return nil, fmt.Errorf("failed to JSON encode object: %w", err)
			}

			mutated, err := asserted.ApplyJSON(encodedToMutate, encodedOtherObj)
			if err != nil {
				return nil, err
			}

			toMutate, err = DecodeObject(mutated)
			if err != nil {
				return nil, err
			}
		}
	}

	return toMutate, nil
}
