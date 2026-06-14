package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	apisxv1alpha1 "sigs.k8s.io/gateway-api/apisx/v1alpha1"
)

func protoPtr(p apisxv1alpha1.BackendProtocol) *apisxv1alpha1.BackendProtocol { return &p }

func makeXBackend(ns, name, host string, port int32, proto *apisxv1alpha1.BackendProtocol, tls *apisxv1alpha1.BackendTLS) *apisxv1alpha1.XBackend {
	return &apisxv1alpha1.XBackend{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: apisxv1alpha1.BackendSpec{
			Type:             apisxv1alpha1.BackendTypeExternalHostname,
			Port:             apisxv1alpha1.BackendPort{Port: apisxv1alpha1.PortNumber(port)},
			ExternalHostname: &apisxv1alpha1.ExternalHostnameBackend{Hostname: gwapiv1.PreciseHostname(host)},
			Protocol:         proto,
			TLS:              tls,
		},
	}
}

func TestTranslateXBackend(t *testing.T) {
	tlsServerOnly := &apisxv1alpha1.BackendTLS{
		Mode:       apisxv1alpha1.BackendTLSModeServerOnly,
		Validation: gwapiv1.BackendTLSPolicyValidation{Hostname: "verify.example.com"},
	}
	tlsNone := &apisxv1alpha1.BackendTLS{Mode: apisxv1alpha1.BackendTLSModeNone}
	tlsMTLS := &apisxv1alpha1.BackendTLS{Mode: apisxv1alpha1.BackendTLSModeClientAndServer}
	tlsCustomCA := &apisxv1alpha1.BackendTLS{
		Mode: apisxv1alpha1.BackendTLSModeServerOnly,
		Validation: gwapiv1.BackendTLSPolicyValidation{
			Hostname:          "verify.example.com",
			CACertificateRefs: []gwapiv1.LocalObjectReference{{Kind: "ConfigMap", Name: "private-ca"}},
		},
	}

	tests := []struct {
		name        string
		xb          *apisxv1alpha1.XBackend
		wantService string
		wantReason  string
		wantHTTP2   bool
		wantSNI     string
	}{
		{
			name:        "http default protocol no tls",
			xb:          makeXBackend("ns", "a", "api.example.com", 80, nil, nil),
			wantService: "http://api.example.com:80",
		},
		{
			name:        "https server-only with sni",
			xb:          makeXBackend("ns", "a", "api.example.com", 443, nil, tlsServerOnly),
			wantService: "https://api.example.com:443",
			wantSNI:     "verify.example.com",
		},
		{
			name:        "tls none stays http",
			xb:          makeXBackend("ns", "a", "api.example.com", 8080, nil, tlsNone),
			wantService: "http://api.example.com:8080",
		},
		{
			name:        "http2 sets http2origin",
			xb:          makeXBackend("ns", "a", "api.example.com", 443, protoPtr(apisxv1alpha1.BackendProtocolHTTP2), tlsServerOnly),
			wantService: "https://api.example.com:443",
			wantHTTP2:   true,
			wantSNI:     "verify.example.com",
		},
		{
			name:        "grpc sets http2origin",
			xb:          makeXBackend("ns", "a", "grpc.example.com", 443, protoPtr(apisxv1alpha1.BackendProtocolGRPC), tlsServerOnly),
			wantService: "https://grpc.example.com:443",
			wantHTTP2:   true,
			wantSNI:     "verify.example.com",
		},
		{
			name:        "tcp protocol",
			xb:          makeXBackend("ns", "a", "tcp.example.com", 5432, protoPtr(apisxv1alpha1.BackendProtocolTCP), nil),
			wantService: "tcp://tcp.example.com:5432",
		},
		{
			name:        "tcp with tls none stays tcp",
			xb:          makeXBackend("ns", "a", "tcp.example.com", 5432, protoPtr(apisxv1alpha1.BackendProtocolTCP), tlsNone),
			wantService: "tcp://tcp.example.com:5432",
		},
		{
			name:       "tcp with server-only tls unsupported",
			xb:         makeXBackend("ns", "a", "tcp.example.com", 5432, protoPtr(apisxv1alpha1.BackendProtocolTCP), tlsServerOnly),
			wantReason: reasonUnsupportedProtocol,
		},
		{
			name:       "mcp unsupported",
			xb:         makeXBackend("ns", "a", "api.example.com", 443, protoPtr(apisxv1alpha1.BackendProtocolMCP), nil),
			wantReason: reasonUnsupportedProtocol,
		},
		{
			name:       "mtls unsupported",
			xb:         makeXBackend("ns", "a", "api.example.com", 443, nil, tlsMTLS),
			wantReason: reasonUnsupportedProtocol,
		},
		{
			name:       "custom ca certs unsupported",
			xb:         makeXBackend("ns", "a", "api.example.com", 443, nil, tlsCustomCA),
			wantReason: reasonUnsupportedCACerts,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rb, reason := translateXBackend(tc.xb)
			if reason != tc.wantReason {
				t.Fatalf("reason: got %q, want %q", reason, tc.wantReason)
			}
			if tc.wantReason != "" {
				return
			}
			if rb.Service != tc.wantService {
				t.Errorf("service: got %q, want %q", rb.Service, tc.wantService)
			}
			gotHTTP2 := rb.OriginRequest != nil && rb.OriginRequest.HTTP2Origin != nil && *rb.OriginRequest.HTTP2Origin
			if gotHTTP2 != tc.wantHTTP2 {
				t.Errorf("http2origin: got %v, want %v", gotHTTP2, tc.wantHTTP2)
			}
			gotSNI := ""
			if rb.OriginRequest != nil && rb.OriginRequest.OriginServerName != nil {
				gotSNI = *rb.OriginRequest.OriginServerName
			}
			if gotSNI != tc.wantSNI {
				t.Errorf("sni: got %q, want %q", gotSNI, tc.wantSNI)
			}
			// Server-cert verification must remain on for external HTTPS.
			if rb.OriginRequest != nil && rb.OriginRequest.NoTLSVerify != nil {
				t.Errorf("NoTLSVerify should not be set for external backends, got %v", *rb.OriginRequest.NoTLSVerify)
			}
		})
	}
}

