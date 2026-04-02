# Cloudflare Tunnel Controller

A Kubernetes controller that uses the [Gateway API](https://gateway-api.sigs.k8s.io/) to provision [Cloudflare Tunnels](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/). Create a Gateway, attach routes, and the controller handles tunnel lifecycle, `cloudflared` deployment, and ingress configuration.

## How It Works

1. You create a `GatewayClass` referencing this controller and a `Gateway` using that class
2. The controller creates a Cloudflare Tunnel and deploys `cloudflared` pods
3. You attach `HTTPRoute`, `GRPCRoute`, `TLSRoute`, or `TCPRoute` resources to the Gateway
4. The controller converts routes to Cloudflare ingress rules and pushes the configuration

The controller also supports `BackendTLSPolicy` for TLS origin verification and `CloudflareAccessPolicy` (a custom CRD) for Cloudflare Access JWT enforcement via [GEP-713 Policy Attachment](https://gateway-api.sigs.k8s.io/geps/gep-713/).

## Getting Started

### Prerequisites

- A Kubernetes cluster (v1.28+)
- Gateway API CRDs installed
- A Cloudflare account with a tunnel-capable plan
- A Cloudflare API token with tunnel permissions

### Install Gateway API CRDs

```sh
kubectl apply --server-side -f \
  https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.5.1/experimental-install.yaml
```

### Install the Controller CRD

```sh
kubectl apply --server-side -f config/crd/cloudflare.jan0ski.net_cloudflareaccesspolicies.yaml
```

### Run the Controller

Set your Cloudflare credentials and run:

```sh
export CLOUDFLARE_ACCOUNT_ID=<your-account-id>
export CLOUDFLARE_API_TOKEN=<your-api-token>
make run
```

### Create Resources

```yaml
# GatewayClass — register this controller
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: cloudflare-tunnel
spec:
  controllerName: jan0ski.net/cf-tunnel-controller
---
# Gateway — provisions the tunnel and deploys cloudflared
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: my-tunnel
  namespace: default
spec:
  gatewayClassName: cloudflare-tunnel
  listeners:
    - name: http
      port: 80
      protocol: HTTP
      allowedRoutes:
        namespaces:
          from: Same
---
# HTTPRoute — expose a service through the tunnel
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: my-route
  namespace: default
spec:
  parentRefs:
    - name: my-tunnel
  hostnames:
    - "app.example.com"
  rules:
    - backendRefs:
        - name: my-service
          port: 8080
```

More examples in [`examples/`](examples/).

## Supported Route Types

| Route Type | API Version | What It Does |
|------------|-------------|--------------|
| HTTPRoute | `v1` | HTTP/HTTPS traffic with path/header matching |
| GRPCRoute | `v1` | gRPC traffic with service/method matching |
| TLSRoute | `v1alpha2` | TLS passthrough via SNI hostname |
| TCPRoute | `v1alpha2` | Raw TCP traffic forwarding |

## CloudflareAccessPolicy

Enforce [Cloudflare Access](https://developers.cloudflare.com/cloudflare-one/policies/access/) JWT validation on routes. Target a Gateway (protects all routes) or a specific HTTPRoute:

```yaml
apiVersion: cloudflare.jan0ski.net/v1alpha1
kind: CloudflareAccessPolicy
metadata:
  name: require-access
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: Gateway
      name: my-tunnel
  teamName: my-org
  required: true
  audTag:
    - "your-aud-tag"
```

## Route Annotations

Fine-tune Cloudflare tunnel origin settings per-route using annotations with the `tunnels.cloudflare.com/` prefix. These map to Cloudflare's [`originRequest`](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/configure-tunnels/origin-configuration/) fields.

## Development

### Requirements

- Go 1.25+
- [`controller-gen`](https://book.kubebuilder.io/reference/controller-gen) (installed automatically by `make`)
- [`golangci-lint`](https://golangci-lint.run/) v2
- Docker (for e2e tests)

### Build and Test

```sh
make build              # Build binary to bin/
make test-unit          # Unit tests (fake client, no cluster)
make test-integration   # Integration tests (envtest, real API server)
make test-e2e           # E2E tests (KinD cluster, requires CLOUDFLARE_* env)
make lint               # golangci-lint
```

### Code Generation

After changing API types or RBAC markers:

```sh
make manifests          # Regenerate CRD and RBAC manifests
make generate           # Regenerate deepcopy methods
```

### Running Locally

```sh
# Point at a cluster (e.g., kind, minikube)
export KUBECONFIG=~/.kube/config
export CLOUDFLARE_ACCOUNT_ID=<your-account-id>
export CLOUDFLARE_API_TOKEN=<your-api-token>

# Install CRDs
make install-crds
kubectl apply --server-side -f config/crd/cloudflare.jan0ski.net_cloudflareaccesspolicies.yaml

# Run
make run
```

### Project Layout

```
cmd/                    Entrypoint
internal/
  controller/           Reconciler, watches, status patching, attachment validation
  cloudflare/           Cloudflare API client, ingress rule building
api/v1alpha1/           CloudflareAccessPolicy CRD types
config/
  crd/                  Generated CRD manifests
  rbac/                 Generated RBAC manifests
examples/               Example Gateway API resources
tests/
  integration/          envtest integration tests (full controller loop)
  e2e/                  KinD cluster e2e tests
  conformance/          Gateway API conformance suite
```
