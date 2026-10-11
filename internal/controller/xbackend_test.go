package controller

import (
	"context"
	"strings"
	"testing"

	cfclient "github.com/mccormickt/cloudflared-gateway/internal/cloudflare"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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
			rb, reason := translateXBackend(tc.xb, "")
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

// xbTarget builds an XBackend backendTarget for resolver tests.
func xbTarget(routeNS, routeKind, xbNS, name string) backendTarget {
	return backendTarget{routeNS: routeNS, routeKind: routeKind, group: xBackendGroup, kind: xBackendKind, ns: xbNS, name: name}
}

func TestResolveXBackendRef_Reasons(t *testing.T) {
	xb := makeXBackend("ext", "api", "api.example.com", 443, nil, &apisxv1alpha1.BackendTLS{Mode: apisxv1alpha1.BackendTLSModeServerOnly})

	t.Run("flag off -> XBackendDisabled", func(t *testing.T) {
		r := &GatewayReconciler{ExperimentalBackends: false}
		_, reason := r.resolveXBackendRef(xbTarget("apps", "HTTPRoute", "apps", "api"), nil)
		if reason != reasonXBackendDisabled {
			t.Errorf("got %q, want XBackendDisabled", reason)
		}
	})

	t.Run("cross-ns not permitted -> RefNotPermitted", func(t *testing.T) {
		r := &GatewayReconciler{ExperimentalBackends: true}
		col := &xbBackends{
			fetched:   map[xbKey]*apisxv1alpha1.XBackend{{namespace: "ext", name: "api"}: xb},
			permitted: map[permitKey]bool{},
		}
		_, reason := r.resolveXBackendRef(xbTarget("apps", "HTTPRoute", "ext", "api"), col)
		if reason != reasonRefNotPermitted {
			t.Errorf("got %q, want RefNotPermitted", reason)
		}
	})

	t.Run("missing -> BackendNotFound", func(t *testing.T) {
		r := &GatewayReconciler{ExperimentalBackends: true}
		col := &xbBackends{fetched: map[xbKey]*apisxv1alpha1.XBackend{}, permitted: map[permitKey]bool{}}
		_, reason := r.resolveXBackendRef(xbTarget("apps", "HTTPRoute", "apps", "api"), col)
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
		rb, reason := r.resolveXBackendRef(xbTarget("apps", "HTTPRoute", "apps", "api"), col)
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
			permitted: map[permitKey]bool{{routeNS: "apps", routeKind: "HTTPRoute", toNS: "ext", name: "api"}: true},
		}
		_, reason := r.resolveXBackendRef(xbTarget("apps", "HTTPRoute", "ext", "api"), col)
		if reason != reasonResolvedOK {
			t.Errorf("got %q, want OK", reason)
		}
	})
}

