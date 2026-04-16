BINARY        ?= cloudflared-gateway
GWAPI_VERSION ?= v1.5.1

# Dev/ephemeral release tag for ko-push + chart-push targets.
DEV_TAG       ?= dev-$(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
KO_REPO       ?= ghcr.io/mccormickt/cloudflared-gateway
CHART_OCI     ?= oci://ghcr.io/mccormickt/charts
KIND_CLUSTER  ?= cloudflared-gateway-dev

TESTBIN_DIR       ?= $(CURDIR)/testbin
KUBEBUILDER_ASSETS ?= $(shell setup-envtest use --bin-dir $(TESTBIN_DIR) -p path)

GOBIN ?= $(shell go env GOBIN)
ifeq ($(GOBIN),)
GOBIN := $(shell go env GOPATH)/bin
endif
CONTROLLER_GEN ?= $(GOBIN)/controller-gen
KO             ?= $(GOBIN)/ko

.PHONY: build test test-unit test-integration test-e2e test-conformance test-all vet lint clean image setup-envtest install-crds manifests generate run fmt controller-gen ko ko-build ko-push chart-package chart-push dev-release kind-up kind-down kind-load kind-install kind-dev help

build: ## Build the controller binary
	go build -o bin/$(BINARY) ./cmd/

image: ko-build ## Build container image locally via ko (alias for ko-build)

run: ## Run the controller locally
	go run ./cmd/main.go

test: test-unit ## Run unit tests (default)

test-unit: ## Run unit tests (no cluster required)
	go test ./internal/...

test-integration: ## Run envtest integration tests (real API server, no cluster)
	KUBEBUILDER_ASSETS=$(KUBEBUILDER_ASSETS) go test ./tests/integration/ -timeout 120s -v

test-e2e: ## Run kind e2e tests (creates a kind cluster, requires docker + CLOUDFLARE_* env)
	go test ./tests/e2e/ -timeout 10m -v

test-conformance: ## Run Gateway API conformance suite (requires deployed controller + CLOUDFLARE_* env)
	go test ./tests/conformance/ -timeout 30m -v \
		-args -gateway-class=cloudflare-tunnel

test-all: test-unit test-integration test-e2e ## Run unit + integration + e2e tests

manifests: controller-gen ## Generate CRD and RBAC manifests from markers
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd \
		paths="./..." \
		output:crd:artifacts:config=config/crd \
		output:rbac:artifacts:config=config/rbac

generate: controller-gen ## Generate deepcopy methods
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

fmt: ## Format Go source files
	go fmt ./...

vet: ## Run go vet
	go vet ./...

lint: ## Lint with golangci-lint
	golangci-lint run ./...

controller-gen: ## Install controller-gen if not present
	@test -x $(CONTROLLER_GEN) || go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest

ko: ## Install ko if not present
	@test -x $(KO) || go install github.com/google/ko@latest

ko-build: ko ## Build image with ko and load into the local docker daemon as $(KO_REPO):$(DEV_TAG)
	KO_DOCKER_REPO=$(KO_REPO) $(KO) build ./cmd --bare --tags=$(DEV_TAG) --local

ko-push: ko ## Build+push multi-arch image via ko to $(KO_REPO):$(DEV_TAG)
	KO_DOCKER_REPO=$(KO_REPO) $(KO) build ./cmd \
		--bare \
		--tags=$(DEV_TAG) \
		--platform=linux/amd64,linux/arm64

chart-package: ## Package the Helm chart into chart-dist/ with version $(DEV_TAG)
	@mkdir -p chart-dist
	helm package charts/cloudflared-gateway \
		--version 0.0.0-$(DEV_TAG) \
		--app-version $(DEV_TAG) \
		--destination chart-dist/

chart-push: chart-package ## Package+push Helm chart to $(CHART_OCI) with version $(DEV_TAG)
	helm push chart-dist/cloudflared-gateway-0.0.0-$(DEV_TAG).tgz $(CHART_OCI)

dev-release: ko-push chart-push ## Push image + chart with dev tag ($(DEV_TAG))

kind-up: ## Create a local kind cluster ($(KIND_CLUSTER)) with Gateway API CRDs installed
	kind create cluster --name $(KIND_CLUSTER)
	kubectl apply --server-side -f \
		https://github.com/kubernetes-sigs/gateway-api/releases/download/$(GWAPI_VERSION)/experimental-install.yaml

kind-down: ## Delete the local kind cluster ($(KIND_CLUSTER))
	kind delete cluster --name $(KIND_CLUSTER)

kind-load: ko-build ## Build image with ko and load into kind cluster $(KIND_CLUSTER)
	kind load docker-image $(KO_REPO):$(DEV_TAG) --name $(KIND_CLUSTER)

kind-install: chart-package ## Install the controller into kind via the local chart (needs CLOUDFLARE_* env)
	@test -n "$$CLOUDFLARE_ACCOUNT_ID" || { echo "CLOUDFLARE_ACCOUNT_ID is required"; exit 1; }
	@test -n "$$CLOUDFLARE_API_TOKEN"  || { echo "CLOUDFLARE_API_TOKEN is required";  exit 1; }
	helm upgrade --install cloudflared-gateway chart-dist/cloudflared-gateway-0.0.0-$(DEV_TAG).tgz \
		--namespace cloudflared-gateway --create-namespace \
		--set image.repository=$(KO_REPO) \
		--set image.tag=$(DEV_TAG) \
		--set cloudflare.accountId=$(CLOUDFLARE_ACCOUNT_ID) \
		--set cloudflare.apiToken=$(CLOUDFLARE_API_TOKEN)

kind-dev: kind-load kind-install ## Build, load, and install into $(KIND_CLUSTER)

setup-envtest: ## Install envtest binaries into testbin/
	go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
	setup-envtest use --bin-dir $(TESTBIN_DIR)

install-crds: ## Install Gateway API CRDs into current cluster
	kubectl apply --server-side -f \
		https://github.com/kubernetes-sigs/gateway-api/releases/download/$(GWAPI_VERSION)/experimental-install.yaml

clean: ## Remove build artifacts
	rm -rf bin/ dist/ chart-dist/ $(TESTBIN_DIR)

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[32m%-30s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := help
