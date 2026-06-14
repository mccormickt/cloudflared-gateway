package cloudflare

import (
	"testing"

	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

const xbackendGroup = "gateway.networking.x-k8s.io"

// xbackendRef builds an HTTPBackendRef pointing at an XBackend.
func xbackendHTTPRef(name string, port int) gwapiv1.HTTPBackendRef {
	group := gwapiv1.Group(xbackendGroup)
	kind := gwapiv1.Kind("XBackend")
	p := gwapiv1.PortNumber(port)
	return gwapiv1.HTTPBackendRef{
		BackendRef: gwapiv1.BackendRef{
			BackendObjectReference: gwapiv1.BackendObjectReference{
				Group: &group,
				Kind:  &kind,
				Name:  gwapiv1.ObjectName(name),
				Port:  &p,
			},
		},
	}
}

// captureResolver records the ref it was called with and returns a fixed result.
type captureResolver struct {
	got    BackendRef
	result ResolvedBackend
	ok     bool
}

func (c *captureResolver) resolve(ref BackendRef) (ResolvedBackend, bool) {
	c.got = ref
	return c.result, c.ok
}

func TestBuildIngressRules_XBackendResolvedService(t *testing.T) {
	serverName := "api.openai.com"
	cap := &captureResolver{
		result: ResolvedBackend{
			Service:       "https://api.openai.com:443",
			OriginRequest: &OriginRequest{OriginServerName: &serverName},
		},
		ok: true,
	}
	route := makeRoute("ext", "apps",
		[]gwapiv1.Hostname{hostname("proxy.example.com")},
		[]gwapiv1.HTTPRouteRule{{
			BackendRefs: []gwapiv1.HTTPBackendRef{xbackendHTTPRef("openai", 443)},
		}},
	)

	rules := BuildIngressRules([]gwapiv1.HTTPRoute{route}, cap.resolve)
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].Service != "https://api.openai.com:443" {
		t.Errorf("service: got %q, want %q", rules[0].Service, "https://api.openai.com:443")
	}
	if rules[0].OriginRequest == nil || rules[0].OriginRequest.OriginServerName == nil ||
		*rules[0].OriginRequest.OriginServerName != serverName {
		t.Errorf("expected OriginServerName %q to be carried through, got %+v", serverName, rules[0].OriginRequest)
	}

	// Resolver must receive the normalized ref identity.
	if cap.got.Group != xbackendGroup || cap.got.Kind != "XBackend" {
		t.Errorf("resolver group/kind: got %q/%q", cap.got.Group, cap.got.Kind)
	}
	if cap.got.Name != "openai" || cap.got.Namespace != "apps" {
		t.Errorf("resolver name/ns: got %q/%q, want openai/apps", cap.got.Name, cap.got.Namespace)
	}
	if cap.got.RouteNamespace != "apps" || cap.got.RouteKind != "HTTPRoute" {
		t.Errorf("resolver route identity: got %q/%q, want apps/HTTPRoute", cap.got.RouteNamespace, cap.got.RouteKind)
	}
	if cap.got.Port == nil || *cap.got.Port != 443 {
		t.Errorf("resolver port: got %v, want 443", cap.got.Port)
	}
}

func TestBuildIngressRules_XBackendUnresolvable503(t *testing.T) {
	cap := &captureResolver{result: ResolvedBackend{Service: ""}, ok: true}
	route := makeRoute("ext", "default",
		[]gwapiv1.Hostname{hostname("proxy.example.com")},
		[]gwapiv1.HTTPRouteRule{{
			BackendRefs: []gwapiv1.HTTPBackendRef{xbackendHTTPRef("missing", 443)},
		}},
	)
	rules := BuildIngressRules([]gwapiv1.HTTPRoute{route}, cap.resolve)
	if len(rules) != 1 || rules[0].Service != "http_status:503" {
		t.Fatalf("expected single http_status:503 rule, got %+v", rules)
	}
}