// TestTranslateXBackend_RouteKindCompatibility covers the reference-level checks:
// the origin scheme has to match the transport the referencing route kind
// carries, and an unset protocol defaults per route kind.
func TestTranslateXBackend_RouteKindCompatibility(t *testing.T) {
	tlsServerOnly := &apisxv1alpha1.BackendTLS{Mode: apisxv1alpha1.BackendTLSModeServerOnly}
	tlsNone := &apisxv1alpha1.BackendTLS{Mode: apisxv1alpha1.BackendTLSModeNone}

	tests := []struct {
		name        string
		routeKind   string
		proto       *apisxv1alpha1.BackendProtocol
		tls         *apisxv1alpha1.BackendTLS
		wantReason  string
		wantService string
	}{
		{
			name: "TCPRoute with unset protocol defaults to tcp", routeKind: "TCPRoute",
			wantService: "tcp://api.example.com:443",
		},
		{
			name: "TCPRoute with explicit TCP", routeKind: "TCPRoute", proto: protoPtr(apisxv1alpha1.BackendProtocolTCP),
			wantService: "tcp://api.example.com:443",
		},
		{
			name: "TCPRoute with HTTP protocol is incompatible", routeKind: "TCPRoute", proto: protoPtr(apisxv1alpha1.BackendProtocolHTTP),
			wantReason: reasonIncompatibleRouteKind,
		},
		{
			name: "TCPRoute asking for TLS is unsupported", routeKind: "TCPRoute", tls: tlsServerOnly,
			wantReason: reasonUnsupportedProtocol,
		},
		{
			name: "TLSRoute with server TLS terminates at https", routeKind: "TLSRoute", tls: tlsServerOnly,
			wantService: "https://api.example.com:443",
		},
		{
			name: "TLSRoute over plain TCP passes through", routeKind: "TLSRoute", proto: protoPtr(apisxv1alpha1.BackendProtocolTCP),
			wantService: "tcp://api.example.com:443",
		},
		{
			name: "TLSRoute with no TLS is incompatible", routeKind: "TLSRoute", tls: tlsNone,
			wantReason: reasonIncompatibleRouteKind,
		},
		{
			name: "HTTPRoute with no TLS stays http", routeKind: "HTTPRoute",
			wantService: "http://api.example.com:443",
		},
		{
			name: "HTTPRoute with TCP is incompatible", routeKind: "HTTPRoute", proto: protoPtr(apisxv1alpha1.BackendProtocolTCP),
			wantReason: reasonIncompatibleRouteKind,
		},
		{
			name: "GRPCRoute with TCP is incompatible", routeKind: "GRPCRoute", proto: protoPtr(apisxv1alpha1.BackendProtocolTCP), tls: tlsNone,
			wantReason: reasonIncompatibleRouteKind,
		},
		{
			name: "GRPCRoute with HTTP2 and TLS stays https", routeKind: "GRPCRoute", proto: protoPtr(apisxv1alpha1.BackendProtocolHTTP2), tls: tlsServerOnly,
			wantService: "https://api.example.com:443",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			xb := makeXBackend("ns", "a", "api.example.com", 443, tc.proto, tc.tls)
			rb, reason := translateXBackend(xb, tc.routeKind)
			if reason != tc.wantReason {
				t.Fatalf("reason: got %q, want %q", reason, tc.wantReason)
			}
			if tc.wantReason == "" && rb.Service != tc.wantService {
				t.Errorf("service: got %q, want %q", rb.Service, tc.wantService)
			}
		})
	}
}

func TestResolveServiceRef(t *testing.T) {
	svcTarget := func(routeNS, svcNS, name string) backendTarget {
		return backendTarget{routeNS: routeNS, routeKind: "HTTPRoute", kind: serviceKind, ns: svcNS, name: name}
	}

	t.Run("nil context fails closed", func(t *testing.T) {
		if got := resolveServiceRef(svcTarget("apps", "apps", "svc"), nil); got != reasonBackendNotFound {
			t.Errorf("got %q, want BackendNotFound", got)
		}
	})

	t.Run("same-ns existing -> OK", func(t *testing.T) {
		col := &svcBackends{exists: map[svcKey]bool{{"apps", "svc"}: true}, permitted: map[permitKey]bool{}}
		if got := resolveServiceRef(svcTarget("apps", "apps", "svc"), col); got != reasonResolvedOK {
			t.Errorf("got %q, want OK", got)
		}
	})

	t.Run("same-ns missing -> BackendNotFound", func(t *testing.T) {
		col := &svcBackends{exists: map[svcKey]bool{{"apps", "svc"}: false}, permitted: map[permitKey]bool{}}
		if got := resolveServiceRef(svcTarget("apps", "apps", "svc"), col); got != reasonBackendNotFound {
			t.Errorf("got %q, want BackendNotFound", got)
		}
	})

	t.Run("cross-ns without grant -> RefNotPermitted", func(t *testing.T) {
		col := &svcBackends{exists: map[svcKey]bool{{"other", "svc"}: true}, permitted: map[permitKey]bool{}}
		if got := resolveServiceRef(svcTarget("apps", "other", "svc"), col); got != reasonRefNotPermitted {
			t.Errorf("got %q, want RefNotPermitted", got)
		}
	})

	t.Run("cross-ns with grant -> OK", func(t *testing.T) {
		col := &svcBackends{
			exists:    map[svcKey]bool{{"other", "svc"}: true},
			permitted: map[permitKey]bool{{routeNS: "apps", routeKind: "HTTPRoute", toNS: "other", name: "svc"}: true},
		}
		if got := resolveServiceRef(svcTarget("apps", "other", "svc"), col); got != reasonResolvedOK {
			t.Errorf("got %q, want OK", got)
		}
	})
}

