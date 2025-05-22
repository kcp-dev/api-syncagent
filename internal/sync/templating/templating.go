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

package templating

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strings"
	"text/template"

	"crypto/sha3"
	"github.com/Masterminds/sprig/v3"

	"github.com/kcp-dev/api-syncagent/internal/crypto"
)

func Render(tpl string, data any) (string, error) {
	parsed, err := template.New("inline").Funcs(templateFuncMap()).Parse(tpl)
	if err != nil {
		return "", fmt.Errorf("failed to parse: %w", err)
	}

	var buf bytes.Buffer
	if err := parsed.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to evaluate: %w", err)
	}

	return strings.TrimSpace(buf.String()), nil
}

func templateFuncMap() template.FuncMap {
	funcs := sprig.TxtFuncMap()
	funcs["join"] = strings.Join
	funcs["sha3sum"] = sha3sum
	funcs["sha3short"] = sha3short

	// shortHash is included for backwards compatibility with the old naming rules,
	// new installations should not use it because it relies on SHA-1. Instead use
	// sha3short.
	funcs["shortHash"] = crypto.ShortHash

	return funcs
}

func sha3sum(input string) string {
	hash := sha3.Sum256([]byte(input))
	return hex.EncodeToString(hash[:])
}

// sha3short supports exactly 1 optional length argument. If not given, length defaults to 20.
func sha3short(input string, lengths ...int) (string, error) {
	var length int
	switch len(lengths) {
	case 0:
		length = 20
	case 1:
		length = lengths[0]
	default:
		return "", fmt.Errorf("sha3short: expected at most one length argument, got %d", len(lengths))
	}

	if length <= 0 {
		return "", fmt.Errorf("sha3short: invalid length %d", length)
	}

	hash := sha3sum(input)

	if length > len(hash) {
		length = len(hash)
	}

	return hash[:length], nil
}
