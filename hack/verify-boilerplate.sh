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

cd $(dirname $0)/..
source hack/lib.sh

make --no-print-directory _tools/boilerplate

echo "Checking file boilerplates…"

set -x

_tools/boilerplate \
  -boilerplates hack/boilerplate \
  -exclude .github \
  -exclude internal/certificates/triple \
  -exclude sdk/applyconfiguration \
  -exclude sdk/clientset \
  -exclude sdk/informers \
  -exclude sdk/listers \
  -exclude test/crds

_tools/boilerplate \
  -boilerplates hack/boilerplate/generated \
  sdk/applyconfiguration \
  sdk/clientset \
  sdk/informers \
  sdk/listers