func TestRouteResolvedRefs(t *testing.T) {
	r := &GatewayReconciler{ExperimentalBackends: true}
	col := &xbBackends{fetched: map[xbKey]*apisxv1alpha1.XBackend{}, permitted: map[permitKey]bool{}}
	svcCol := &svcBackends{exists: map[svcKey]bool{{"apps", "svc"}: true}, permitted: map[permitKey]bool{}}

	t.Run("existing service ref -> OK", func(t *testing.T) {
		refs := []gwapiv1.BackendObjectReference{{Name: "svc"}}
		res := r.routeResolvedRefs("apps", "HTTPRoute", refs, col, svcCol)
		if !res.OK || res.Reason != string(gwapiv1.RouteReasonResolvedRefs) {
			t.Errorf("got %+v, want OK/ResolvedRefs", res)
		}
	})

	t.Run("missing service ref -> BackendNotFound", func(t *testing.T) {
		refs := []gwapiv1.BackendObjectReference{{Name: "nope"}}
		res := r.routeResolvedRefs("apps", "HTTPRoute", refs, col, svcCol)
		if res.OK || res.Reason != string(gwapiv1.RouteReasonBackendNotFound) {
			t.Errorf("got %+v, want not-OK/BackendNotFound", res)
		}
	})

	t.Run("unpermitted cross-ns service ref -> RefNotPermitted", func(t *testing.T) {
		otherNS := gwapiv1.Namespace("other")
		refs := []gwapiv1.BackendObjectReference{{Name: "svc", Namespace: &otherNS}}
		res := r.routeResolvedRefs("apps", "HTTPRoute", refs, col, svcCol)
		if res.OK || res.Reason != string(gwapiv1.RouteReasonRefNotPermitted) {
			t.Errorf("got %+v, want not-OK/RefNotPermitted", res)
		}
	})

	t.Run("unroutable kind -> InvalidKind", func(t *testing.T) {
		kind := gwapiv1.Kind("Foo")
		refs := []gwapiv1.BackendObjectReference{{Kind: &kind, Name: "thing"}}
		res := r.routeResolvedRefs("apps", "HTTPRoute", refs, col, svcCol)
		if res.OK || res.Reason != string(gwapiv1.RouteReasonInvalidKind) {
			t.Errorf("got %+v, want not-OK/InvalidKind", res)
		}
	})

	t.Run("missing xbackend ref -> BackendNotFound", func(t *testing.T) {
		group := gwapiv1.Group(xBackendGroup)
		kind := gwapiv1.Kind(xBackendKind)
		refs := []gwapiv1.BackendObjectReference{{Group: &group, Kind: &kind, Name: "missing"}}
		res := r.routeResolvedRefs("apps", "HTTPRoute", refs, col, svcCol)
		if res.OK || res.Reason != string(gwapiv1.RouteReasonBackendNotFound) {
			t.Errorf("got %+v, want not-OK/BackendNotFound", res)
		}
	})

	t.Run("worst reason wins across refs", func(t *testing.T) {
		otherNS := gwapiv1.Namespace("other")
		refs := []gwapiv1.BackendObjectReference{
			{Name: "nope"},                     // BackendNotFound
			{Name: "svc", Namespace: &otherNS}, // RefNotPermitted (more actionable)
		}
		res := r.routeResolvedRefs("apps", "HTTPRoute", refs, col, svcCol)
		if res.Reason != string(gwapiv1.RouteReasonRefNotPermitted) {
			t.Errorf("got %+v, want RefNotPermitted", res)
		}
	})
}

