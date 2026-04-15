BINARY        ?= cloudflared-gateway
IMAGE         ?= ghcr.io/mccormickt/cloudflared-gateway:dev
GWAPI_VERSION ?= v1.5.1

TESTBIN_DIR       ?= $(CURDIR)/testbin
KUBEBUILDER_ASSETS ?= $(shell setup-envtest use --bin-dir $(TESTBIN_DIR) -p path)

GOBIN ?= $(shell go env GOBIN)
ifeq ($(GOBIN),)
GOBIN := $(shell go env GOPATH)/bin
endif
CONTROLLER_GEN ?= $(GOBIN)/controller-gen

.PHONY: build test test-unit test-integration test-e2e test-conformance test-all vet lint clean image setup-envtest install-crds manifests generate run fmt controller-gen help

build: ## Build the controller binary
	go build -o bin/$(BINARY) ./cmd/

image: ## Build the container image
	docker build -t $(IMAGE) .

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

setup-envtest: ## Install envtest binaries into testbin/
	go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
	setup-envtest use --bin-dir $(TESTBIN_DIR)

install-crds: ## Install Gateway API CRDs into current cluster
	kubectl apply --server-side -f \
		https://github.com/kubernetes-sigs/gateway-api/releases/download/$(GWAPI_VERSION)/experimental-install.yaml

clean: ## Remove build artifacts
	rm -rf bin/ $(TESTBIN_DIR)

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[32m%-30s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := help
