# cloudflared-gateway

> Kubernetes Gateway API controller for Cloudflare Tunnels

[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
![CI](https://github.com/mccormickt/cloudflared-gateway/actions/workflows/ci.yaml/badge.svg)
![Release](https://img.shields.io/github/v/release/mccormickt/cloudflared-gateway)

## Overview

`cloudflared-gateway` is a Kubernetes [Gateway API](https://gateway-api.sigs.k8s.io/) controller that provisions [Cloudflare Tunnels](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/). It watches `Gateway` and route resources, programs a Cloudflare Tunnel for each Gateway via the Cloudflare API, and runs the `cloudflared` pods that terminate the tunnel inside the cluster.

Because traffic egresses through a tunnel, no public LoadBalancer or ingress IP is required — pods reach Cloudflare over outbound connections. Cloudflare Access integrates natively via the `CloudflareAccessPolicy` CRD (GEP-713 policy attachment), so JWT enforcement can be applied at the Gateway or route level without custom annotations. Standard Gateway API semantics cover the common flows: attach a route to a Gateway and its hostnames become reachable through the tunnel.

Supported route types are `HTTPRoute`, `GRPCRoute`, `TLSRoute`, and `TCPRoute`, plus `BackendTLSPolicy` for origin TLS verification. Each `Gateway` gets its own Cloudflare Tunnel and its own `cloudflared` Deployment; routes attached to that Gateway are converted into the tunnel's ingress rules.

## Architecture

```
┌─────────────┐     watches     ┌──────────────────┐     manages     ┌─────────────────┐
│  Gateway    │ ──────────────▶ │ cloudflared-     │ ──────────────▶ │  Cloudflare     │
│  HTTPRoute  │                 │ gateway          │   tunnel +      │  API            │
│  TLSRoute   │                 │ controller       │   ingress cfg   │                 │
│  ...        │                 └──────────────────┘                 └─────────────────┘
└─────────────┘                          │
                                         │ deploys
                                         ▼
                                ┌──────────────────┐
                                │ cloudflared pods │ ◀────── tunnel token
                                │ (per Gateway)    │
                                └──────────────────┘
                                         │
                                         ▼
                                ┌──────────────────┐
                                │  backend Service │
                                └──────────────────┘
```

The reconcile loop is Gateway-primary. For each `Gateway` the controller:

1. Validates the `GatewayClass` controller name and manages a cleanup finalizer.
2. Ensures a Kubernetes `Secret` exists holding a 32-byte tunnel secret.
3. Creates or retrieves the Cloudflare tunnel (recreating it if the secret was regenerated).
4. Assembles the tunnel token and stores it in the `Secret`.
5. Applies a `cloudflared` `Deployment` that reads the token from the `Secret`.
6. Collects attached routes (`HTTPRoute`, `GRPCRoute`, `TLSRoute`, `TCPRoute`), validates attachment, and converts them to Cloudflare ingress rules (with a catch-all 404).
7. Pushes the ingress configuration to Cloudflare and patches status on the `Gateway`, `GatewayClass`, and each route.

## Getting Started on KinD

### Prerequisites

- Docker
- `kind` (v0.23+)
- `kubectl` (v1.28+)
- `helm` (v3.8+ for OCI support)
- A Cloudflare account with tunnel permissions
- A Cloudflare API token scoped to `Account:Cloudflare Tunnel:Edit`
- Your Cloudflare account ID

### 1. Create a KinD cluster

```sh
kind create cluster --name cloudflared-gateway
```

### 2. Install Gateway API CRDs

```sh
kubectl apply --server-side -f \
  https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.5.1/experimental-install.yaml
```

### 3. Create the Cloudflare credentials Secret

```sh
kubectl create namespace cloudflared-gateway

kubectl -n cloudflared-gateway create secret generic cloudflare-creds \
  --from-literal=account-id=$CLOUDFLARE_ACCOUNT_ID \
  --from-literal=api-token=$CLOUDFLARE_API_TOKEN
```

### 4. Install the controller via Helm

```sh
helm install cloudflared-gateway \
  oci://ghcr.io/mccormickt/charts/cloudflared-gateway \
  --version 0.1.0 \
  --namespace cloudflared-gateway \
  --set cloudflare.existingSecret=cloudflare-creds
```

### 5. Apply an example Gateway + HTTPRoute

```sh
kubectl apply -f examples/gatewayclass.yaml
kubectl apply -f examples/gateway.yaml
kubectl apply -f examples/httproute.yaml
```

### 6. Verify

```sh
# The Gateway should show Accepted=True and Programmed=True
kubectl get gateway -A

# The controller pod is running
kubectl -n cloudflared-gateway get pods -l app.kubernetes.io/name=cloudflared-gateway

# A cloudflared Deployment has been provisioned in the Gateway's namespace
# (one per Gateway, named cloudflared-<gateway-name>)
kubectl get deployment -A -l app=cloudflared-<gateway-name>

# Check the Cloudflare dashboard at https://one.dash.cloudflare.com/ under
# Networks → Tunnels. Your tunnel should be listed and healthy.

# curl the hostname you configured in the Gateway
curl https://my-host.example.com/
```

## Configuration

The full chart value reference lives in [`charts/cloudflared-gateway/values.yaml`](charts/cloudflared-gateway/values.yaml). The most common values:

| Value | Description |
|-------|-------------|
| `image.repository` | Controller image repository (default: `ghcr.io/mccormickt/cloudflared-gateway`) |
| `image.tag` | Image tag (defaults to the chart `appVersion`) |
| `replicaCount` | Number of controller replicas |
| `cloudflare.existingSecret` | Name of a pre-existing Secret with `account-id` and `api-token` keys |
| `controllerName` | `GatewayClass.spec.controllerName` value the controller claims (default: `jan0ski.net/cloudflared-gateway`) |
| `resources` | Pod resource requests and limits |

### Route annotations

Per-route Cloudflare origin settings can be set via annotations with the `tunnels.cloudflare.com/` prefix. These map to Cloudflare's [`originRequest`](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/configure-tunnels/origin-configuration/) fields. Source of truth: [`internal/cloudflare/annotations.go`](internal/cloudflare/annotations.go).

| Annotation | Type |
|------------|------|
| `tunnels.cloudflare.com/proxy-type` | string |
| `tunnels.cloudflare.com/bastion-mode` | bool |
| `tunnels.cloudflare.com/disable-chunked-encoding` | bool |
| `tunnels.cloudflare.com/keep-alive-connections` | int |
| `tunnels.cloudflare.com/keep-alive-timeout` | duration (e.g. `30s`) |
| `tunnels.cloudflare.com/no-happy-eyeballs` | bool |

## CloudflareAccessPolicy

`CloudflareAccessPolicy` is a namespaced CRD (group `cloudflare.jan0ski.net`, kind `CloudflareAccessPolicy`) that uses the Gateway API Policy Attachment pattern ([GEP-713](https://gateway-api.sigs.k8s.io/geps/gep-713/)) to enforce Cloudflare Access JWT validation on a targeted resource. Point `spec.targetRefs` at a `Gateway` to protect every route attached to it, or at a specific `HTTPRoute` (or other route kind) for a narrower scope.

```yaml
apiVersion: cloudflare.jan0ski.net/v1alpha1
kind: CloudflareAccessPolicy
metadata:
  name: gateway-access
  namespace: default
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: Gateway
      name: cloudflare-tunnel
  teamName: my-org
  required: true
  audTag:
    - "your-aud-tag-here"
```

See [`examples/cloudflare-access-policy.yaml`](examples/cloudflare-access-policy.yaml) for a route-scoped variant.

## Development

```sh
make build              # Build the binary
make test-unit          # Unit tests (no cluster)
make test-integration   # envtest integration tests
make test-e2e           # KinD end-to-end (needs CLOUDFLARE_* env vars)
make manifests generate # Regenerate CRDs + deepcopy
make image              # Build container image locally via ko
make lint               # golangci-lint
make run                # Run the controller locally (needs kubeconfig + CF creds)
```

### Inner-loop on KinD

```sh
make kind-up            # Create a kind cluster with Gateway API CRDs
make kind-dev           # ko-build + kind load + helm install (needs CLOUDFLARE_* env)
make kind-down          # Tear down
```

## Project Layout

```
cmd/                            Entrypoint; builds the manager and wires dependencies
internal/
  controller/                   GatewayReconciler, watches, attachment validation, status patching
  cloudflare/                   Cloudflare API client, ingress rule building, annotation parsing
api/v1alpha1/                   CloudflareAccessPolicy CRD types (group: cloudflare.jan0ski.net)
config/
  crd/                          Generated CRD manifests
  rbac/                         Generated RBAC manifests
charts/cloudflared-gateway/     Helm chart
examples/                       Example Gateway API resources
tests/
  integration/                  envtest integration tests
  e2e/                          KinD end-to-end tests
  conformance/                  Gateway API conformance suite
```

## Contributing

Issues and pull requests are welcome. Run `make lint` and the relevant `make test-*` targets before submitting. Commits follow a conventional style (`feat:`, `fix:`, `chore:`, `refactor:`, `docs:`) — see `git log --oneline` for recent examples.

## License

This project is licensed under the Apache License 2.0 — see [LICENSE](LICENSE).
