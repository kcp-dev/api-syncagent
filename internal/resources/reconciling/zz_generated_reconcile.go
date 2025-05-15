/*
Copyright The KCP Authors.

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

package reconciling

import (
	"context"
	"fmt"

	"k8c.io/reconciler/pkg/reconciling"
	"k8s.io/apimachinery/pkg/types"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"

	kcpdevv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

// APIExportReconciler defines an interface to create/update APIExports.
type APIExportReconciler = func(existing *kcpdevv1alpha1.APIExport) (*kcpdevv1alpha1.APIExport, error)

// NamedAPIExportReconcilerFactory returns the name of the resource and the corresponding Reconciler function.
type NamedAPIExportReconcilerFactory = func() (name string, reconciler APIExportReconciler)

// APIExportObjectWrapper adds a wrapper so the APIExportReconciler matches ObjectReconciler.
// This is needed as Go does not support function interface matching.
func APIExportObjectWrapper(reconciler APIExportReconciler) reconciling.ObjectReconciler {
	return func(existing ctrlruntimeclient.Object) (ctrlruntimeclient.Object, error) {
		if existing != nil {
			return reconciler(existing.(*kcpdevv1alpha1.APIExport))
		}
		return reconciler(&kcpdevv1alpha1.APIExport{})
	}
}

// ReconcileAPIExports will create and update the APIExports coming from the passed APIExportReconciler slice.
func ReconcileAPIExports(ctx context.Context, namedFactories []NamedAPIExportReconcilerFactory, namespace string, client ctrlruntimeclient.Client, objectModifiers ...reconciling.ObjectModifier) error {
	for _, factory := range namedFactories {
		name, reconciler := factory()
		reconcileObject := APIExportObjectWrapper(reconciler)
		reconcileObject = reconciling.CreateWithNamespace(reconcileObject, namespace)
		reconcileObject = reconciling.CreateWithName(reconcileObject, name)

		for _, objectModifier := range objectModifiers {
			reconcileObject = objectModifier(reconcileObject)
		}

		if err := reconciling.EnsureNamedObject(ctx, types.NamespacedName{Namespace: namespace, Name: name}, reconcileObject, client, &kcpdevv1alpha1.APIExport{}, false); err != nil {
			return fmt.Errorf("failed to ensure APIExport %s/%s: %w", namespace, name, err)
		}
	}

	return nil
}

// APIResourceSchemaReconciler defines an interface to create/update APIResourceSchemas.
type APIResourceSchemaReconciler = func(existing *kcpdevv1alpha1.APIResourceSchema) (*kcpdevv1alpha1.APIResourceSchema, error)

// NamedAPIResourceSchemaReconcilerFactory returns the name of the resource and the corresponding Reconciler function.
type NamedAPIResourceSchemaReconcilerFactory = func() (name string, reconciler APIResourceSchemaReconciler)

// APIResourceSchemaObjectWrapper adds a wrapper so the APIResourceSchemaReconciler matches ObjectReconciler.
// This is needed as Go does not support function interface matching.
func APIResourceSchemaObjectWrapper(reconciler APIResourceSchemaReconciler) reconciling.ObjectReconciler {
	return func(existing ctrlruntimeclient.Object) (ctrlruntimeclient.Object, error) {
		if existing != nil {
			return reconciler(existing.(*kcpdevv1alpha1.APIResourceSchema))
		}
		return reconciler(&kcpdevv1alpha1.APIResourceSchema{})
	}
}

// ReconcileAPIResourceSchemas will create and update the APIResourceSchemas coming from the passed APIResourceSchemaReconciler slice.
func ReconcileAPIResourceSchemas(ctx context.Context, namedFactories []NamedAPIResourceSchemaReconcilerFactory, namespace string, client ctrlruntimeclient.Client, objectModifiers ...reconciling.ObjectModifier) error {
	for _, factory := range namedFactories {
		name, reconciler := factory()
		reconcileObject := APIResourceSchemaObjectWrapper(reconciler)
		reconcileObject = reconciling.CreateWithNamespace(reconcileObject, namespace)
		reconcileObject = reconciling.CreateWithName(reconcileObject, name)

		for _, objectModifier := range objectModifiers {
			reconcileObject = objectModifier(reconcileObject)
		}

		if err := reconciling.EnsureNamedObject(ctx, types.NamespacedName{Namespace: namespace, Name: name}, reconcileObject, client, &kcpdevv1alpha1.APIResourceSchema{}, false); err != nil {
			return fmt.Errorf("failed to ensure APIResourceSchema %s/%s: %w", namespace, name, err)
		}
	}

	return nil
}
