# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Is

A Kubernetes Gateway API controller that provisions Cloudflare Tunnels. It watches Gateway, HTTPRoute, and TLSRoute resources, creates/manages Cloudflare tunnels via their API, deploys `cloudflared` pods, and pushes ingress configurations. The controller name is `jan0ski.net/cf-tunnel-controller`.

## Build & Test

```sh
go build ./...     # Build
go test ./...      # Run all tests
go vet ./...       # Static analysis
```

## Architecture

**Gateway-primary reconciliation**: The controller-runtime `Controller` watches `Gateway` as its primary resource. Secondary watches on `GatewayClass`, `HTTPRoute`, and `TLSRoute` map changes back to the parent Gateway for re-reconciliation.

### Reconciliation flow (reconciler.go)

1. Fetch Gateway by request NamespacedName
2. Validate GatewayClass controller name (before finalizer to avoid claiming other controllers' Gateways)
3. Add/remove finalizer (`cloudflare-tunnel-controller.jan0ski.net/cleanup`)
4. Ensure K8s Secret exists with 32-byte tunnel secret
5. Create or retrieve Cloudflare tunnel (delete+recreate if secret was regenerated)
6. Assemble tunnel token: `base64(json({"a": account_id, "t": tunnel_id, "s": base64(secret)}))`
7. Store token in Secret via `stringData` (not `data`, to avoid double-encoding)
8. Apply cloudflared Deployment (2 replicas, token from Secret env var)
9. Collect attached HTTPRoutes and TLSRoutes, validating attachment rules
10. Convert routes to Cloudflare ingress rules + catch-all 404, PUT to Cloudflare API
11. Patch status on Gateway, GatewayClass, and all routes

Cleanup (on Gateway deletion) is best-effort: continues through individual failures and reports the first error.

### Module layout

- **`internal/controller/`** — Controller setup, reconciler, attachment validation, status patching, secret management, deployment builder
- **`internal/cloudflare/`** — `APIClient` interface + real impl, ingress rule building, tunnel token assembly
- **`examples/`** — Example Gateway API resources

### Key abstractions

- **`APIClient` interface** — Tunnel CRUD + config push. Real impl wraps `cloudflare-go`; tests use a mock with call recording.
- **`tunnelReconciler`** — Holds `client.Client`, `APIClient`, controller name. Implements `reconcile.Reconciler`.
- **Finalizer** (`cloudflare-tunnel-controller.jan0ski.net/cleanup`) — Ensures tunnel + deployment + secret are cleaned up before Gateway deletion.

### Route-to-ingress mapping

- HTTPRoute: hostname + path combinations → `http://service.namespace:port`
- TLSRoute: SNI hostname only → `https://service.namespace:port` with `noTLSVerify: true`, port defaults to 443
- Cloudflare path field uses regex: prefix `/foo` becomes `^/foo`, exact `/foo` becomes `^/foo$`, prefix `/` omits path (empty = match all)
- Missing backend refs produce `http_status:503`
- HTTPRoute filters: `URLRewrite` hostname and `RequestHeaderModifier` set Host both map to Cloudflare's `originRequest.httpHostHeader`. Other filter types are ignored (Cloudflare tunnels don't support arbitrary header modification).

## Environment

Requires `CLOUDFLARE_ACCOUNT_ID` and `CLOUDFLARE_API_TOKEN` env vars at runtime.

## Testing

Unit tests cover:
- `BuildTunnelToken` — token assembly
- `BuildIngressRules` — HTTPRoute to Cloudflare ingress conversion (paths, hostnames, filters)
- `BuildTLSIngressRules` — TLSRoute to Cloudflare ingress conversion
- Reconciler flow — tunnel creation, config push, cleanup, attachment validation
- Mock `APIClient` with call recording for assertion
