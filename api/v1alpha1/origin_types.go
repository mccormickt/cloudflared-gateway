package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=cloudflare
//
// CloudflareOriginPolicy is an Inherited Policy (per GEP-713) that tunes the
// originRequest settings cloudflared uses when connecting to backends. A policy
// targeting a Gateway acts as a default for every route attached to it; a policy
// targeting a route overrides the Gateway-level default for that route.
//
// It replaces the former tunnels.cloudflare.com/* route annotations. Fields
// owned by other mechanisms — Access (CloudflareAccessPolicy), origin TLS
// (BackendTLSPolicy), httpHostHeader (HTTPRoute filters), connectTimeout
// (HTTPRoute timeouts) — are intentionally not exposed here.
type CloudflareOriginPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CloudflareOriginPolicySpec `json:"spec"`
	// +optional
	Status gwapiv1.PolicyStatus `json:"status,omitempty"`
}

type CloudflareOriginPolicySpec struct {
	// TargetRefs identifies the Gateway API resources this policy applies to.
	// Supported kinds: Gateway, HTTPRoute, GRPCRoute, TLSRoute, TCPRoute.
	//
	// +listType=map
	// +listMapKey=group
	// +listMapKey=kind
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	TargetRefs []gwapiv1.LocalPolicyTargetReference `json:"targetRefs"`

	// ProxyType selects the cloudflared proxy mode. Empty means a regular HTTP
	// proxy; "socks" runs a SOCKS5 proxy.
	// +kubebuilder:validation:Enum=socks
	// +optional
	ProxyType *string `json:"proxyType,omitempty"`

	// DisableChunkedEncoding disables chunked transfer encoding to the origin.
	// +optional
	DisableChunkedEncoding *bool `json:"disableChunkedEncoding,omitempty"`

	// KeepAliveConnections is the maximum number of idle keepalive connections
	// to the origin.
	// +kubebuilder:validation:Minimum=0
	// +optional
	KeepAliveConnections *int32 `json:"keepAliveConnections,omitempty"`

	// KeepAliveTimeout is how long an idle keepalive connection is kept open.
	// +optional
	KeepAliveTimeout *metav1.Duration `json:"keepAliveTimeout,omitempty"`

	// NoHappyEyeballs disables RFC 8305 Happy Eyeballs for IPv4/IPv6 fallback.
	// +optional
	NoHappyEyeballs *bool `json:"noHappyEyeballs,omitempty"`

	// TLSTimeout is the timeout for completing a TLS handshake to the origin.
	// +optional
	TLSTimeout *metav1.Duration `json:"tlsTimeout,omitempty"`

	// TCPKeepAlive is the interval between TCP keepalive packets to the origin.
	// +optional
	TCPKeepAlive *metav1.Duration `json:"tcpKeepAlive,omitempty"`

	// HTTP2Origin makes cloudflared connect to the origin over HTTP/2. GRPCRoutes
	// always use HTTP/2 regardless of this setting.
	// +optional
	HTTP2Origin *bool `json:"http2Origin,omitempty"`

	// MatchSNIToHost routes by matching the TLS SNI to the origin host.
	// +optional
	MatchSNIToHost *bool `json:"matchSNIToHost,omitempty"`
}

// +kubebuilder:object:root=true
type CloudflareOriginPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CloudflareOriginPolicy `json:"items"`
}
