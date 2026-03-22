package cloudflare

import (
	"fmt"
	"strconv"
	"time"

	cf "github.com/cloudflare/cloudflare-go"
)

const annotationPrefix = "tunnels.cloudflare.com/"

// ParseOriginAnnotations extracts Cloudflare-specific origin request configuration
// from resource annotations with the tunnels.cloudflare.com/ prefix.
// Returns the config (or nil if no valid annotations found) and any warnings
// for annotations that were recognized but had invalid values.
func ParseOriginAnnotations(annotations map[string]string) (*cf.OriginRequestConfig, []string) {
	if len(annotations) == 0 {
		return nil, nil
	}

	var cfg cf.OriginRequestConfig
	var hasConfig bool
	var warnings []string

	if v, ok := annotations[annotationPrefix+"proxy-type"]; ok {
		cfg.ProxyType = &v
		hasConfig = true
	}
	if v, ok := annotations[annotationPrefix+"bastion-mode"]; ok {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.BastionMode = &b
			hasConfig = true
		} else {
			warnings = append(warnings, fmt.Sprintf("invalid bastion-mode value %q: %v", v, err))
		}
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
			td := cf.TunnelDuration{Duration: d}
			cfg.KeepAliveTimeout = &td
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

// MergeOriginRequest merges annotation-based config into an existing OriginRequestConfig.
// Annotation values do NOT override values already set by filters or policies.
func MergeOriginRequest(base *cf.OriginRequestConfig, annotations *cf.OriginRequestConfig) *cf.OriginRequestConfig {
	if annotations == nil {
		return base
	}
	if base == nil {
		return annotations
	}
	if base.ProxyType == nil && annotations.ProxyType != nil {
		base.ProxyType = annotations.ProxyType
	}
	if base.BastionMode == nil && annotations.BastionMode != nil {
		base.BastionMode = annotations.BastionMode
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
