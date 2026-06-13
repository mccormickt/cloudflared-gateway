package cloudflare

import "time"

// Tunnel is the controller's view of a Cloudflare tunnel. It carries only the
// fields the controller needs, keeping callers free of any SDK type.
type Tunnel struct {
	ID   string
	Name string
}

// AccessConfig gates a hostname behind Cloudflare Access. It mirrors the
// originRequest.access object pushed to the tunnel configuration API.
type AccessConfig struct {
	Required bool
	TeamName string
	AudTag   []string
}

// OriginRequest is the controller's representation of per-rule origin settings
// for a tunnel ingress rule. It is a curated subset of the Cloudflare tunnel
// configuration originRequest object — only the fields the controller actually
// sets — and is translated to the SDK type at the client boundary. Keeping this
// type SDK-agnostic means an SDK version bump touches only client.go.
//
// Durations are time.Duration; the client boundary converts them to the integer
// seconds the configuration API expects (sub-second precision is not
// representable in the API).
type OriginRequest struct {
	ConnectTimeout         *time.Duration
	TLSTimeout             *time.Duration
	TCPKeepAlive           *time.Duration
	NoHappyEyeballs        *bool
	KeepAliveConnections   *int
	KeepAliveTimeout       *time.Duration
	HTTPHostHeader         *string
	OriginServerName       *string
	NoTLSVerify            *bool
	DisableChunkedEncoding *bool
	ProxyType              *string
	HTTP2Origin            *bool
	MatchSNIToHost         *bool
	Access                 *AccessConfig
}

// IngressRule is the controller's representation of a single tunnel ingress
// rule, translated to the SDK type at the client boundary.
type IngressRule struct {
	Hostname      string
	Path          string
	Service       string
	OriginRequest *OriginRequest
}

// BackendProtocol mirrors the gateway-api apisx BackendProtocol values, kept here
// so the cloudflare package stays free of the gateway-api dependency.
type BackendProtocol string

const (
	BackendProtocolTCP    BackendProtocol = "TCP"
	BackendProtocolHTTP   BackendProtocol = "HTTP"
	BackendProtocolHTTP2  BackendProtocol = "HTTP2"
	BackendProtocolHTTP11 BackendProtocol = "HTTP11"
	BackendProtocolH2C    BackendProtocol = "H2C"
	BackendProtocolGRPC   BackendProtocol = "GRPC"
	BackendProtocolMCP    BackendProtocol = "MCP"
)

// ResolvedBackend is the controller-side resolution of an external (XBackend)
// backendRef: the fully-formed tunnel service URL plus the originRequest deltas
// the backend's spec implies (TLS server name, NoTLSVerify, HTTP2Origin). An
// empty Service means the ref was recognized as an XBackend but cannot be served
// — missing, an unsupported protocol/TLS mode, or the feature is disabled — and
// the builder emits http_status:503 for it.
type ResolvedBackend struct {
	Service       string
	OriginRequest *OriginRequest
}

// BackendRef is the normalized view of a route backendRef passed to a
// BackendResolver. RouteNamespace and RouteKind identify the referencing route
// (the ReferenceGrant "from" identity); Group/Kind/Namespace/Name/Port identify
// the backend the ref points at, with Namespace already defaulted to the route's
// namespace when the ref omitted it.
type BackendRef struct {
	RouteNamespace string
	RouteKind      string
	Group          string
	Kind           string
	Namespace      string
	Name           string
	Port           *int
}

// BackendResolver resolves a route backendRef that may target an external
// XBackend. It returns ok=false for any ref the cloudflare package should handle
// with its native in-cluster Service logic (i.e. non-XBackend refs); ok=true
// (with a possibly-empty ResolvedBackend.Service) for XBackend refs. The
// controller supplies the implementation, closing over the XBackends it
// pre-fetched for the reconcile.
type BackendResolver func(ref BackendRef) (ResolvedBackend, bool)

// NilResolver declines every ref, so all backendRefs use the native Service path.
// It is the default when experimental backend support is disabled.
func NilResolver(ref BackendRef) (ResolvedBackend, bool) {
	return ResolvedBackend{}, false
}
