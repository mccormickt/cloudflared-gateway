package cloudflare

// MergeOriginRequest fills unset fields of base from override and returns base.
// Fields already set on base are NOT overridden — base wins. Used both to layer
// a route-level policy over a Gateway-level default (base=route, override=gateway)
// and to apply a resolved policy onto a rule whose fields come from filters,
// BackendTLSPolicy, or CloudflareAccessPolicy (base=rule, override=policy).
func MergeOriginRequest(base *OriginRequest, override *OriginRequest) *OriginRequest {
	if override == nil {
		return base
	}
	if base == nil {
		return override
	}
	if base.ConnectTimeout == nil {
		base.ConnectTimeout = override.ConnectTimeout
	}
	if base.TLSTimeout == nil {
		base.TLSTimeout = override.TLSTimeout
	}
	if base.TCPKeepAlive == nil {
		base.TCPKeepAlive = override.TCPKeepAlive
	}
	if base.NoHappyEyeballs == nil {
		base.NoHappyEyeballs = override.NoHappyEyeballs
	}
	if base.KeepAliveConnections == nil {
		base.KeepAliveConnections = override.KeepAliveConnections
	}
	if base.KeepAliveTimeout == nil {
		base.KeepAliveTimeout = override.KeepAliveTimeout
	}
	if base.HTTPHostHeader == nil {
		base.HTTPHostHeader = override.HTTPHostHeader
	}
	if base.OriginServerName == nil {
		base.OriginServerName = override.OriginServerName
	}
	if base.NoTLSVerify == nil {
		base.NoTLSVerify = override.NoTLSVerify
	}
	if base.DisableChunkedEncoding == nil {
		base.DisableChunkedEncoding = override.DisableChunkedEncoding
	}
	if base.ProxyType == nil {
		base.ProxyType = override.ProxyType
	}
	if base.HTTP2Origin == nil {
		base.HTTP2Origin = override.HTTP2Origin
	}
	if base.MatchSNIToHost == nil {
		base.MatchSNIToHost = override.MatchSNIToHost
	}
	if base.Access == nil {
		base.Access = override.Access
	}
	return base
}