func TestResolveXBackendRef_Reasons(t *testing.T) {
	xb := makeXBackend("ext", "api", "api.example.com", 443, nil, &apisxv1alpha1.BackendTLS{Mode: apisxv1alpha1.BackendTLSModeServerOnly})

	t.Run("flag off -> InvalidKind", func(t *testing.T) {
		r := &GatewayReconciler{ExperimentalBackends: false}
		_, reason := r.resolveXBackendRef("apps", "HTTPRoute", "apps", "api", nil)
		if reason != reasonInvalidKind {
			t.Errorf("got %q, want InvalidKind", reason)
		}
	})

	t.Run("cross-ns not permitted -> RefNotPermitted", func(t *testing.T) {
		r := &GatewayReconciler{ExperimentalBackends: true}
		col := &xbBackends{
			fetched:   map[xbKey]*apisxv1alpha1.XBackend{{namespace: "ext", name: "api"}: xb},
			permitted: map[permitKey]bool{},
		}
		_, reason := r.resolveXBackendRef("apps", "HTTPRoute", "ext", "api", col)
		if reason != reasonRefNotPermitted {
			t.Errorf("got %q, want RefNotPermitted", reason)
		}
	})

	t.Run("missing -> BackendNotFound", func(t *testing.T) {
		r := &GatewayReconciler{ExperimentalBackends: true}
		col := &xbBackends{fetched: map[xbKey]*apisxv1alpha1.XBackend{}, permitted: map[permitKey]bool{}}
		_, reason := r.resolveXBackendRef("apps", "HTTPRoute", "apps", "api", col)
		if reason != reasonBackendNotFound {
			t.Errorf("got %q, want BackendNotFound", reason)
		}
	})

	t.Run("same-ns resolved", func(t *testing.T) {
		r := &GatewayReconciler{ExperimentalBackends: true}
		col := &xbBackends{
			fetched:   map[xbKey]*apisxv1alpha1.XBackend{{namespace: "apps", name: "api"}: makeXBackend("apps", "api", "api.example.com", 443, nil, &apisxv1alpha1.BackendTLS{Mode: apisxv1alpha1.BackendTLSModeServerOnly})},
			permitted: map[permitKey]bool{},
		}
		rb, reason := r.resolveXBackendRef("apps", "HTTPRoute", "apps", "api", col)
		if reason != reasonResolvedOK {
			t.Fatalf("got reason %q, want OK", reason)
		}
		if rb.Service != "https://api.example.com:443" {
			t.Errorf("service: got %q", rb.Service)
		}
	})

	t.Run("cross-ns permitted resolved", func(t *testing.T) {
		r := &GatewayReconciler{ExperimentalBackends: true}
		col := &xbBackends{
			fetched:   map[xbKey]*apisxv1alpha1.XBackend{{namespace: "ext", name: "api"}: xb},
			permitted: map[permitKey]bool{{routeNS: "apps", routeKind: "HTTPRoute", xbNS: "ext", name: "api"}: true},
		}
		_, reason := r.resolveXBackendRef("apps", "HTTPRoute", "ext", "api", col)
		if reason != reasonResolvedOK {
			t.Errorf("got %q, want OK", reason)
		}
	})
}

func TestRouteResolvedRefs(t *testing.T) {
	r := &GatewayReconciler{ExperimentalBackends: true}
	col := &xbBackends{fetched: map[xbKey]*apisxv1alpha1.XBackend{}, permitted: map[permitKey]bool{}}

	t.Run("no xbackend refs -> OK", func(t *testing.T) {
		refs := []gwapiv1.BackendObjectReference{{Name: "svc"}}
		res := r.routeResolvedRefs("apps", "HTTPRoute", refs, col)
		if !res.OK || res.Reason != string(gwapiv1.RouteReasonResolvedRefs) {
			t.Errorf("got %+v, want OK/ResolvedRefs", res)
		}
	})

	t.Run("missing xbackend ref -> BackendNotFound", func(t *testing.T) {
		group := gwapiv1.Group(xBackendGroup)
		kind := gwapiv1.Kind(xBackendKind)
		refs := []gwapiv1.BackendObjectReference{{Group: &group, Kind: &kind, Name: "missing"}}
		res := r.routeResolvedRefs("apps", "HTTPRoute", refs, col)
		if res.OK || res.Reason != string(gwapiv1.RouteReasonBackendNotFound) {
			t.Errorf("got %+v, want not-OK/BackendNotFound", res)
		}
	})
}
