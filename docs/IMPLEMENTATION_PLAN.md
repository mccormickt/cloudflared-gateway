# Cloudflare Tunnel Controller — Feature Implementation Plan

Multi-phase plan to implement full Gateway API v1.4.1 support mapped to Cloudflare tunnel capabilities. Each phase is independent and can be parallelized as an agent swarm task.

---

## Phase 1: TCPRoute Support

**Difficulty:** Low — same pattern as TLSRoute
**Dependencies:** None
**Agent scope:** Single agent

### What it does
Maps Gateway API TCPRoute (v1alpha2) to Cloudflare `tcp://service:port` ingress rules. Cloudflare tunnels natively support TCP proxying over WebSocket.

### Files to create/modify
- `internal/cloudflare/ingress.go` — Add `BuildTCPIngressRules(routes []v1alpha2.TCPRoute) []cf.UnvalidatedIngressRule`
- `internal/cloudflare/ingress_test.go` — Tests for TCP ingress rule building
- `internal/controller/controller.go` — Add TCPRoute watch with `routeToGateways` mapper
- `internal/controller/reconciler.go` — Add `collectTCPRoutes()`, include in ingress assembly
- `internal/controller/attachment.go` — TCP protocol already handled via `defaultKindForProtocol`
- `internal/controller/status.go` — Add `PatchTCPRouteStatus` (same pattern as TLSRoute)
- `internal/controller/controller_test.go` — Reconciler test with TCPRoute
- `tests/integration/integration_test.go` — envtest TCPRoute test

### Implementation details
```go
func BuildTCPIngressRules(routes []v1alpha2.TCPRoute) []cf.UnvalidatedIngressRule
```
- TCPRoute has no hostname or path matching — it's port-based only
- Service URL: `tcp://service.namespace:port`
- Port defaults to the listener port if not specified on BackendRef
- No `originRequest` needed (no TLS, no host rewrite)
- The catch: TCPRoute has no hostnames field. The hostname comes from the Gateway listener. The reconciler must associate TCPRoute rules with the listener hostname.

### Listener hostname association
When a TCPRoute attaches to a Gateway listener that has a hostname, use that hostname for the ingress rule. When the listener has no hostname, emit a rule with no hostname (catch-all for that service).

### Tests
- `TestBuildTCPIngressRules_BasicRoute` — single backend, correct `tcp://` URL
- `TestBuildTCPIngressRules_DefaultPort` — no port specified
- `TestBuildTCPIngressRules_NoBackendRef` — produces 503
- `TestReconcile_TCPRoute` — reconciler collects and includes TCP rules
- `TestIntegration_TCPRoute` — envtest with TCPRoute resource

---

## Phase 2: GRPCRoute Support

**Difficulty:** Low-Medium — needs `http2Origin` flag
**Dependencies:** None
**Agent scope:** Single agent

### What it does
Maps Gateway API GRPCRoute (v1 stable) to Cloudflare ingress rules with `http2Origin: true` in `originRequest`. GRPCRoute matches on gRPC service and method names.

### Files to create/modify
- `internal/cloudflare/ingress.go` — Add `BuildGRPCIngressRules(routes []v1.GRPCRoute) []cf.UnvalidatedIngressRule`
- `internal/cloudflare/ingress_test.go` — Tests for gRPC ingress rules
- `internal/controller/controller.go` — Add GRPCRoute watch
- `internal/controller/reconciler.go` — Add `collectGRPCRoutes()`, include in ingress assembly
- `internal/controller/status.go` — Add `PatchGRPCRouteStatus`
- `internal/controller/attachment.go` — Add gRPC to `defaultKindForProtocol` (HTTP/HTTPS protocols)
- `internal/controller/controller_test.go` — Reconciler test with GRPCRoute
- `tests/integration/integration_test.go` — envtest GRPCRoute test

### Implementation details
```go
func BuildGRPCIngressRules(routes []v1.GRPCRoute) []cf.UnvalidatedIngressRule
```
- GRPCRoute matches on service name and method name, not URL paths
- Map gRPC service/method to path: `/package.ServiceName/MethodName` → Cloudflare path regex `^/package.ServiceName/MethodName$`
- If only service specified (no method): `^/package.ServiceName/`
- If no match specified: match all paths for the hostname
- Service URL: `http://service.namespace:port` (gRPC uses HTTP/2)
- `originRequest.http2Origin = true` on every gRPC ingress rule
- Port defaults to 80 (h2c) unless HTTPS listener, then 443

### GRPCRoute match types
```go
type GRPCRouteMatch struct {
    Method  *GRPCMethodMatch
    Headers []GRPCHeaderMatch
}
type GRPCMethodMatch struct {
    Type    *GRPCMethodMatchType  // Exact or RegularExpression
    Service *string
    Method  *string
}
```

