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
	"bytes"
	"fmt"
	"html/template"
	"strings"

	"github.com/Masterminds/sprig/v3"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	syncagentv1alpha1 "github.com/kcp-dev/api-syncagent/sdk/apis/syncagent/v1alpha1"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type templateTransformer struct {
	path string
	tpl  *template.Template
}

func NewTemplate(mut *syncagentv1alpha1.ResourceTemplateMutation) (*templateTransformer, error) {
	tpl, err := template.New("mutation").Funcs(sprig.TxtFuncMap()).Parse(mut.Template)
	if err != nil {
		return nil, fmt.Errorf("failed to parse template %q: %w", mut.Template, err)
	}

	return &templateTransformer{
		path: mut.Path,
		tpl:  tpl,
	}, nil
}

type templateMutationContext struct {
	// Value is always set by this package to the value found in the document.
	Value gjson.Result

	LocalObject  map[string]any
	RemoteObject map[string]any
}

func (m *templateTransformer) Apply(toMutate *unstructured.Unstructured, otherObj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	encoded, err := EncodeObject(toMutate)
	if err != nil {
		return nil, fmt.Errorf("failed to JSON encode object: %w", err)
	}

	ctx := templateMutationContext{
		Value:       gjson.Get(encoded, m.path),
		LocalObject: toMutate.Object,
	}

	if otherObj != nil {
		ctx.RemoteObject = otherObj.Object
	}

	var buf bytes.Buffer
	if err := m.tpl.Execute(&buf, ctx); err != nil {
		return nil, fmt.Errorf("failed to execute template: %w", err)
	}

	replacement := strings.TrimSpace(buf.String())

	updated, err := sjson.Set(encoded, m.path, replacement)
	if err != nil {
		return nil, fmt.Errorf("failed to set updated value: %w", err)
	}

	return DecodeObject(updated)
}