// TestApplyBackendTLSPolicies_PreservesXBackendOrigin guards against
// applyBackendTLSPolicies clobbering the XBackend-derived origin on a TLSRoute
// rule. GetBackendTLSConfig falls back to noTLSVerify=true when no
// BackendTLSPolicy targets the backend Service, which would silently disable
// origin certificate verification for an external HTTPS destination.
func TestApplyBackendTLSPolicies_PreservesXBackendOrigin(t *testing.T) {
	group := gwapiv1.Group(xBackendGroup)
	kind := gwapiv1.Kind(xBackendKind)

	xbRoute := gwapiv1.TLSRoute{}
	xbRoute.Name = "tls-ext"
	xbRoute.Namespace = "default"
	xbRoute.Spec.Hostnames = []gwapiv1.Hostname{"secure.example.net"}
	xbRoute.Spec.Rules = []gwapiv1.TLSRouteRule{{
		BackendRefs: []gwapiv1.BackendRef{{BackendObjectReference: gwapiv1.BackendObjectReference{
			Group: &group, Kind: &kind, Name: "secure",
		}}},
	}}

	xb := makeXBackend("default", "secure", "secure.example.net", 8443, nil,
		&apisxv1alpha1.BackendTLS{
			Mode:       apisxv1alpha1.BackendTLSModeServerOnly,
			Validation: gwapiv1.BackendTLSPolicyValidation{Hostname: "secure.example.net"},
		})

	r := &GatewayReconciler{
		Client:               fake.NewClientBuilder().WithScheme(testScheme()).Build(),
		ExperimentalBackends: true,
	}
	col := &xbBackends{
		fetched:   map[xbKey]*apisxv1alpha1.XBackend{{namespace: "default", name: "secure"}: xb},
		permitted: map[permitKey]bool{},
	}

	rules := cfclient.BuildTLSIngressRules([]gwapiv1.TLSRoute{xbRoute}, r.backendResolver(col, nil))
	rules, err := r.applyBackendTLSPolicies(context.Background(), rules, []gwapiv1.TLSRoute{xbRoute})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].Service != "https://secure.example.net:8443" {
		t.Errorf("service: got %q", rules[0].Service)
	}
	if rules[0].OriginRequest == nil {
		t.Fatal("origin request was dropped")
	}
	if rules[0].OriginRequest.NoTLSVerify != nil {
		t.Errorf("NoTLSVerify must stay unset for an external XBackend origin, got %v", *rules[0].OriginRequest.NoTLSVerify)
	}
	if rules[0].OriginRequest.OriginServerName == nil || *rules[0].OriginRequest.OriginServerName != "secure.example.net" {
		t.Errorf("OriginServerName was dropped: %+v", rules[0].OriginRequest)
	}
}

