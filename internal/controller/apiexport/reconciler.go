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

package apiexport

import (
	"fmt"
	"slices"
	"strings"

	"github.com/kcp-dev/api-syncagent/internal/resources/reconciling"
	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	kcpdevv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
)

// createAPIExportReconciler creates the reconciler for the APIExport.
// WARNING: The APIExport in this is NOT created by the Sync Agent, it's created
// by a controller in kcp. Make sure you don't create a reconciling conflict!
func (r *Reconciler) createAPIExportReconciler(
	availableResourceSchemas sets.Set[string],
	claimedResourceKinds sets.Set[kcpdevv1alpha1.GroupResource],
	agentName string,
	apiExportName string,
	recorder record.EventRecorder,
) reconciling.NamedAPIExportReconcilerFactory {
	return func() (string, reconciling.APIExportReconciler) {
		return apiExportName, func(existing *kcpdevv1alpha1.APIExport) (*kcpdevv1alpha1.APIExport, error) {
			if existing.Annotations == nil {
				existing.Annotations = map[string]string{}
			}
			existing.Annotations[syncagentv1alpha1.AgentNameAnnotation] = agentName

			// combine existing schemas with new ones
			newSchemas := mergeResourceSchemas(existing.Spec.LatestResourceSchemas, availableResourceSchemas)
			createSchemaEvents(existing, existing.Spec.LatestResourceSchemas, newSchemas, recorder)

			existing.Spec.LatestResourceSchemas = newSchemas

			// To allow admins to configure additional permission claims, sometimes
			// useful for debugging, we do not override the permission claims, but
			// only ensure the ones originating from the published resources;
			// step 1 is to collect all existing claims with the same properties
			// as ours.
			existingClaims := sets.New[kcpdevv1alpha1.GroupResource]()
			for _, claim := range existing.Spec.PermissionClaims {
				if claim.All && len(claim.ResourceSelector) == 0 {
					existingClaims.Insert(claim.GroupResource)
				}
			}

			missingClaims := claimedResourceKinds.Difference(existingClaims)

			claimsToAdd := missingClaims.UnsortedList()
			slices.SortStableFunc(claimsToAdd, func(a, b kcpdevv1alpha1.GroupResource) int {
				if a.Group != b.Group {
					return strings.Compare(a.Group, b.Group)
				}

				return strings.Compare(a.Resource, b.Resource)
			})

			// add our missing claims
			for _, claimed := range claimsToAdd {
				existing.Spec.PermissionClaims = append(existing.Spec.PermissionClaims, kcpdevv1alpha1.PermissionClaim{
					GroupResource: claimed,
					All:           true,
				})
			}

			if missingClaims.Len() > 0 {
				claims := make([]string, 0, len(claimsToAdd))
				for _, claimed := range claimsToAdd {
					claims = append(claims, groupResourceToString(claimed))
				}
				recorder.Eventf(existing, corev1.EventTypeNormal, "AddingPermissionClaims", "Added new permission claim(s) for all %s.", strings.Join(claims, ", "))
			}

			// prevent reconcile loops by ensuring a stable order
			slices.SortFunc(existing.Spec.PermissionClaims, func(a, b kcpdevv1alpha1.PermissionClaim) int {
				if a.Group != b.Group {
					return strings.Compare(a.Group, b.Group)
				}

				if a.Resource != b.Resource {
					return strings.Compare(a.Resource, b.Resource)
				}

				return 0
			})

			return existing, nil
		}
	}
}

func groupResourceToString(gr kcpdevv1alpha1.GroupResource) string {
	if gr.Group == "" {
		return gr.Resource
	}

	return fmt.Sprintf("%s/%s", gr.Group, gr.Resource)
}

func mergeResourceSchemas(existing []string, configured sets.Set[string]) []string {
	var result []string

	// first we copy all ARS that are coming from the PublishedResources
	knownResources := sets.New[string]()
	for _, schema := range configured.UnsortedList() {
		result = append(result, schema)
		knownResources.Insert(parseResourceGroup(schema))
	}

	// Now we include all other existing ARS that use unknown resources;
	// this both allows an APIExport to contain "unmanaged" ARS, and also
	// will purposefully leave behind ARS for deleted PublishedResources,
	// allowing cleanup to take place outside of the agent's control.
	for _, schema := range existing {
		if !knownResources.Has(parseResourceGroup(schema)) {
			result = append(result, schema)
		}
	}

	// for stability and beauty, sort the schemas
	slices.SortFunc(result, func(a, b string) int {
		return strings.Compare(parseResourceGroup(a), parseResourceGroup(b))
	})

	return result
}

func createSchemaEvents(obj runtime.Object, oldSchemas, newSchemas []string, recorder record.EventRecorder) {
	oldSet := sets.New(oldSchemas...)
	newSet := sets.New(newSchemas...)

	if change := sets.List(newSet.Difference(oldSet)); len(change) > 0 {
		recorder.Eventf(obj, corev1.EventTypeNormal, "AddingResourceSchemas", "Added new resource schema(s) %s.", strings.Join(change, ", "))
	}

	if change := sets.List(oldSet.Difference(newSet)); len(change) > 0 {
		recorder.Eventf(obj, corev1.EventTypeWarning, "RemovingResourceSchemas", "Removed resource schema(s) %s.", strings.Join(change, ", "))
	}
}

func parseResourceGroup(schema string) string {
	// <version>.<resource>.<group>
	parts := strings.SplitN(schema, ".", 2)

	return parts[1]
}