func TestBuildIngressRules_NativeServiceUnaffectedByResolver(t *testing.T) {
	// A resolver that would fail loudly if consulted for a non-XBackend ref.
	resolve := func(ref BackendRef) (ResolvedBackend, bool) {
		if ref.Kind == "XBackend" {
			return ResolvedBackend{Service: "https://should-not-be-used"}, true
		}
		return ResolvedBackend{}, false
	}
	route := makeRoute("web", "default",
		[]gwapiv1.Hostname{hostname("example.com")},
		[]gwapiv1.HTTPRouteRule{{
			BackendRefs: []gwapiv1.HTTPBackendRef{makeBackendRef("web-svc", 8080)},
		}},
	)
	rules := BuildIngressRules([]gwapiv1.HTTPRoute{route}, resolve)
	if len(rules) != 1 || rules[0].Service != "http://web-svc.default:8080" {
		t.Fatalf("native Service ref should be unchanged, got %+v", rules)
	}
}

func TestBuildTLSIngressRules_XBackendSuppressesDefaultNoTLSVerify(t *testing.T) {
	serverName := "secure.example.net"
	xbOrigin := &OriginRequest{OriginServerName: &serverName}
	resolve := func(ref BackendRef) (ResolvedBackend, bool) {
		if ref.Kind == "XBackend" {
			return ResolvedBackend{Service: "https://secure.example.net:8443", OriginRequest: xbOrigin}, true
		}
		return ResolvedBackend{}, false
	}

	group := gwapiv1.Group(xbackendGroup)
	kind := gwapiv1.Kind("XBackend")
	xbRef := gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{
		Group: &group, Kind: &kind, Name: "secure",
	}}
	route := gwapiv1alpha2.TLSRoute{}
	route.Name = "tls-ext"
	route.Namespace = "default"
	route.Spec.Hostnames = []gwapiv1.Hostname{hostname("secure.example.net")}
	route.Spec.Rules = []gwapiv1alpha2.TLSRouteRule{{BackendRefs: []gwapiv1.BackendRef{xbRef}}}

	rules := BuildTLSIngressRules([]gwapiv1alpha2.TLSRoute{route}, resolve)
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].Service != "https://secure.example.net:8443" {
		t.Errorf("service: got %q", rules[0].Service)
	}
	// External XBackend TLS governs: the default NoTLSVerify=true must NOT be set,
	// and the XBackend's OriginServerName must be present.
	if rules[0].OriginRequest == nil || rules[0].OriginRequest.NoTLSVerify != nil {
		t.Errorf("NoTLSVerify should be unset for an XBackend ref, got %+v", rules[0].OriginRequest)
	}
	if rules[0].OriginRequest.OriginServerName == nil || *rules[0].OriginRequest.OriginServerName != serverName {
		t.Errorf("expected OriginServerName %q, got %+v", serverName, rules[0].OriginRequest)
	}
}

func TestBuildTLSIngressRules_NativeServiceKeepsNoTLSVerify(t *testing.T) {
	ref := gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "tls-svc"}}
	route := gwapiv1alpha2.TLSRoute{}
	route.Name = "tls"
	route.Namespace = "default"
	route.Spec.Hostnames = []gwapiv1.Hostname{hostname("svc.example.com")}
	route.Spec.Rules = []gwapiv1alpha2.TLSRouteRule{{BackendRefs: []gwapiv1.BackendRef{ref}}}

	rules := BuildTLSIngressRules([]gwapiv1alpha2.TLSRoute{route}, NilResolver)
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].Service != "https://tls-svc.default:443" {
		t.Errorf("service: got %q", rules[0].Service)
	}
	if rules[0].OriginRequest == nil || rules[0].OriginRequest.NoTLSVerify == nil || !*rules[0].OriginRequest.NoTLSVerify {
		t.Errorf("native TLS Service ref should keep NoTLSVerify=true, got %+v", rules[0].OriginRequest)
	}
}
