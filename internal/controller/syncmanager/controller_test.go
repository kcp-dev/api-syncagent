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

package syncmanager

import (
	"context"
	"errors"
	"testing"
)

func TestStartAndRecord(t *testing.T) {
	r := &Reconciler{syncCancels: map[string]context.CancelCauseFunc{}}

	ctx, cancel := context.WithCancelCause(context.Background())

	if err := r.startAndRecord("key", cancel, func() error { return errors.New("boom") }); err == nil {
		t.Fatal("expected an error")
	}

	if _, exists := r.syncCancels["key"]; exists {
		t.Error("cancel function must not be recorded after a failed start")
	}

	if ctx.Err() == nil {
		t.Error("context should be cancelled after a failed start")
	}

	// a retry must be able to succeed
	_, cancel = context.WithCancelCause(context.Background())

	if err := r.startAndRecord("key", cancel, func() error { return nil }); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, exists := r.syncCancels["key"]; !exists {
		t.Error("cancel function should be recorded after a successful start")
	}
}
