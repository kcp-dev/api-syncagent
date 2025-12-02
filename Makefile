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
GIT_VERSION = $(shell git describe --tags --always --match='v*')
LDFLAGS += -extldflags '-static' \
  -X github.com/kcp-dev/api-syncagent/internal/version.gitVersion=$(GIT_VERSION) \
  -X github.com/kcp-dev/api-syncagent/internal/version.gitHead=$(GIT_HEAD)
LDFLAGS_EXTRA ?= -w

ifdef DEBUG_BUILD
GOFLAGS = -mod=readonly
LDFLAGS_EXTRA =
GOTOOLFLAGS_EXTRA = -gcflags=all="-N -l"
endif

BUILD_DEST ?= _build
GOTOOLFLAGS ?= $(GOBUILDFLAGS) -ldflags '$(LDFLAGS) $(LDFLAGS_EXTRA)' $(GOTOOLFLAGS_EXTRA)
GOARCH ?= $(shell go env GOARCH)
GOOS ?= $(shell go env GOOS)

export UGET_DIRECTORY = _tools
export UGET_CHECKSUMS = hack/tools.checksums
export UGET_VERSIONED_BINARIES = true

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

.PHONY: test
test:
	./hack/run-tests.sh

.PHONY: test-e2e
test-e2e:
	./hack/run-e2e-tests.sh

.PHONY: codegen
codegen:
	hack/update-codegen-crds.sh
	hack/update-codegen-sdk.sh

.PHONY: build-tests
build-tests:
	go test -run nope ./...

.PHONY: clean
clean:
	rm -rf $(BUILD_DEST) $(UGET_DIRECTORY)
	@echo "Cleaned $(BUILD_DEST) and $(UGET_DIRECTORY)"

.PHONY: lint
lint: install-golangci-lint
	$(GOLANGCI_LINT) run \
		--verbose \
		--timeout 20m \
		--print-resources-usage \
		./...

.PHONY: imports
imports: install-gimps
	$(GIMPS) .

.PHONY: verify
verify:
	./hack/verify-boilerplate.sh
	./hack/verify-licenses.sh

############################################################################
### tools

BOILERPLATE_VERSION ?= 0.3.0
ENVTEST_VERSION ?= release-0.19
GIMPS_VERSION ?= 0.6.3
GOIMPORTS_VERSION ?= c70783e636f2213cac683f6865d88c5edace3157
GOLANGCI_LINT_VERSION ?= 2.1.6
KUBECTL_VERSION ?= v1.34.2
WWHRD_VERSION ?= 06b99400ca6db678386ba5dc39bbbdcdadb664ff
YQ_VERSION ?= 4.44.6

# exported because the e2e tests make use of it
export KCP_VERSION ?= 0.28.1

APPLYCONFIGURATION_GEN_VERSION ?= v0.33.4
CLIENT_GEN_VERSION ?= v0.33.4
CONTROLLER_GEN_VERSION ?= v0.18.0
KCP_CODEGEN_VERSION ?= v2.4.0
RECONCILER_GEN_VERSION ?= v0.5.0

.PHONY: install-boilerplate
install-boilerplate:
	@hack/uget.sh https://github.com/kubermatic-labs/boilerplate/releases/download/v{VERSION}/boilerplate_{VERSION}_{GOOS}_{GOARCH}.tar.gz boilerplate $(BOILERPLATE_VERSION)

.PHONY: install-envtest
install-envtest:
	@GO_MODULE=true hack/uget.sh sigs.k8s.io/controller-runtime/tools/setup-envtest setup-envtest $(ENVTEST_VERSION)

GIMPS = $(UGET_DIRECTORY)/gimps-$(GIMPS_VERSION)

.PHONY: install-gimps
install-gimps:
	@hack/uget.sh https://codeberg.org/xrstf/gimps/releases/download/v{VERSION}/gimps_{VERSION}_{GOOS}_{GOARCH}.tar.gz gimps $(GIMPS_VERSION)

.PHONY: install-goimports
install-goimports:
	@GO_MODULE=true hack/uget.sh github.com/openshift-eng/openshift-goimports goimports $(GOIMPORTS_VERSION)

GOLANGCI_LINT = $(UGET_DIRECTORY)/golangci-lint-$(GOLANGCI_LINT_VERSION)

.PHONY: install-golangci-lint
install-golangci-lint:
	@hack/uget.sh https://github.com/golangci/golangci-lint/releases/download/v{VERSION}/golangci-lint-{VERSION}-{GOOS}-{GOARCH}.tar.gz golangci-lint $(GOLANGCI_LINT_VERSION)

.PHONY: install-kubectl
install-kubectl:
	@UNCOMPRESSED=true hack/uget.sh https://dl.k8s.io/release/{VERSION}/bin/{GOOS}/{GOARCH}/kubectl kubectl $(KUBECTL_VERSION) kubectl

# wwhrd is installed as a Go module rather than from the provided
# binaries because there is no arm64 binary available from the author.
# See https://github.com/frapposelli/wwhrd/issues/141
.PHONY: install-wwhrd
install-wwhrd:
	@GO_MODULE=true hack/uget.sh github.com/frapposelli/wwhrd wwhrd $(WWHRD_VERSION)

.PHONY: install-yq
install-yq:
	@UNCOMPRESSED=true hack/uget.sh https://github.com/mikefarah/yq/releases/download/v{VERSION}/yq_{GOOS}_{GOARCH} yq $(YQ_VERSION) yq_*

.PHONY: install-kcp
install-kcp:
	@hack/uget.sh https://github.com/kcp-dev/kcp/releases/download/v{VERSION}/kcp_{VERSION}_{GOOS}_{GOARCH}.tar.gz kcp $(KCP_VERSION)

.PHONY: install-applyconfiguration-gen
install-applyconfiguration-gen:
	@GO_MODULE=true hack/uget.sh k8s.io/code-generator/cmd/applyconfiguration-gen applyconfiguration-gen $(APPLYCONFIGURATION_GEN_VERSION)

.PHONY: install-client-gen
install-client-gen:
	@GO_MODULE=true hack/uget.sh k8s.io/code-generator/cmd/client-gen client-gen $(CLIENT_GEN_VERSION)

.PHONY: install-controller-gen
install-controller-gen:
	@GO_MODULE=true hack/uget.sh sigs.k8s.io/controller-tools/cmd/controller-gen controller-gen $(CONTROLLER_GEN_VERSION)

.PHONY: install-kcp-codegen
install-kcp-codegen:
	@GO_MODULE=true hack/uget.sh github.com/kcp-dev/code-generator/v2 kcp-code-generator $(KCP_CODEGEN_VERSION) code-generator

.PHONY: install-reconciler-gen
install-reconciler-gen:
	@GO_MODULE=true hack/uget.sh k8c.io/reconciler/cmd/reconciler-gen reconciler-gen $(RECONCILER_GEN_VERSION)

############################################################################
### docs

VENVDIR=$(abspath docs/venv)
REQUIREMENTS_TXT=docs/requirements.txt

.PHONY: generate-api-docs
generate-api-docs: ## Generate api docs
	git clean -fdX docs/content/reference
	docs/generators/crd-ref/run-crd-ref-gen.sh

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