### Tests
- `TestBuildGRPCIngressRules_ServiceAndMethod` — exact match produces correct path regex
- `TestBuildGRPCIngressRules_ServiceOnly` — prefix match on service
- `TestBuildGRPCIngressRules_NoMatch` — matches all paths
- `TestBuildGRPCIngressRules_Http2Origin` — verify `http2Origin: true` set
- `TestBuildGRPCIngressRules_MultipleHostnames` — hostname × match combinations

---

## Phase 3: BackendTLSPolicy Support

**Difficulty:** Medium — needs policy-to-backend association
**Dependencies:** None
**Agent scope:** Single agent

### What it does
Maps Gateway API BackendTLSPolicy (v1 stable) to Cloudflare `originRequest` TLS fields: `originServerName`, `caPool`, `noTLSVerify`. Replaces the current hardcoded `noTLSVerify: true` on TLS routes.

### Files to create/modify
- `internal/controller/backendtls.go` — New file: `GetBackendTLSConfig(ctx, client, backendRef) *cf.OriginRequestConfig`
- `internal/controller/backendtls_test.go` — Unit tests
- `internal/cloudflare/ingress.go` — Modify ingress builders to accept optional TLS config per backend
- `internal/controller/reconciler.go` — Look up BackendTLSPolicy for each backend ref, pass to ingress builders
- `internal/controller/controller.go` — Watch BackendTLSPolicy, map to parent Gateway
- `tests/integration/integration_test.go` — envtest with BackendTLSPolicy

### Implementation details
```go
func GetBackendTLSConfig(ctx context.Context, c client.Client, serviceNS, serviceName string) (*cf.OriginRequestConfig, error)
```

1. List all `BackendTLSPolicy` resources
2. Find policies targeting the given service (via `targetRefs`)
3. For matching policy:
   - `validation.hostname` → `originRequest.originServerName`
   - `validation.caCertificateRefs` → fetch ConfigMap/Secret, write CA cert, set `originRequest.caPool` (path or inline)
   - `validation.wellKnownCACertificates: "SYSTEM"` → don't set `caPool` (use system CAs)
   - If no policy exists → `originRequest.noTLSVerify: true` (current behavior, backward compatible)

### Cloudflare field mapping
| BackendTLSPolicy | OriginRequestConfig |
|---|---|
| `validation.hostname` | `originServerName` |
| `validation.caCertificateRefs` | `caPool` (CA cert content) |
| `wellKnownCACertificates: SYSTEM` | (no caPool, no noTLSVerify — use system CAs) |
| No policy | `noTLSVerify: true` (backward compat) |

### Tests
- `TestGetBackendTLSConfig_WithHostname` — sets originServerName
- `TestGetBackendTLSConfig_WithCACert` — fetches cert from Secret, sets caPool
- `TestGetBackendTLSConfig_SystemCAs` — uses system CAs (no noTLSVerify)
- `TestGetBackendTLSConfig_NoPolicyFallback` — defaults to noTLSVerify
- `TestIntegration_BackendTLSPolicy` — envtest with policy targeting service

---

## Phase 4: HTTPRoute Timeouts

**Difficulty:** Low — direct field mapping
**Dependencies:** None
**Agent scope:** Single agent (can combine with another small phase)

### What it does
Maps HTTPRoute `timeouts.request` and `timeouts.backendRequest` to Cloudflare `originRequest.connectTimeout` and response timeout behavior.

### Files to modify
- `internal/cloudflare/ingress.go` — Extract timeouts from `HTTPRouteRule.Timeouts`, add to `originRequest`
- `internal/cloudflare/ingress_test.go` — Timeout tests

### Implementation details
In `BuildIngressRules`, for each rule:
```go
if rule.Timeouts != nil {
    if rule.Timeouts.BackendRequest != nil {
        // Parse Gateway API Duration format (e.g., "10s", "1m")
        // Map to originRequest.connectTimeout
    }
}
```

| HTTPRoute Field | OriginRequestConfig Field |
|---|---|
| `timeouts.backendRequest` | `connectTimeout` |
| `timeouts.request` | No direct mapping (Cloudflare handles this at the edge) |

### Tests
- `TestBuildIngressRules_BackendTimeout` — sets connectTimeout
- `TestBuildIngressRules_NoTimeout` — no originRequest timeout fields

---

## Phase 5: Cloudflare Access Integration via ExtensionRef

**Difficulty:** High — requires custom CRD design
**Dependencies:** None (but benefits from Phase 3 patterns)
**Agent scope:** Single agent

