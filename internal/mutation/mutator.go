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

package mutation

import (
	"errors"
	"fmt"

	"github.com/kcp-dev/api-syncagent/internal/mutation/transformer"
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type Mutator interface {
	// MutateSpec transform a remote object into a local one. On the first
	// mutation, otherObj will be nil. MutateSpec can modify all fields
	// except the status (i.e. "mutate spec" here means to mutate expected state,
	// which can be more than just the spec).
	MutateSpec(toMutate *unstructured.Unstructured, otherObj *unstructured.Unstructured) (*unstructured.Unstructured, error)
	// MutateStatus transform a local object into a remote one. MutateStatus
	// must only modify the status field.
	MutateStatus(toMutate *unstructured.Unstructured, otherObj *unstructured.Unstructured) (*unstructured.Unstructured, error)
}

type mutator struct {
	spec   *transformer.AggregateTransformer
	status *transformer.AggregateTransformer
}

var _ Mutator = &mutator{}

// NewMutator creates a new mutator, which will apply the mutation rules to a synced object, in
// both directions. A nil spec is supported and will simply make the mutator not do anything.
func NewMutator(spec *syncagentv1alpha1.ResourceMutationSpec) (Mutator, error) {
	if spec == nil {
		return nil, nil
	}

	specAgg, err := createAggregatedTransformer(spec.Spec)
	if err != nil {
		return nil, fmt.Errorf("cannot create transformer for spec: %w", err)
	}

	statusAgg, err := createAggregatedTransformer(spec.Status)
	if err != nil {
		return nil, fmt.Errorf("cannot create transformer for status: %w", err)
	}

	return &mutator{
		spec:   specAgg,
		status: statusAgg,
	}, nil
}

func (m *mutator) MutateSpec(toMutate *unstructured.Unstructured, otherObj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	if m == nil {
		return toMutate, nil
	}

	return m.spec.Apply(toMutate, otherObj)
}

func (m *mutator) MutateStatus(toMutate *unstructured.Unstructured, otherObj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	if m == nil {
		return toMutate, nil
	}

	return m.status.Apply(toMutate, otherObj)
}

func createAggregatedTransformer(mutations []syncagentv1alpha1.ResourceMutation) (*transformer.AggregateTransformer, error) {
	agg := transformer.NewAggregate()

	for _, mut := range mutations {
		var (
			trans any
			err   error
		)

		switch {
		case mut.Delete != nil:
			trans, err = transformer.NewDelete(mut.Delete)
			if err != nil {
				return nil, err
			}

		case mut.Regex != nil:
			trans, err = transformer.NewRegex(mut.Regex)
			if err != nil {
				return nil, err
			}

		case mut.Template != nil:
			trans, err = transformer.NewTemplate(mut.Template)
			if err != nil {
				return nil, err
			}

		default:
			return nil, errors.New("no valid mutation mechanism provided")
		}

		agg.Add(trans)
	}

	return agg, nil
}
