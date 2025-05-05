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

export CGO_ENABLED ?= 0
export GOFLAGS ?= -mod=readonly -trimpath
export GO111MODULE = on
CMD ?= $(filter-out OWNERS, $(notdir $(wildcard ./cmd/*)))
GOBUILDFLAGS ?= -v
GIT_HEAD ?= $(shell git log -1 --format=%H)
GIT_VERSION = $(shell git describe --tags --always)
LDFLAGS += -extldflags '-static' \
  -X github.com/kcp-dev/api-syncagent/internal/version.gitVersion=$(GIT_VERSION) \
  -X github.com/kcp-dev/api-syncagent/internal/version.gitHead=$(GIT_HEAD)
BUILD_DEST ?= _build
GOTOOLFLAGS ?= $(GOBUILDFLAGS) -ldflags '-w $(LDFLAGS)' $(GOTOOLFLAGS_EXTRA)
GOARCH ?= $(shell go env GOARCH)
GOOS ?= $(shell go env GOOS)

.PHONY: all
all: build test

ldflags:
	@echo $(LDFLAGS)

.PHONY: build
build: $(CMD)

.PHONY: $(CMD)
$(CMD): %: $(BUILD_DEST)/%

$(BUILD_DEST)/%: cmd/%
	go build $(GOTOOLFLAGS) -o $@ ./cmd/$*

GOLANGCI_LINT = _tools/golangci-lint
GOLANGCI_LINT_VERSION = 1.64.2

.PHONY: $(GOLANGCI_LINT)
$(GOLANGCI_LINT):
	@hack/download-tool.sh \
	  https://github.com/golangci/golangci-lint/releases/download/v${GOLANGCI_LINT_VERSION}/golangci-lint-${GOLANGCI_LINT_VERSION}-${GOOS}-${GOARCH}.tar.gz \
	  golangci-lint \
	  ${GOLANGCI_LINT_VERSION}

GIMPS = _tools/gimps
GIMPS_VERSION = 0.6.0

.PHONY: $(GIMPS)
$(GIMPS):
	@hack/download-tool.sh \
	  https://github.com/xrstf/gimps/releases/download/v${GIMPS_VERSION}/gimps_${GIMPS_VERSION}_${GOOS}_${GOARCH}.tar.gz \
	  gimps \
	  ${GIMPS_VERSION}

WWHRD = _tools/wwhrd
WWHRD_VERSION = 0.4.0

.PHONY: $(WWHRD)
$(WWHRD):
	@hack/download-tool.sh \
	  https://github.com/frapposelli/wwhrd/releases/download/v${WWHRD_VERSION}/wwhrd_${WWHRD_VERSION}_${GOOS}_${GOARCH}.tar.gz \
	  wwhrd \
	  ${WWHRD_VERSION} \
	  wwhrd

BOILERPLATE = _tools/boilerplate
BOILERPLATE_VERSION = 0.3.0

.PHONY: $(BOILERPLATE)
$(BOILERPLATE):
	@hack/download-tool.sh \
	  https://github.com/kubermatic-labs/boilerplate/releases/download/v${BOILERPLATE_VERSION}/boilerplate_${BOILERPLATE_VERSION}_${GOOS}_${GOARCH}.tar.gz \
	  boilerplate \
	  ${BOILERPLATE_VERSION}

YQ = _tools/yq
YQ_VERSION = 4.44.6

.PHONY: $(YQ)
$(YQ):
	@UNCOMPRESSED=true hack/download-tool.sh \
	  https://github.com/mikefarah/yq/releases/download/v${YQ_VERSION}/yq_${GOOS}_${GOARCH} \
	  yq \
	  ${YQ_VERSION} \
	  yq_*

KCP = _tools/kcp
KCP_VERSION = 0.27.1

.PHONY: $(KCP)
$(KCP):
	@hack/download-tool.sh \
	  https://github.com/kcp-dev/kcp/releases/download/v${KCP_VERSION}/kcp_${KCP_VERSION}_${GOOS}_${GOARCH}.tar.gz \
	  kcp \
	  ${KCP_VERSION}

ENVTEST = _tools/setup-envtest
ENVTEST_VERSION = release-0.19

.PHONY: $(ENVTEST)
$(ENVTEST):
	@GO_MODULE=true hack/download-tool.sh sigs.k8s.io/controller-runtime/tools/setup-envtest setup-envtest $(ENVTEST_VERSION)

.PHONY: test
test:
	./hack/run-tests.sh

.PHONY: codegen
codegen: $(YQ)
	hack/update-codegen-crds.sh
	hack/update-codegen-sdk.sh

.PHONY: build-tests
build-tests:
	go test -run nope ./...

.PHONY: clean
clean:
	rm -rf $(BUILD_DEST)
	@echo "Cleaned $(BUILD_DEST)"

.PHONY: lint
lint: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) run \
		--verbose \
		--print-resources-usage \
		./...

.PHONY: imports
imports: $(GIMPS)
	$(GIMPS) .

.PHONY: verify
verify:
	./hack/verify-boilerplate.sh
	./hack/verify-licenses.sh


### docs

VENVDIR=$(abspath docs/venv)
REQUIREMENTS_TXT=docs/requirements.txt

.PHONY: local-docs
local-docs: venv ## Run mkdocs serve
	. $(VENV)/activate; \
	VENV=$(VENV) cd docs && mkdocs serve

.PHONY: serve-docs
serve-docs: venv ## Serve docs
	. $(VENV)/activate; \
	VENV=$(VENV) REMOTE=$(REMOTE) BRANCH=$(BRANCH) docs/scripts/serve-docs.sh

.PHONY: deploy-docs
deploy-docs: venv ## Deploy docs
	. $(VENV)/activate; \
	REMOTE=$(REMOTE) BRANCH=$(BRANCH) docs/scripts/deploy-docs.sh

include Makefile.venv