### What it does
Enables per-route Cloudflare Access enforcement using Gateway API's `ExtensionRef` filter mechanism. Creates a custom `CloudflareAccessPolicy` CRD that can be referenced from HTTPRoute filters.

### Design

#### Custom CRD: `CloudflareAccessPolicy`
```yaml
apiVersion: cloudflare.jan0ski.net/v1alpha1
kind: CloudflareAccessPolicy
metadata:
  name: require-access
  namespace: default
spec:
  teamName: my-org
  required: true
  audTag:
    - "abc123..."
```

#### Usage in HTTPRoute
```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
spec:
  rules:
    - filters:
        - type: ExtensionRef
          extensionRef:
            group: cloudflare.jan0ski.net
            kind: CloudflareAccessPolicy
            name: require-access
      backendRefs:
        - name: protected-svc
          port: 80
```

### Files to create/modify
- `api/v1alpha1/types.go` — `CloudflareAccessPolicy` CRD type definition
- `api/v1alpha1/groupversion_info.go` — Scheme registration
- `api/v1alpha1/zz_generated.deepcopy.go` — Generated deepcopy (via controller-gen)
- `config/crd/cloudflare.jan0ski.net_cloudflareAccesspolicies.yaml` — CRD manifest
- `internal/cloudflare/ingress.go` — Extract ExtensionRef filters, look up CloudflareAccessPolicy, set `originRequest.access`
- `internal/controller/reconciler.go` — Pass client to ingress builder for CRD lookups
- `internal/controller/controller.go` — Watch CloudflareAccessPolicy, register scheme
- `main.go` — Register custom scheme
- `Makefile` — Add `generate` target for controller-gen
- `tests/integration/integration_test.go` — envtest with CloudflareAccessPolicy

### Implementation flow
1. During ingress rule building, check each rule's filters for `ExtensionRef` with `group=cloudflare.jan0ski.net, kind=CloudflareAccessPolicy`
2. Fetch the referenced `CloudflareAccessPolicy` object
3. Map fields to `originRequest.access`:
   - `spec.required` → `access.required`
   - `spec.teamName` → `access.teamName`
   - `spec.audTag` → `access.audTag`
4. If no ExtensionRef Access filter, no `access` config on the rule

### Tests
- `TestExtensionRef_CloudflareAccess` — ExtensionRef resolves and sets originRequest.access
- `TestExtensionRef_NotFound` — missing policy produces error
- `TestExtensionRef_NonAccessRef` — non-Access ExtensionRef ignored
- `TestIntegration_CloudflareAccessPolicy` — envtest with custom CRD

---

## Phase 6: Gateway Infrastructure Propagation

**Difficulty:** Low
**Dependencies:** None
**Agent scope:** Combine with Phase 4

### What it does
Propagates `gateway.spec.infrastructure.labels` and `gateway.spec.infrastructure.annotations` to the cloudflared Deployment and its pods.

### Files to modify
- `internal/controller/deployment.go` — Read `gw.Spec.Infrastructure`, merge labels/annotations into Deployment and PodTemplate

### Implementation
```go
if gw.Spec.Infrastructure != nil {
    // Merge infrastructure labels into deployment labels
    for k, v := range gw.Spec.Infrastructure.Labels {
        deployment.Labels[string(k)] = string(v)
        deployment.Spec.Template.Labels[string(k)] = string(v)
    }
    // Merge infrastructure annotations
    for k, v := range gw.Spec.Infrastructure.Annotations {
        deployment.Annotations[string(k)] = string(v)
        deployment.Spec.Template.Annotations[string(k)] = string(v)
    }
}
```

### Tests
- `TestBuildDeployment_InfrastructureLabels` — labels propagated to deployment + pod template
- `TestBuildDeployment_NoInfrastructure` — existing behavior unchanged

---

## Phase 7: Cloudflare-Specific Annotations

**Difficulty:** Low-Medium
**Dependencies:** None
**Agent scope:** Single agent

### What it does
Supports Cloudflare-specific origin configuration via annotations on HTTPRoute, TLSRoute, or Gateway resources for features that don't have Gateway API equivalents.

### Annotation prefix: `tunnels.cloudflare.com/`

| Annotation | Value | Maps to |
|---|---|---|
| `tunnels.cloudflare.com/proxy-type` | `socks` | `originRequest.proxyType` |
| `tunnels.cloudflare.com/bastion-mode` | `true` | `originRequest.bastionMode` |
| `tunnels.cloudflare.com/disable-chunked-encoding` | `true` | `originRequest.disableChunkedEncoding` |
| `tunnels.cloudflare.com/keep-alive-connections` | `50` | `originRequest.keepAliveConnections` |
| `tunnels.cloudflare.com/keep-alive-timeout` | `90s` | `originRequest.keepAliveTimeout` |
| `tunnels.cloudflare.com/no-happy-eyeballs` | `true` | `originRequest.noHappyEyeballs` |