func TestXBackendAncestorStatus(t *testing.T) {
	cn := gwapiv1.GatewayController(ControllerName)
	first := gwapiv1.ParentReference{Name: "first"}
	second := gwapiv1.ParentReference{Name: "second"}
	status := apisxv1alpha1.BackendStatus{}
	accepted := xbAcceptedCondition(1, reasonResolvedOK)
	if !upsertXBackendAncestor(&status, first, cn, accepted) || !upsertXBackendAncestor(&status, second, cn, accepted) {
		t.Fatal("both Gateways should receive an ancestor entry")
	}
	if upsertXBackendAncestor(&status, first, cn, accepted) {
		t.Fatal("unchanged status must not request an update")
	}
	if removeXBackendAncestor(&status, first, "another-controller") {
		t.Fatal("must not prune another controller's entry")
	}
	if !removeXBackendAncestor(&status, first, cn) || len(status.Ancestors) != 1 || status.Ancestors[0].AncestorRef.Name != "second" {
		t.Fatalf("pruning first must preserve second: %+v", status)
	}
	for _, reason := range []string{reasonUnsupportedProtocol, reasonUnsupportedCACerts, reasonUnsupportedTLSValidation} {
		cond := xbAcceptedCondition(2, reason)
		if !upsertXBackendAncestor(&status, second, cn, cond) {
			t.Fatal("condition change should request a status update")
		}
		got := status.Ancestors[0].Conditions[0]
		if got.Status != metav1.ConditionFalse || got.Reason != reason || got.ObservedGeneration != 2 {
			t.Fatalf("unexpected rejection condition: %+v", got)
		}
	}
	for len(status.Ancestors) < maxXBackendAncestors {
		status.Ancestors = append(status.Ancestors, apisxv1alpha1.BackendAncestorStatus{AncestorRef: first, ControllerName: "other-controller"})
	}
	if upsertXBackendAncestor(&status, first, cn, accepted) || len(status.Ancestors) != maxXBackendAncestors {
		t.Fatal("new ancestors must not exceed the cap")
	}
	if !upsertXBackendAncestor(&status, second, cn, accepted) || status.Ancestors[0].Conditions[0].Status != metav1.ConditionTrue {
		t.Fatal("existing ancestors must still update at the cap")
	}
}

func TestXBackendRejectsSubjectAltNames(t *testing.T) {
	for _, san := range []gwapiv1.SubjectAltName{
		{Type: gwapiv1.HostnameSubjectAltNameType, Hostname: "certificate.example.net"},
		{Type: gwapiv1.URISubjectAltNameType, URI: "spiffe://example.net/backend"},
	} {
		t.Run(string(san.Type), func(t *testing.T) {
			xb := makeXBackend("apps", "api", "origin.example.com", 443, nil, &apisxv1alpha1.BackendTLS{
				Mode: apisxv1alpha1.BackendTLSModeServerOnly,
				Validation: gwapiv1.BackendTLSPolicyValidation{
					Hostname:                "sni.example.com",
					WellKnownCACertificates: ptr(gwapiv1.WellKnownCACertificatesSystem),
					SubjectAltNames:         []gwapiv1.SubjectAltName{san},
				},
			})
			r := &GatewayReconciler{ExperimentalBackends: true}
			col := &xbBackends{fetched: map[xbKey]*apisxv1alpha1.XBackend{{namespace: "apps", name: "api"}: xb}}
			refs := []gwapiv1.BackendObjectReference{{Group: ptr(gwapiv1.Group(xBackendGroup)), Kind: ptr(gwapiv1.Kind(xBackendKind)), Name: "api"}}
			routeResult := r.routeResolvedRefs("apps", "HTTPRoute", refs, col, nil)
			if routeResult.OK || routeResult.Reason != "UnsupportedTLSValidation" || !strings.Contains(routeResult.Message, "subjectAltNames") {
				t.Fatalf("route must reject custom SAN validation: %+v", routeResult)
			}
			rb, reason := r.resolveXBackendRef(xbTarget("apps", "HTTPRoute", "apps", "api"), col)
			if rb.Service != "" || reason != "UnsupportedTLSValidation" {
				t.Fatalf("must not serve a backend with ignored SAN validation: %+v, %s", rb, reason)
			}
			_, intrinsicReason := translateXBackend(xb, "")
			ancestor := xbAcceptedCondition(1, intrinsicReason)
			if ancestor.Status != metav1.ConditionFalse || ancestor.Reason != "UnsupportedTLSValidation" || !strings.Contains(ancestor.Message, "subjectAltNames") {
				t.Fatalf("ancestor must reject custom SAN validation: %+v", ancestor)
			}
		})
	}
}
