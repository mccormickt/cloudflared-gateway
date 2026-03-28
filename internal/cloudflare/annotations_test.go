package cloudflare

import (
	"testing"
	"time"

	cf "github.com/cloudflare/cloudflare-go"
)

func TestParseOriginAnnotations_ProxyType(t *testing.T) {
	annotations := map[string]string{
		"tunnels.cloudflare.com/proxy-type": "socks",
	}
	cfg, warnings := ParseOriginAnnotations(annotations)
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.ProxyType == nil || *cfg.ProxyType != "socks" {
		t.Errorf("proxyType: got %v, want socks", cfg.ProxyType)
	}
}

func TestParseOriginAnnotations_BastionMode(t *testing.T) {
	annotations := map[string]string{
		"tunnels.cloudflare.com/bastion-mode": "true",
	}
	cfg, warnings := ParseOriginAnnotations(annotations)
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.BastionMode == nil || !*cfg.BastionMode {
		t.Errorf("bastionMode: got %v, want true", cfg.BastionMode)
	}
}

func TestParseOriginAnnotations_Multiple(t *testing.T) {
	annotations := map[string]string{
		"tunnels.cloudflare.com/proxy-type":               "socks",
		"tunnels.cloudflare.com/bastion-mode":             "true",
		"tunnels.cloudflare.com/disable-chunked-encoding": "true",
		"tunnels.cloudflare.com/keep-alive-connections":   "10",
		"tunnels.cloudflare.com/keep-alive-timeout":       "30s",
		"tunnels.cloudflare.com/no-happy-eyeballs":        "false",
	}
	cfg, warnings := ParseOriginAnnotations(annotations)
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.ProxyType == nil || *cfg.ProxyType != "socks" {
		t.Errorf("proxyType: got %v", cfg.ProxyType)
	}
	if cfg.BastionMode == nil || !*cfg.BastionMode {
		t.Errorf("bastionMode: got %v", cfg.BastionMode)
	}
	if cfg.DisableChunkedEncoding == nil || !*cfg.DisableChunkedEncoding {
		t.Errorf("disableChunkedEncoding: got %v", cfg.DisableChunkedEncoding)
	}
	if cfg.KeepAliveConnections == nil || *cfg.KeepAliveConnections != 10 {
		t.Errorf("keepAliveConnections: got %v", cfg.KeepAliveConnections)
	}
	if cfg.KeepAliveTimeout == nil || cfg.KeepAliveTimeout.Duration != 30*time.Second {
		t.Errorf("keepAliveTimeout: got %v", cfg.KeepAliveTimeout)
	}
	if cfg.NoHappyEyeballs == nil || *cfg.NoHappyEyeballs {
		t.Errorf("noHappyEyeballs: got %v, want false", cfg.NoHappyEyeballs)
	}
}

func TestParseOriginAnnotations_Empty(t *testing.T) {
	cfg, _ := ParseOriginAnnotations(nil)
	if cfg != nil {
		t.Errorf("expected nil for nil annotations, got %+v", cfg)
	}

	cfg, _ = ParseOriginAnnotations(map[string]string{})
	if cfg != nil {
		t.Errorf("expected nil for empty annotations, got %+v", cfg)
	}
}

func TestParseOriginAnnotations_InvalidBool(t *testing.T) {
	annotations := map[string]string{
		"tunnels.cloudflare.com/bastion-mode": "not-a-bool",
	}
	cfg, warnings := ParseOriginAnnotations(annotations)
	if cfg != nil {
		t.Errorf("expected nil for invalid bool, got %+v", cfg)
	}
	if len(warnings) != 1 {
		t.Errorf("expected 1 warning, got %d: %v", len(warnings), warnings)
	}
}

func TestParseOriginAnnotations_InvalidInt(t *testing.T) {
	annotations := map[string]string{
		"tunnels.cloudflare.com/keep-alive-connections": "not-a-number",
	}
	cfg, warnings := ParseOriginAnnotations(annotations)
	if cfg != nil {
		t.Errorf("expected nil for invalid int, got %+v", cfg)
	}
	if len(warnings) != 1 {
		t.Errorf("expected 1 warning, got %d: %v", len(warnings), warnings)
	}
}

func TestParseOriginAnnotations_InvalidDuration(t *testing.T) {
	annotations := map[string]string{
		"tunnels.cloudflare.com/keep-alive-timeout": "not-a-duration",
	}
	cfg, warnings := ParseOriginAnnotations(annotations)
	if cfg != nil {
		t.Errorf("expected nil for invalid duration, got %+v", cfg)
	}
	if len(warnings) != 1 {
		t.Errorf("expected 1 warning, got %d: %v", len(warnings), warnings)
	}
}

func TestParseOriginAnnotations_UnrelatedAnnotationsIgnored(t *testing.T) {
	annotations := map[string]string{
		"kubernetes.io/ingress.class":           "nginx",
		"tunnels.cloudflare.com/proxy-type":     "socks",
		"something-else.cloudflare.com/bastion": "true",
	}
	cfg, warnings := ParseOriginAnnotations(annotations)
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.ProxyType == nil || *cfg.ProxyType != "socks" {
		t.Errorf("proxyType: got %v, want socks", cfg.ProxyType)
	}
	// Only proxy-type should be set
	if cfg.BastionMode != nil {
		t.Errorf("bastionMode should be nil, got %v", cfg.BastionMode)
	}
}

func TestMergeOriginRequest_AnnotationsDontOverride(t *testing.T) {
	existingHost := "existing.example.com"
	existingProxyType := ""
	base := &cf.OriginRequestConfig{
		HTTPHostHeader: &existingHost,
		ProxyType:      &existingProxyType,
	}

	annoProxyType := "socks"
	annoBastionMode := true
	annotations := &cf.OriginRequestConfig{
		ProxyType:   &annoProxyType,
		BastionMode: &annoBastionMode,
	}

	result := MergeOriginRequest(base, annotations)

	// ProxyType was already set in base — should not be overridden
	if result.ProxyType == nil || *result.ProxyType != "" {
		t.Errorf("proxyType should remain %q, got %v", existingProxyType, result.ProxyType)
	}
	// BastionMode was nil in base — should be set from annotations
	if result.BastionMode == nil || !*result.BastionMode {
		t.Errorf("bastionMode should be true, got %v", result.BastionMode)
	}
	// HTTPHostHeader was already set — should remain unchanged
	if result.HTTPHostHeader == nil || *result.HTTPHostHeader != "existing.example.com" {
		t.Errorf("httpHostHeader should remain, got %v", result.HTTPHostHeader)
	}
}

func TestMergeOriginRequest_NilBase(t *testing.T) {
	proxyType := "socks"
	annotations := &cf.OriginRequestConfig{
		ProxyType: &proxyType,
	}

	result := MergeOriginRequest(nil, annotations)
	if result != annotations {
		t.Error("nil base should return annotations directly")
	}
}

func TestMergeOriginRequest_NilAnnotations(t *testing.T) {
	host := "example.com"
	base := &cf.OriginRequestConfig{
		HTTPHostHeader: &host,
	}

	result := MergeOriginRequest(base, nil)
	if result != base {
		t.Error("nil annotations should return base directly")
	}
}

func TestMergeOriginRequest_BothNil(t *testing.T) {
	result := MergeOriginRequest(nil, nil)
	if result != nil {
		t.Errorf("both nil should return nil, got %+v", result)
	}
}
