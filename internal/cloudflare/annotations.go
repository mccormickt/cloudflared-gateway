package cloudflare

import (
	"fmt"
	"strconv"
	"time"
)

const annotationPrefix = "tunnels.cloudflare.com/"

// ParseOriginAnnotations extracts Cloudflare-specific origin request configuration
// from resource annotations with the tunnels.cloudflare.com/ prefix.
// Returns the config (or nil if no valid annotations found) and any warnings
// for annotations that were recognized but had invalid values.
func ParseOriginAnnotations(annotations map[string]string) (*OriginRequest, []string) {
	if len(annotations) == 0 {
		return nil, nil
	}

	var cfg OriginRequest
	var hasConfig bool
	var warnings []string

	if v, ok := annotations[annotationPrefix+"proxy-type"]; ok {
		cfg.ProxyType = &v
		hasConfig = true
	}
	if v, ok := annotations[annotationPrefix+"disable-chunked-encoding"]; ok {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.DisableChunkedEncoding = &b
			hasConfig = true
		} else {
			warnings = append(warnings, fmt.Sprintf("invalid disable-chunked-encoding value %q: %v", v, err))
		}
	}
	if v, ok := annotations[annotationPrefix+"keep-alive-connections"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.KeepAliveConnections = &n
			hasConfig = true
		} else {
			warnings = append(warnings, fmt.Sprintf("invalid keep-alive-connections value %q: %v", v, err))
		}
	}
	if v, ok := annotations[annotationPrefix+"keep-alive-timeout"]; ok {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.KeepAliveTimeout = &d
			hasConfig = true
		} else {
			warnings = append(warnings, fmt.Sprintf("invalid keep-alive-timeout value %q: %v", v, err))
		}
	}
	if v, ok := annotations[annotationPrefix+"no-happy-eyeballs"]; ok {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.NoHappyEyeballs = &b
			hasConfig = true
		} else {
			warnings = append(warnings, fmt.Sprintf("invalid no-happy-eyeballs value %q: %v", v, err))
		}
	}

	if !hasConfig {
		return nil, warnings
	}
	return &cfg, warnings
}

// MergeOriginRequest merges annotation-based config into an existing OriginRequest.
// Annotation values do NOT override values already set by filters or policies.
func MergeOriginRequest(base *OriginRequest, annotations *OriginRequest) *OriginRequest {
	if annotations == nil {
		return base
	}
	if base == nil {
		return annotations
	}
	if base.ProxyType == nil && annotations.ProxyType != nil {
		base.ProxyType = annotations.ProxyType
	}
	if base.DisableChunkedEncoding == nil && annotations.DisableChunkedEncoding != nil {
		base.DisableChunkedEncoding = annotations.DisableChunkedEncoding
	}
	if base.KeepAliveConnections == nil && annotations.KeepAliveConnections != nil {
		base.KeepAliveConnections = annotations.KeepAliveConnections
	}
	if base.KeepAliveTimeout == nil && annotations.KeepAliveTimeout != nil {
		base.KeepAliveTimeout = annotations.KeepAliveTimeout
	}
	if base.NoHappyEyeballs == nil && annotations.NoHappyEyeballs != nil {
		base.NoHappyEyeballs = annotations.NoHappyEyeballs
	}
	return base
}
