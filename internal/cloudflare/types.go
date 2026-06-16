package cloudflare

import (
	"strings"
	"time"
)

// Tunnel is the controller's view of a Cloudflare tunnel. It carries only the
// fields the controller needs, keeping callers free of any SDK type.
type Tunnel struct {
	ID   string
	Name string
}

// Zone is the controller's view of a Cloudflare DNS zone, used to route a
// hostname to the zone that should hold its record.
type Zone struct {
	ID   string
	Name string
}

// DNSRecord is the controller's representation of a Cloudflare DNS record. Only
// the fields needed to manage proxied CNAMEs for tunnel hostnames are carried;
// the client boundary translates to/from SDK types. Names and CNAME targets are
// normalized (lowercase, no trailing dot) so reads and desired records compare
// cleanly.
type DNSRecord struct {
	ID      string // set when read from the API; empty for desired records
	Name    string // FQDN, normalized
	Type    string // always "CNAME" for records this controller manages
	Content string // CNAME target, e.g. <tunnelID>.cfargotunnel.com
	Proxied bool
	Comment string // ownership marker, e.g. "cloudflared-gateway:owner=<gateway-uid>"
}

// DNSChanges is a planned set of DNS record mutations applied as a single
// batched, transactional request per zone (mirrors Cloudflare's
// dns_records/batch endpoint). Updates and Deletes carry record IDs from a prior
// read; Creates do not.
type DNSChanges struct {
	Creates []DNSRecord
	Updates []DNSRecord
	Deletes []DNSRecord
}

// Empty reports whether the change set has no mutations.
func (c DNSChanges) Empty() bool {
	return len(c.Creates) == 0 && len(c.Updates) == 0 && len(c.Deletes) == 0
}

// NormalizeDNSName lowercases a DNS name and strips any trailing dot and
// surrounding whitespace, so that case- or dot-only differences between a
// desired record and an API read-back do not produce spurious diffs.
func NormalizeDNSName(name string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
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

// ResolvedBackend is the controller-side resolution of an external (XBackend)
// backendRef: the fully-formed tunnel service URL plus the originRequest deltas
// the backend's spec implies (TLS server name, NoTLSVerify, HTTP2Origin). An
// empty Service means the ref was recognized as an XBackend but cannot be served
// — missing, an unsupported protocol/TLS mode, or the feature is disabled — and
// the builder emits http_status:500 for HTTPRoute or http_status:503 otherwise.
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

// BackendResolver resolves a route backendRef. It returns ok=false for a valid
// Service ref that the builder should turn into an in-cluster URL; ok=true for
// an external origin or a rejected ref. An empty Service with ok=true fails
// closed. The controller supplies the implementation with the backend objects
// and ReferenceGrant permissions collected for the reconcile.
type BackendResolver func(ref BackendRef) (ResolvedBackend, bool)

// NilResolver declines every ref, so all backendRefs use the native Service path.
// It is used by tests that do not need backend validation.
func NilResolver(ref BackendRef) (ResolvedBackend, bool) {
	return ResolvedBackend{}, false
}
