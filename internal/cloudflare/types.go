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
