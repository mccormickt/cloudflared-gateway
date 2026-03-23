BINARY        ?= cloudflare-tunnel-controller
IMAGE         ?= $(BINARY):dev
GWAPI_VERSION ?= v1.5.1

TESTBIN_DIR       ?= $(CURDIR)/testbin
KUBEBUILDER_ASSETS ?= $(shell setup-envtest use --bin-dir $(TESTBIN_DIR) -p path)

.PHONY: build test test-unit test-integration test-e2e test-conformance test-all vet lint clean image setup-envtest install-crds help

build: ## Build the controller binary
	go build -o bin/$(BINARY) .

image: ## Build the container image
	docker build -t $(IMAGE) .

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

vet: ## Run go vet
	go vet ./...

lint: vet ## Lint (currently just vet)

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
