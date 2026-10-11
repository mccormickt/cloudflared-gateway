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
  https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.6.3/standard-install.yaml
```

The controller uses `gateway.networking.k8s.io/v1` for Gateway, GatewayClass,
HTTPRoute, GRPCRoute, TLSRoute, TCPRoute, BackendTLSPolicy, and ReferenceGrant.
All are in the standard v1.6 bundle. Startup checks each required served API
version and reports an install command if one is missing. Older TLSRoute and
TCPRoute versions are not served by this bundle; update manifests to `v1`.
The experimental channel is needed only for [XBackend](#experimental-external-origins-xbackend).

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

### Backend references

Every route's `backendRefs[0]` is resolved before it reaches the tunnel config. A ref must name a `Service` in the core group (the Gateway API default) or, with the experimental feature on, an [`XBackend`](#experimental-external-origins-xbackend); any other kind is rejected. The referenced object must exist, and a ref that crosses namespaces must be authorized by a `ReferenceGrant` in the *backend's* namespace.

A ref that fails any of these reports `ResolvedRefs=False`. HTTPRoute serves
`http_status:500`, as required by Gateway API; other route kinds use
`http_status:503`:

| Condition | Reason |
|-----------|--------|
| Backend object does not exist | `BackendNotFound` |
| Cross-namespace ref with no matching `ReferenceGrant` | `RefNotPermitted` |
| `kind` is neither `Service` nor `XBackend` | `InvalidKind` |

A route with several failing refs reports the most actionable one. Services and grants are watched: creating a missing Service or a required `ReferenceGrant` restores routing without changing the route, and deleting either stops serving it.

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

Routes can target destinations **outside** the cluster using the experimental Gateway API [`XBackend`](https://gateway-api.sigs.k8s.io/reference/api-types/backend/) resource (`gateway.networking.x-k8s.io/v1alpha1`, kind `XBackend`) instead of a synthetic `ExternalName` Service. Gateway API v1.6.3 has **no stable Backend/XBackend API**. A `backendRef` pointing at an `XBackend` of `type: ExternalHostname` makes the tunnel route to that FQDN — e.g. `https://api.openai.com:443`. This works for HTTPRoute, GRPCRoute, TLSRoute, and TCPRoute. See [`examples/xbackend.yaml`](examples/xbackend.yaml). Set the destination port in `XBackend.spec.port.port`; the route does not need a `backendRef.port`.

This feature is **off by default**. Enable it with:

- Helm: `--set experimental.backends.enabled=true`
- Controller flag/env: `--enable-experimental-backends` / `ENABLE_EXPERIMENTAL_BACKENDS=true`

Keep the stable CRDs on the standard channel and install **only** the experimental
XBackend CRD, then enable the feature. From this checkout:

```sh
make install-crds         # standard bundle; not needed if already installed
make install-xbackend-crd # only the XBackend CRD; waits until Established
```

The standalone manifest is pinned to:
`https://raw.githubusercontent.com/kubernetes-sigs/gateway-api/v1.6.3/config/crd/experimental/gateway.networking.x-k8s.io_xbackends.yaml`.
An existing full v1.6.x experimental bundle also works; do not replace it with
the standard bundle just to use this controller. The controller never installs CRDs.

| Cluster and feature flag | Startup / route behavior |
|--------------------------|--------------------------|
| Standard CRDs, feature disabled (default) | Starts without XBackend watches or API calls. XBackend refs report `ResolvedRefs=False` / `InvalidKind`, with an enable instruction. HTTPRoute serves 500; other route kinds use 503. |
| Feature enabled, XBackend CRD absent or `v1alpha1` not served | Fails at startup with install and disable instructions. Install the CRD before enabling the flag. |
| Standard CRDs plus standalone XBackend CRD, feature enabled | Starts. Compatibility is checked against the XBackend CRD, not the Gateway CRD's channel. |
| XBackend CRD from a different minor release, a prerelease, or an invalid annotated version | Fails at startup. Use a GA v1.6.x XBackend CRD; patch versions within that release line are compatible. |
| XBackend CRD metadata cannot be read (for example, missing RBAC) | Fails at startup with a CRD read-access instruction. Missing annotations on custom CRDs produce a compatibility warning instead. |

Restart the controller after installing or upgrading CRDs; startup discovery does
not enable features dynamically. A missing XBackend object on an enabled controller
reports `ResolvedRefs=False` / `BackendNotFound`. HTTPRoute serves 500; other route
kinds use 503.

