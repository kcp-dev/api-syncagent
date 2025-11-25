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

package validation

import (
	"errors"
	"fmt"

	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"
)

func ValidatePublishedResource(pr *syncagentv1alpha1.PublishedResource) error {
	if err := ValidatePublishedResourceSpec(pr.Spec); err != nil {
		return fmt.Errorf("invalid spec: %w", err)
	}

	return nil
}

func ValidatePublishedResourceSpec(spec syncagentv1alpha1.PublishedResourceSpec) error {
	for _, rr := range spec.Related {
		if err := ValidateRelatedResource(rr); err != nil {
			return fmt.Errorf("invalid related resource: %w", err)
		}
	}

	return nil
}

func ValidateRelatedResource(rr syncagentv1alpha1.RelatedResourceSpec) error {
	//nolint:staticcheck
	if rr.Kind != "" && rr.Resource != "" {
		return errors.New("resource and kind are mutually exclusive")
	}

	//nolint:staticcheck
	if rr.Kind == "" && rr.Resource == "" {
		return errors.New("must specify either Resource or Kind")
	}

	//nolint:staticcheck
	if kind := rr.Kind; kind != "" {
		if kind != "Secret" && kind != "ConfigMap" {
			return errors.New("legacy kind must be either ConfigMap or Secret, use references for anything else")
		}
	}

	return nil
}
