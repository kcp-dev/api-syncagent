#!/usr/bin/env bash

# Copyright 2025 The KCP Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

cd "$(dirname $0)/.."

source hack/lib.sh

# get kube envtest binaries
echodate "Setting up Kube binaries…"
ENVTEST="$(UGET_PRINT_PATH=relative make --no-print-directory install-envtest)"
export KUBEBUILDER_ASSETS="$($ENVTEST use 1.31.0 --bin-dir $UGET_DIRECTORY -p path)"
KUBEBUILDER_ASSETS="$(realpath "$KUBEBUILDER_ASSETS")"

export ARTIFACTS=.e2e

rm -rf "$ARTIFACTS"
mkdir -p "$ARTIFACTS"

KCP_ROOT_DIRECTORY="$ARTIFACTS/kcp"
KCP_LOGFILE="$ARTIFACTS/kcp.log"
KCP_TOKENFILE=hack/ci/testdata/e2e-kcp.tokens
KCP_PID=0

echodate "Starting kcp…"
KCP="$(UGET_PRINT_PATH=relative make --no-print-directory install-kcp)"

rm -rf "$KCP_ROOT_DIRECTORY" "$KCP_LOGFILE"
"$KCP" start \
  -v4 \
  --token-auth-file "$KCP_TOKENFILE" \
  --root-directory "$KCP_ROOT_DIRECTORY" 1>"$KCP_LOGFILE" 2>&1 &
KCP_PID=$!

stop_kcp() {
  echodate "Stopping kcp (set \$KEEP_KCP=true to not do this)…"
  kill -TERM $KCP_PID
  wait $KCP_PID
}

if [[ -v KEEP_KCP ]] && $KEEP_KCP; then
  echodate "\$KEEP_KCP is set, will not stop kcp once the script is finished."
else
  append_trap stop_kcp EXIT
fi

# make the token available to the Go tests
export KCP_AGENT_TOKEN="$(grep e2e "$KCP_TOKENFILE" | cut -f1 -d,)"

# Wait for kcp to be ready; this env name is also hardcoded in the Go tests.
export KCP_KUBECONFIG="$KCP_ROOT_DIRECTORY/admin.kubeconfig"

# the tenancy API becomes available pretty late during startup, so it's a good readiness check
KUBECTL="$(UGET_PRINT_PATH=relative make --no-print-directory install-kubectl)"
if ! retry_linear 3 20 "$KUBECTL" --kubeconfig "$KCP_KUBECONFIG" get workspaces; then
  echodate "kcp never became ready."
  exit 1
fi

# makes it easier to reference these files from various _test.go files.
export ROOT_DIRECTORY="$(realpath .)"
export KCP_KUBECONFIG="$(realpath "$KCP_KUBECONFIG")"
export AGENT_BINARY="$(realpath _build/api-syncagent)"

# The tests require ARTIFACTS to be absolute.
ARTIFACTS="$(realpath "$ARTIFACTS")"

# time to run the tests
echodate "Running e2e tests…"

WHAT="${WHAT:-./test/e2e/...}"
TEST_ARGS="${TEST_ARGS:--timeout 30m -v}"
E2E_PARALLELISM=${E2E_PARALLELISM:-2}

(set -x; go test -tags e2e -parallel $E2E_PARALLELISM $TEST_ARGS "$WHAT")