For a new cluster, the optional Helm hook can install the standard bundle plus
XBackend: `--set experimental.installGatewayAPICRDs=true --set experimental.backends.enabled=true`.
It uses `experimental.gatewayAPIVersion` (default `v1.6.3`). When the hook is
enabled, rendering requires a GA v1 release >=v1.6.0 (older bundles lack required
`v1` APIs) and, when installing XBackend, a GA v1.6.x release; with the hook
disabled the value is not validated. The hook requires Kubernetes >=1.30 for
stable admission-policy APIs and outbound access to `github.com`,
`raw.githubusercontent.com`, and GitHub's release-asset CDN (for example
`release-assets.githubusercontent.com`). Its pod reuses the chart's
`imagePullSecrets`, `nodeSelector`, `tolerations`, `affinity.nodeAffinity`, and
`resources`, but has its own `app.kubernetes.io/name` label, so controller
NetworkPolicies do not grant it egress. This grants a temporary Job
permission to manage CRDs and Gateway API's safe-upgrade admission policies.
These are **cluster-wide, shared resources**, are not owned by the Helm release,
and remain after uninstall. On clusters with another CRD manager or an experimental
bundle, leave this hook disabled and have the cluster administrator install or
upgrade XBackend out of band.

The hook server-side applies with its own field manager (`<fullname>-crd-install`),
so it can upgrade without ownership conflicts only bundles it applied earlier
under the same release name and fullname. Manually installed bundles, a changed
release name, or a changed `fullnameOverride` leave the previous field owner in
place: keep upgrading those bundles out of band, or have an administrator
explicitly approve the ownership transfer. `experimental.crdInstaller.forceConflicts=true`
is only for that deliberate, one-off action, not a recommended fix; the chart never
adopts existing bundles silently. Upstream safe-upgrade policies do not block a
standard bundle from replacing experimental schemas. Do not change channels or
bypass those policies. Helm rollback does not undo CRD changes. A failed hook Job
and its RBAC remain after uninstall or disabling the hook; see the
[chart README](charts/cloudflared-gateway/README.md#optional-gateway-api-crd-install-hook)
for log and cleanup commands.

Mapping and limitations:

| XBackend spec | Behavior |
|---------------|----------|
| `protocol: HTTP`/`HTTP11` | HTTP origin |
| `protocol: HTTP2`/`H2C` | HTTP/2 origin |
| `protocol: TCP` (with `tls.mode: None` or unset) | `tcp://` origin |
| `protocol: TCP` + `tls.mode != None` | **Unsupported** (cloudflared's `tcp://` proxy cannot verify origin TLS) — route reports `ResolvedRefs=False` (`UnsupportedProtocol`) |
| `protocol: MCP` | **Unsupported** — route reports `ResolvedRefs=False` (`UnsupportedProtocol`) |
| `tls.mode: None` | Plain HTTP origin |
| `tls.mode: ServerOnly` | HTTPS origin, server certificate verified against system CAs; `validation.hostname` becomes the SNI server name |
| `tls.mode: ServerOnly` + `validation.caCertificateRefs` | **Unsupported** (a custom CA pool isn't provisioned into cloudflared; use `wellKnownCACertificates: System`) — route reports `ResolvedRefs=False` (`UnsupportedCACerts`) |
| `tls.mode: ServerOnly` + `validation.subjectAltNames` | **Unsupported** (custom SAN matching cannot be enforced) — route reports `ResolvedRefs=False` (`UnsupportedTLSValidation`) and XBackend reports `Accepted=False` |
| `tls.mode: ClientAndServer` | **Unsupported** (cloudflared cannot present an origin client certificate) — route reports `ResolvedRefs=False` (`UnsupportedProtocol`) |

The `XBackend` must match the transport of the referencing route kind. HTTPRoute
and GRPCRoute reject `protocol: TCP`. A `TCPRoute` needs a `tcp://` origin
(`protocol: TCP`, or unset). A `TLSRoute` needs either `tls.mode: ServerOnly`
(an `https://` origin) or `protocol: TCP` (TLS stream passthrough). A mismatch
reports `ResolvedRefs=False` (`IncompatibleRouteKind`). HTTPRoute serves 500;
other route kinds use 503. This condition belongs to the route, not the
`XBackend`'s `Accepted` condition.

Each route rule uses only its first `backendRef` (`backendRefs[0]`); additional backends and `weight` are ignored, since a Cloudflare ingress rule maps to a single origin service. Weighted/multi-backend external origins are not supported.

TLS `validation.hostname` controls certificate verification and SNI, not the
HTTP Host header. For an origin that requires its own hostname, set an HTTPRoute
`URLRewrite` hostname filter, as in `examples/xbackend.yaml`.

Cross-namespace `XBackend` references require a `ReferenceGrant` in the backend's namespace (`to.group: gateway.networking.x-k8s.io`, `to.kind: XBackend`); otherwise the route reports `ResolvedRefs=False` with reason `RefNotPermitted`. XBackends report ancestor status under `status.ancestors[]`.

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