### Files to create/modify
- `internal/cloudflare/annotations.go` — `ParseOriginAnnotations(annotations map[string]string) *cf.OriginRequestConfig`
- `internal/cloudflare/annotations_test.go` — Tests
- `internal/cloudflare/ingress.go` — Merge annotation-based config with filter-based config per rule
- `internal/controller/reconciler.go` — Pass route annotations to ingress builders

### Tests
- `TestParseOriginAnnotations_ProxyType` — socks mode
- `TestParseOriginAnnotations_BastionMode` — bastion enabled
- `TestParseOriginAnnotations_Multiple` — multiple annotations merged
- `TestParseOriginAnnotations_Empty` — no annotations returns nil

---

## Phase 8: WARP Private Network Routing

**Difficulty:** High — involves TunnelRoute CIDR management
**Dependencies:** Phase 1 (TCPRoute helps but not required)
**Agent scope:** Single agent

### What it does
Enables Cloudflare WARP routing through the tunnel for private network access. Maps a custom annotation or CRD to `warpRouting.enabled` and manages `TunnelRoute` CIDR entries via the Cloudflare API.

### Design option: Annotation-based
```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  annotations:
    tunnels.cloudflare.com/warp-routing: "true"
    tunnels.cloudflare.com/private-networks: "10.0.0.0/8,192.168.0.0/16"
```

### Files to create/modify
- `internal/cloudflare/client.go` — Add `CreateTunnelRoute`, `DeleteTunnelRoute`, `ListTunnelRoutes` to APIClient interface
- `internal/controller/reconciler.go` — Parse WARP annotations, enable warpRouting in config, manage TunnelRoute CIDRs
- `internal/cloudflare/client.go` — Implement route management methods

### Cloudflare API operations
```go
type APIClient interface {
    // ... existing methods ...
    CreateTunnelRoute(ctx context.Context, tunnelID, network, comment string) error
    DeleteTunnelRoute(ctx context.Context, network string) error
    ListTunnelRoutes(ctx context.Context, tunnelID string) ([]cf.TunnelRoute, error)
}
```

### Reconciliation flow
1. Check for `tunnels.cloudflare.com/warp-routing: "true"` annotation on Gateway
2. If enabled, set `warpRouting.enabled: true` in TunnelConfiguration
3. Parse `tunnels.cloudflare.com/private-networks` annotation for CIDR list
4. Diff desired CIDRs against existing TunnelRoutes
5. Create/delete routes to reach desired state
6. On cleanup: delete all TunnelRoutes for this tunnel

---

## Agent Swarm Execution Plan

### Parallel Group A (no dependencies, run simultaneously)
| Agent | Phase | Est. Files |
|---|---|---|
| Agent 1 | Phase 1: TCPRoute | 8 files |
| Agent 2 | Phase 2: GRPCRoute | 8 files |
| Agent 3 | Phase 3: BackendTLSPolicy | 6 files |
| Agent 4 | Phase 4+6: Timeouts + Infrastructure | 4 files |

### Parallel Group B (after Group A merges)
| Agent | Phase | Est. Files |
|---|---|---|
| Agent 5 | Phase 5: Cloudflare Access CRD | 12 files |
| Agent 6 | Phase 7: Annotations | 4 files |

### Sequential (after Group B)
| Agent | Phase | Est. Files |
|---|---|---|
| Agent 7 | Phase 8: WARP Routing | 4 files |

### Post-implementation
| Agent | Task |
|---|---|
| Agent 8 | Update CLAUDE.md, examples/, conformance test configuration |
| Agent 9 | Run full test suite: `make test-all` |

---

## Feature Support Declaration

After implementation, the controller can declare these Gateway API conformance features:

### Stable
- `Gateway`, `GatewayClass`, `HTTPRoute`, `GRPCRoute`
- `ReferenceGrant`, `BackendTLSPolicy`
- `HTTPRouteHostRewrite`, `HTTPRoutePathRewrite`
- `HTTPRouteBackendTimeout`
- `GatewayInfrastructurePropagation`

### Extended
- `HTTPRouteRequestHeaderModification`
- `HTTPRouteBackendProtocolH2C` (via GRPCRoute)

### Experimental
- `TCPRoute`, `TLSRoute`

### Not Supported (document clearly)
- `UDPRoute` (Cloudflare tunnels don't support UDP)
- `RequestRedirect`, `ResponseHeaderModifier`, `RequestMirror`, `CORS` (edge concerns, not tunnel-level)
- `SessionPersistence`, `XBackendTrafficPolicy` (no tunnel-level support)
- `XMesh` (no service mesh capability)
