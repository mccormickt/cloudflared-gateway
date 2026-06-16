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
- A Cloudflare API token scoped to `Account:Cloudflare Tunnel:Edit` (for [DNS management](#dns-management) the token must also be **account-scoped** with `Zone:Read` + `Zone:DNS:Edit` — a token restricted to specific zones cannot enumerate zones and DNS silently does nothing)
- Your Cloudflare account ID

### 1. Create a KinD cluster

```sh
kind create cluster --name cloudflared-gateway
```

### 2. Install Gateway API CRDs

```sh
kubectl apply --server-side -f \
  https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.6.0-rc.1/experimental-install.yaml
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

> **DNS:** For traffic to reach the tunnel, each route hostname needs a *proxied*
> CNAME pointing at `<tunnelID>.cfargotunnel.com`. Enable [DNS management](#dns-management)
> to have the controller create and prune these records for you, or create them by
> hand in the Cloudflare dashboard. The Gateway's `status.addresses` reports the
> `cfargotunnel.com` target to point at.

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
| `dns.enabled` | Manage proxied CNAME records for route hostnames (default: `false`; see [DNS management](#dns-management)) |

### Origin request tuning (CloudflareOriginPolicy)

Per-route Cloudflare origin settings are configured with the typed `CloudflareOriginPolicy` CRD (Inherited Policy, [GEP-713](https://gateway-api.sigs.k8s.io/geps/gep-713/)) — this replaces the former `tunnels.cloudflare.com/*` route annotations. A policy targeting a `Gateway` is the default for every attached route; a policy targeting a route overrides it for that route. Fields map to Cloudflare's [`originRequest`](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/configure-tunnels/origin-configuration/) object. See [`examples/cloudflare-origin-policy.yaml`](examples/cloudflare-origin-policy.yaml).

| Field | Type |
|-------|------|
| `proxyType` | string (enum: `socks`) |
| `disableChunkedEncoding` | bool |
| `keepAliveConnections` | int |
| `keepAliveTimeout` | duration (e.g. `30s`) |
| `noHappyEyeballs` | bool |
| `tlsTimeout` | duration |
| `tcpKeepAlive` | duration |
| `http2Origin` | bool |
| `matchSNIToHost` | bool |

Fields owned by other mechanisms are intentionally not exposed here: Access (`CloudflareAccessPolicy`), origin TLS (`BackendTLSPolicy`), `httpHostHeader` (HTTPRoute filters), and `connectTimeout` (HTTPRoute timeouts).

### Tunnel infrastructure (CloudflareTunnelConfig)

The cloudflared Deployment is customized with the `CloudflareTunnelConfig` CRD, referenced via `GatewayClass.spec.parametersRef` (cluster-wide default) or `Gateway.spec.infrastructure.parametersRef` (per-Gateway override). It exposes `replicas`, `image`, `resources`, `logLevel`, `metricsPort`, pod labels/annotations, and scheduling (`nodeSelector`/`tolerations`/`affinity`). The pod security context is always fixed by the controller. See [`examples/cloudflare-tunnel-config.yaml`](examples/cloudflare-tunnel-config.yaml).

### DNS management

A Cloudflare Tunnel only receives traffic for a hostname once a *proxied* (orange-cloud) CNAME points that hostname at `<tunnelID>.cfargotunnel.com`. Set `dns.enabled=true` (flag `--enable-dns-management`, env `ENABLE_DNS_MANAGEMENT`) and the controller creates and maintains that record for every attached `HTTPRoute`/`GRPCRoute`/`TLSRoute` hostname, so no manual DNS step is needed. It is **off by default**.

- **Token scopes**: the API token must be **account-scoped** and additionally carry `Zone:Read` + `Zone:DNS:Edit`. Zone discovery lists zones by account; a token restricted to specific zones returns no zones, so no records are created (the controller logs a per-hostname "no zone" skip).
- **Always proxied**: records are created proxied with automatic TTL, because a tunnel CNAME is only reachable through Cloudflare's edge.
- **Wildcard hostnames are skipped**: a proxied wildcard CNAME requires an Enterprise plan, so `*.example.com` route hostnames are not managed (logged) and must be configured by hand.
- **Ownership**: each record is tagged with an owner comment (`cloudflared-gateway:owner=<gateway-uid>`). The controller only ever updates or deletes records it created — a pre-existing record for the same hostname is left untouched and logged. Records are batched per zone via Cloudflare's transactional [batch DNS endpoint](https://developers.cloudflare.com/dns/manage-dns-records/how-to/batch-record-changes/).
- **Cleanup**: when a Gateway is deleted, its owned records are pruned by the finalizer.
- **Per-Gateway opt-out**: annotate a Gateway with `cloudflared-gateway.jan0ski.net/dns-managed: "false"` to manage its DNS yourself.

Hostnames that don't fall under a zone on the account are skipped (and logged). A hostname whose entire zone no longer has any served route is pruned on Gateway deletion.

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

## Experimental: external origins (XBackend)

Routes can target destinations **outside** the cluster using the experimental Gateway API [`XBackend`](https://gateway-api.sigs.k8s.io/) resource (group `gateway.networking.x-k8s.io`, kind `XBackend`) instead of a synthetic `ExternalName` Service. A `backendRef` pointing at an `XBackend` of `type: ExternalHostname` makes the tunnel route to that FQDN — e.g. `https://api.openai.com:443`. This works for HTTPRoute, GRPCRoute, TLSRoute, and TCPRoute. See [`examples/xbackend.yaml`](examples/xbackend.yaml).

This feature is **off by default**. Enable it with:

- Helm: `--set experimental.backends.enabled=true`
- Controller flag/env: `--enable-experimental-backends` / `ENABLE_EXPERIMENTAL_BACKENDS=true`

When enabled, the controller requires the `XBackend` CRD (Gateway API **experimental** channel) and **fails fast at startup** if it — or any core Gateway API CRD — is missing. Install the CRDs out of band (`make install-crds` / `kubectl apply -f .../experimental-install.yaml`) or let the chart install them via the opt-in pre-install hook: `--set experimental.installGatewayAPICRDs=true` (pins `experimental.gatewayAPIVersion`, default the version this controller is built against). Startup also inspects the installed bundle's channel/version annotations: with experimental backends enabled, a non-`experimental` channel or a bundle older than the version the controller was built against is a fatal error; otherwise such mismatches are logged as warnings.

Mapping and limitations:

| XBackend spec | Behavior |
|---------------|----------|
| `protocol` omitted | Uses the referencing Route's protocol: HTTP for HTTPRoute, GRPC for GRPCRoute, and TCP for TLSRoute/TCPRoute |
| `protocol: HTTP`/`HTTP11` | HTTP origin |
| `protocol: HTTP2`/`H2C`/`GRPC` | HTTP/2 origin |
| `protocol: TCP` (with `tls.mode: None` or unset) | `tcp://` origin |
| `protocol: TCP` + `tls.mode != None` | **Unsupported** (cloudflared's `tcp://` proxy cannot verify origin TLS) — route reports `ResolvedRefs=False` (`UnsupportedProtocol`) |
| `protocol: MCP` | **Unsupported** — route reports `ResolvedRefs=False` (`UnsupportedProtocol`) |
| `tls.mode: None` | Plain HTTP origin |
| `tls.mode: ServerOnly` | HTTPS origin, server certificate verified against system CAs; `validation.hostname` becomes the SNI server name |
| `tls.mode: ServerOnly` + `validation.caCertificateRefs` | **Unsupported** (a custom CA pool isn't provisioned into cloudflared; use `wellKnownCACertificates: System`) — route reports `ResolvedRefs=False` (`UnsupportedCACerts`) |
| `tls.mode: ClientAndServer` | **Unsupported** (cloudflared cannot present an origin client certificate) — route reports `ResolvedRefs=False` (`UnsupportedProtocol`) |

Each route rule uses only its first `backendRef` (`backendRefs[0]`); additional backends and `weight` are ignored, since a Cloudflare ingress rule maps to a single origin service. Weighted/multi-backend external origins are not supported.

An `XBackend` must be in the same namespace as the Route that references it; cross-namespace references report `ResolvedRefs=False` with reason `RefNotPermitted`. When the feature is disabled, a route referencing an `XBackend` reports `ResolvedRefs=False` (`InvalidKind`) and serves `http_status:503`. XBackends report GEP-713 ancestor status under `status.parents[]`.

## Verifying releases

Every release signs the container image, the chart OCI artifact, and the checksums file with [cosign](https://github.com/sigstore/cosign) keyless (GitHub OIDC → Fulcio, logged to Rekor). Pin signatures to the release workflow's OIDC identity.

```sh
export IDENTITY='^https://github\.com/mccormickt/cloudflared-gateway/\.github/workflows/release\.yml@refs/tags/v.*'
export ISSUER=https://token.actions.githubusercontent.com

# Image
cosign verify \
  --certificate-identity-regexp "$IDENTITY" \
  --certificate-oidc-issuer "$ISSUER" \
  ghcr.io/mccormickt/cloudflared-gateway:0.1.0

# Helm chart OCI artifact
cosign verify \
  --certificate-identity-regexp "$IDENTITY" \
  --certificate-oidc-issuer "$ISSUER" \
  ghcr.io/mccormickt/charts/cloudflared-gateway:0.1.0

# checksums.txt (covers all binary archives and the chart .tgz asset on the Release)
curl -sLO https://github.com/mccormickt/cloudflared-gateway/releases/download/v0.1.0/checksums.txt
curl -sLO https://github.com/mccormickt/cloudflared-gateway/releases/download/v0.1.0/checksums.txt.sig
curl -sLO https://github.com/mccormickt/cloudflared-gateway/releases/download/v0.1.0/checksums.txt.pem
cosign verify-blob \
  --certificate checksums.txt.pem \
  --signature  checksums.txt.sig \
  --certificate-identity-regexp "$IDENTITY" \
  --certificate-oidc-issuer "$ISSUER" \
  checksums.txt
```

The image also publishes an SPDX SBOM attestation; fetch it with `cosign download sbom ghcr.io/mccormickt/cloudflared-gateway:0.1.0`.

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
  cloudflare/                   cloudflare-go v7 client + domain-type boundary, ingress rule building
api/v1alpha1/                   CRD types (group: cloudflare.jan0ski.net): CloudflareAccessPolicy, CloudflareOriginPolicy, CloudflareTunnelConfig
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
