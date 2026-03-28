package cloudflare

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func makeRoute(name, namespace string, hostnames []gwapiv1.Hostname, rules []gwapiv1.HTTPRouteRule) gwapiv1.HTTPRoute {
	return gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: gwapiv1.HTTPRouteSpec{
			Hostnames: hostnames,
			Rules:     rules,
		},
	}
}

func makeBackendRef(name string, port int) gwapiv1.HTTPBackendRef {
	p := gwapiv1.PortNumber(port)
	return gwapiv1.HTTPBackendRef{
		BackendRef: gwapiv1.BackendRef{
			BackendObjectReference: gwapiv1.BackendObjectReference{
				Name: gwapiv1.ObjectName(name),
				Port: &p,
			},
		},
	}
}

func makePathMatch(pathType gwapiv1.PathMatchType, value string) gwapiv1.HTTPRouteMatch {
	return gwapiv1.HTTPRouteMatch{
		Path: &gwapiv1.HTTPPathMatch{
			Type:  &pathType,
			Value: &value,
		},
	}
}

func hostname(s string) gwapiv1.Hostname {
	return gwapiv1.Hostname(s)
}

func preciseHostname(s string) gwapiv1.PreciseHostname {
	return gwapiv1.PreciseHostname(s)
}

// ---------------------------------------------------------------------------
// Tests: BuildTunnelToken
// ---------------------------------------------------------------------------

func TestBuildTunnelToken_ValidBase64JSON(t *testing.T) {
	token := BuildTunnelToken("acc-123", "tun-456", []byte("my-secret"))

	decoded, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("token should be valid base64: %v", err)
	}

	var parsed map[string]string
	if err := json.Unmarshal(decoded, &parsed); err != nil {
		t.Fatalf("decoded should be valid JSON: %v", err)
	}

	if parsed["a"] != "acc-123" {
		t.Errorf("account_id: got %q, want %q", parsed["a"], "acc-123")
	}
	if parsed["t"] != "tun-456" {
		t.Errorf("tunnel_id: got %q, want %q", parsed["t"], "tun-456")
	}

	expectedSecret := base64.StdEncoding.EncodeToString([]byte("my-secret"))
	if parsed["s"] != expectedSecret {
		t.Errorf("secret: got %q, want %q", parsed["s"], expectedSecret)
	}
}

func TestBuildTunnelToken_EmptySecret(t *testing.T) {
	token := BuildTunnelToken("a", "t", []byte{})

	decoded, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("token should be valid base64: %v", err)
	}

	var parsed map[string]string
	if err := json.Unmarshal(decoded, &parsed); err != nil {
		t.Fatalf("decoded should be valid JSON: %v", err)
	}

	if parsed["s"] != "" {
		t.Errorf("empty secret should encode to empty string, got %q", parsed["s"])
	}
}

// ---------------------------------------------------------------------------
// Tests: BuildIngressRules
// ---------------------------------------------------------------------------

func TestBuildIngressRules_EmptyRoutes(t *testing.T) {
	rules := BuildIngressRules(nil)
	if len(rules) != 0 {
		t.Errorf("empty routes should produce no rules, got %d", len(rules))
	}
}

func TestBuildIngressRules_SingleRouteWithHostnameAndPath(t *testing.T) {
	route := makeRoute("web", "default",
		[]gwapiv1.Hostname{hostname("example.com")},
		[]gwapiv1.HTTPRouteRule{{
			BackendRefs: []gwapiv1.HTTPBackendRef{makeBackendRef("web-svc", 8080)},
			Matches:     []gwapiv1.HTTPRouteMatch{makePathMatch(gwapiv1.PathMatchPathPrefix, "/api")},
		}},
	)

	rules := BuildIngressRules([]gwapiv1.HTTPRoute{route})

	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].Hostname != "example.com" {
		t.Errorf("hostname: got %q, want %q", rules[0].Hostname, "example.com")
	}
	if rules[0].Path != "^/api" {
		t.Errorf("path: got %q, want %q", rules[0].Path, "^/api")
	}
	if rules[0].Service != "http://web-svc.default:8080" {
		t.Errorf("service: got %q, want %q", rules[0].Service, "http://web-svc.default:8080")
	}
}

func TestBuildIngressRules_MultipleRoutes(t *testing.T) {
	route1 := makeRoute("web", "default",
		[]gwapiv1.Hostname{hostname("web.example.com")},
		[]gwapiv1.HTTPRouteRule{{
			BackendRefs: []gwapiv1.HTTPBackendRef{makeBackendRef("web-svc", 80)},
		}},
	)
	route2 := makeRoute("api", "backend",
		[]gwapiv1.Hostname{hostname("api.example.com")},
		[]gwapiv1.HTTPRouteRule{{
			BackendRefs: []gwapiv1.HTTPBackendRef{makeBackendRef("api-svc", 3000)},
			Matches:     []gwapiv1.HTTPRouteMatch{makePathMatch(gwapiv1.PathMatchExact, "/health")},
		}},
	)

	rules := BuildIngressRules([]gwapiv1.HTTPRoute{route1, route2})

	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rules))
	}

	if rules[0].Hostname != "web.example.com" || rules[0].Path != "" {
		t.Errorf("rule 0: hostname=%q path=%q", rules[0].Hostname, rules[0].Path)
	}
	if rules[0].Service != "http://web-svc.default:80" {
		t.Errorf("rule 0 service: got %q", rules[0].Service)
	}

	if rules[1].Hostname != "api.example.com" || rules[1].Path != "^/health$" {
		t.Errorf("rule 1: hostname=%q path=%q", rules[1].Hostname, rules[1].Path)
	}
	if rules[1].Service != "http://api-svc.backend:3000" {
		t.Errorf("rule 1 service: got %q", rules[1].Service)
	}
}

func TestBuildIngressRules_RouteWithoutHostname(t *testing.T) {
	route := makeRoute("catch", "default", nil,
		[]gwapiv1.HTTPRouteRule{{
			BackendRefs: []gwapiv1.HTTPBackendRef{makeBackendRef("fallback", 9090)},
		}},
	)

	rules := BuildIngressRules([]gwapiv1.HTTPRoute{route})

	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].Hostname != "" {
		t.Errorf("hostname should be empty, got %q", rules[0].Hostname)
	}
	if rules[0].Service != "http://fallback.default:9090" {
		t.Errorf("service: got %q", rules[0].Service)
	}
}

func TestBuildIngressRules_PathPrefixRootOmitsPath(t *testing.T) {
	route := makeRoute("root", "default",
		[]gwapiv1.Hostname{hostname("example.com")},
		[]gwapiv1.HTTPRouteRule{{
			BackendRefs: []gwapiv1.HTTPBackendRef{makeBackendRef("root-svc", 80)},
			Matches:     []gwapiv1.HTTPRouteMatch{makePathMatch(gwapiv1.PathMatchPathPrefix, "/")},
		}},
	)

	rules := BuildIngressRules([]gwapiv1.HTTPRoute{route})

	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].Path != "" {
		t.Errorf("root prefix path should be empty, got %q", rules[0].Path)
	}
}

func TestBuildIngressRules_NoBackendRefProduces503(t *testing.T) {
	route := makeRoute("empty", "default",
		[]gwapiv1.Hostname{hostname("example.com")},
		[]gwapiv1.HTTPRouteRule{{
			// No BackendRefs
		}},
	)

	rules := BuildIngressRules([]gwapiv1.HTTPRoute{route})

	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].Service != "http_status:503" {
		t.Errorf("service: got %q, want %q", rules[0].Service, "http_status:503")
	}
}

// ---------------------------------------------------------------------------
// Tests: HTTPRoute filters
// ---------------------------------------------------------------------------

func TestBuildIngressRules_URLRewriteSetsHTTPHostHeader(t *testing.T) {
	h := preciseHostname("internal.example.com")
	route := makeRoute("rewrite", "default",
		[]gwapiv1.Hostname{hostname("example.com")},
		[]gwapiv1.HTTPRouteRule{{
			BackendRefs: []gwapiv1.HTTPBackendRef{makeBackendRef("web-svc", 8080)},
			Filters: []gwapiv1.HTTPRouteFilter{{
				Type: gwapiv1.HTTPRouteFilterURLRewrite,
				URLRewrite: &gwapiv1.HTTPURLRewriteFilter{
					Hostname: &h,
				},
			}},
		}},
	)

	rules := BuildIngressRules([]gwapiv1.HTTPRoute{route})

	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].OriginRequest == nil {
		t.Fatal("expected originRequest to be set")
	}
	if rules[0].OriginRequest.HTTPHostHeader == nil || *rules[0].OriginRequest.HTTPHostHeader != "internal.example.com" {
		t.Errorf("httpHostHeader: got %v", rules[0].OriginRequest.HTTPHostHeader)
	}
}

func TestBuildIngressRules_RequestHeaderModifierHostSetsHTTPHostHeader(t *testing.T) {
	route := makeRoute("header-mod", "default",
		[]gwapiv1.Hostname{hostname("example.com")},
		[]gwapiv1.HTTPRouteRule{{
			BackendRefs: []gwapiv1.HTTPBackendRef{makeBackendRef("web-svc", 8080)},
			Filters: []gwapiv1.HTTPRouteFilter{{
				Type: gwapiv1.HTTPRouteFilterRequestHeaderModifier,
				RequestHeaderModifier: &gwapiv1.HTTPHeaderFilter{
					Set: []gwapiv1.HTTPHeader{{
						Name:  "Host",
						Value: "rewritten.example.com",
					}},
				},
			}},
		}},
	)

	rules := BuildIngressRules([]gwapiv1.HTTPRoute{route})

	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].OriginRequest == nil {
		t.Fatal("expected originRequest to be set")
	}
	if rules[0].OriginRequest.HTTPHostHeader == nil || *rules[0].OriginRequest.HTTPHostHeader != "rewritten.example.com" {
		t.Errorf("httpHostHeader: got %v", rules[0].OriginRequest.HTTPHostHeader)
	}
}

func TestBuildIngressRules_NoFiltersNoOriginRequest(t *testing.T) {
	route := makeRoute("plain", "default",
		[]gwapiv1.Hostname{hostname("example.com")},
		[]gwapiv1.HTTPRouteRule{{
			BackendRefs: []gwapiv1.HTTPBackendRef{makeBackendRef("web-svc", 80)},
		}},
	)

	rules := BuildIngressRules([]gwapiv1.HTTPRoute{route})

	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].OriginRequest != nil {
		t.Errorf("expected nil originRequest, got %+v", rules[0].OriginRequest)
	}
}

func TestBuildIngressRules_UnsupportedFilterIgnored(t *testing.T) {
	route := makeRoute("mirror", "default",
		[]gwapiv1.Hostname{hostname("example.com")},
		[]gwapiv1.HTTPRouteRule{{
			BackendRefs: []gwapiv1.HTTPBackendRef{makeBackendRef("web-svc", 80)},
			Filters: []gwapiv1.HTTPRouteFilter{{
				Type: gwapiv1.HTTPRouteFilterRequestMirror,
			}},
		}},
	)

	rules := BuildIngressRules([]gwapiv1.HTTPRoute{route})

	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].OriginRequest != nil {
		t.Errorf("unsupported filter should not produce originRequest, got %+v", rules[0].OriginRequest)
	}
}

// ---------------------------------------------------------------------------
// Tests: HTTPRoute Timeouts
// ---------------------------------------------------------------------------

func TestBuildIngressRules_BackendTimeout(t *testing.T) {
	timeout := gwapiv1.Duration("10s")
	route := makeRoute("web", "default",
		[]gwapiv1.Hostname{hostname("example.com")},
		[]gwapiv1.HTTPRouteRule{{
			BackendRefs: []gwapiv1.HTTPBackendRef{makeBackendRef("web-svc", 8080)},
			Timeouts: &gwapiv1.HTTPRouteTimeouts{
				BackendRequest: &timeout,
			},
		}},
	)

	rules := BuildIngressRules([]gwapiv1.HTTPRoute{route})

	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].OriginRequest == nil {
		t.Fatal("expected originRequest to be set")
	}
	if rules[0].OriginRequest.ConnectTimeout == nil {
		t.Fatal("expected connectTimeout to be set")
	}
	if rules[0].OriginRequest.ConnectTimeout.Duration != 10*time.Second {
		t.Errorf("connectTimeout: got %v, want 10s", rules[0].OriginRequest.ConnectTimeout.Duration)
	}
}

func TestBuildIngressRules_BackendTimeoutMilliseconds(t *testing.T) {
	timeout := gwapiv1.Duration("500ms")
	route := makeRoute("web", "default",
		[]gwapiv1.Hostname{hostname("example.com")},
		[]gwapiv1.HTTPRouteRule{{
			BackendRefs: []gwapiv1.HTTPBackendRef{makeBackendRef("web-svc", 8080)},
			Timeouts: &gwapiv1.HTTPRouteTimeouts{
				BackendRequest: &timeout,
			},
		}},
	)

	rules := BuildIngressRules([]gwapiv1.HTTPRoute{route})

	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].OriginRequest == nil || rules[0].OriginRequest.ConnectTimeout == nil {
		t.Fatal("expected connectTimeout to be set")
	}
	if rules[0].OriginRequest.ConnectTimeout.Duration != 500*time.Millisecond {
		t.Errorf("connectTimeout: got %v, want 500ms", rules[0].OriginRequest.ConnectTimeout.Duration)
	}
}

func TestBuildIngressRules_NoTimeout(t *testing.T) {
	route := makeRoute("web", "default",
		[]gwapiv1.Hostname{hostname("example.com")},
		[]gwapiv1.HTTPRouteRule{{
			BackendRefs: []gwapiv1.HTTPBackendRef{makeBackendRef("web-svc", 8080)},
			// No Timeouts
		}},
	)

	rules := BuildIngressRules([]gwapiv1.HTTPRoute{route})

	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].OriginRequest != nil {
		t.Errorf("expected nil originRequest when no timeout, got %+v", rules[0].OriginRequest)
	}
}

func TestBuildIngressRules_TimeoutWithFilter(t *testing.T) {
	timeout := gwapiv1.Duration("30s")
	h := preciseHostname("internal.example.com")
	route := makeRoute("web", "default",
		[]gwapiv1.Hostname{hostname("example.com")},
		[]gwapiv1.HTTPRouteRule{{
			BackendRefs: []gwapiv1.HTTPBackendRef{makeBackendRef("web-svc", 8080)},
			Filters: []gwapiv1.HTTPRouteFilter{{
				Type: gwapiv1.HTTPRouteFilterURLRewrite,
				URLRewrite: &gwapiv1.HTTPURLRewriteFilter{
					Hostname: &h,
				},
			}},
			Timeouts: &gwapiv1.HTTPRouteTimeouts{
				BackendRequest: &timeout,
			},
		}},
	)

	rules := BuildIngressRules([]gwapiv1.HTTPRoute{route})

	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].OriginRequest == nil {
		t.Fatal("expected originRequest to be set")
	}
	// Both host rewrite and timeout should be present
	if rules[0].OriginRequest.HTTPHostHeader == nil || *rules[0].OriginRequest.HTTPHostHeader != "internal.example.com" {
		t.Errorf("httpHostHeader: got %v", rules[0].OriginRequest.HTTPHostHeader)
	}
	if rules[0].OriginRequest.ConnectTimeout == nil || rules[0].OriginRequest.ConnectTimeout.Duration != 30*time.Second {
		t.Errorf("connectTimeout: got %v, want 30s", rules[0].OriginRequest.ConnectTimeout)
	}
}

// ---------------------------------------------------------------------------
// Tests: BuildTLSIngressRules
// ---------------------------------------------------------------------------

func TestBuildTLSIngressRules_WithHostnames(t *testing.T) {
	p := gwapiv1.PortNumber(8443)
	route := gwapiv1alpha2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tls-route",
			Namespace: "default",
		},
		Spec: gwapiv1alpha2.TLSRouteSpec{
			Hostnames: []gwapiv1.Hostname{hostname("secure.example.com")},
			Rules: []gwapiv1alpha2.TLSRouteRule{{
				BackendRefs: []gwapiv1.BackendRef{{
					BackendObjectReference: gwapiv1.BackendObjectReference{
						Name: "tls-svc",
						Port: &p,
					},
				}},
			}},
		},
	}

	rules := BuildTLSIngressRules([]gwapiv1alpha2.TLSRoute{route})

	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].Hostname != "secure.example.com" {
		t.Errorf("hostname: got %q", rules[0].Hostname)
	}
	if rules[0].Service != "https://tls-svc.default:8443" {
		t.Errorf("service: got %q", rules[0].Service)
	}
	if rules[0].OriginRequest == nil || rules[0].OriginRequest.NoTLSVerify == nil || !*rules[0].OriginRequest.NoTLSVerify {
		t.Error("expected noTLSVerify: true")
	}
}

func TestBuildTLSIngressRules_DefaultPort443(t *testing.T) {
	route := gwapiv1alpha2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tls-route",
			Namespace: "default",
		},
		Spec: gwapiv1alpha2.TLSRouteSpec{
			Hostnames: []gwapiv1.Hostname{hostname("secure.example.com")},
			Rules: []gwapiv1alpha2.TLSRouteRule{{
				BackendRefs: []gwapiv1.BackendRef{{
					BackendObjectReference: gwapiv1.BackendObjectReference{
						Name: "tls-svc",
						// No port specified — should default to 443
					},
				}},
			}},
		},
	}

	rules := BuildTLSIngressRules([]gwapiv1alpha2.TLSRoute{route})

	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].Service != "https://tls-svc.default:443" {
		t.Errorf("service: got %q, want %q", rules[0].Service, "https://tls-svc.default:443")
	}
}

func TestBuildTLSIngressRules_NoBackendRefProduces503(t *testing.T) {
	route := gwapiv1alpha2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tls-empty",
			Namespace: "default",
		},
		Spec: gwapiv1alpha2.TLSRouteSpec{
			Hostnames: []gwapiv1.Hostname{hostname("example.com")},
			Rules:     []gwapiv1alpha2.TLSRouteRule{{
				// No BackendRefs
			}},
		},
	}

	rules := BuildTLSIngressRules([]gwapiv1alpha2.TLSRoute{route})

	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].Service != "http_status:503" {
		t.Errorf("service: got %q", rules[0].Service)
	}
}

// ---------------------------------------------------------------------------
// Tests: BuildTCPIngressRules
// ---------------------------------------------------------------------------

func TestBuildTCPIngressRules_BasicRoute(t *testing.T) {
	p := gwapiv1.PortNumber(5432)
	route := gwapiv1alpha2.TCPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tcp-route",
			Namespace: "default",
		},
		Spec: gwapiv1alpha2.TCPRouteSpec{
			Rules: []gwapiv1alpha2.TCPRouteRule{{
				BackendRefs: []gwapiv1.BackendRef{{
					BackendObjectReference: gwapiv1.BackendObjectReference{
						Name: "db-svc",
						Port: &p,
					},
				}},
			}},
		},
	}

	rules := BuildTCPIngressRules([]gwapiv1alpha2.TCPRoute{route})

	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].Service != "tcp://db-svc.default:5432" {
		t.Errorf("service: got %q, want %q", rules[0].Service, "tcp://db-svc.default:5432")
	}
	if rules[0].Hostname != "" {
		t.Errorf("hostname should be empty for TCPRoute, got %q", rules[0].Hostname)
	}
	if rules[0].OriginRequest != nil {
		t.Errorf("expected nil originRequest for TCPRoute, got %+v", rules[0].OriginRequest)
	}
}

func TestBuildTCPIngressRules_NoBackendRef(t *testing.T) {
	route := gwapiv1alpha2.TCPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tcp-empty",
			Namespace: "default",
		},
		Spec: gwapiv1alpha2.TCPRouteSpec{
			Rules: []gwapiv1alpha2.TCPRouteRule{{
				// No BackendRefs
			}},
		},
	}

	rules := BuildTCPIngressRules([]gwapiv1alpha2.TCPRoute{route})

	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].Service != "http_status:503" {
		t.Errorf("service: got %q, want %q", rules[0].Service, "http_status:503")
	}
}

// ---------------------------------------------------------------------------
// Tests: BuildGRPCIngressRules
// ---------------------------------------------------------------------------

func makeGRPCRoute(name, namespace string, hostnames []gwapiv1.Hostname, rules []gwapiv1.GRPCRouteRule) gwapiv1.GRPCRoute {
	return gwapiv1.GRPCRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: gwapiv1.GRPCRouteSpec{
			Hostnames: hostnames,
			Rules:     rules,
		},
	}
}

func makeGRPCBackendRef(name string, port int) gwapiv1.GRPCBackendRef {
	p := gwapiv1.PortNumber(port)
	return gwapiv1.GRPCBackendRef{
		BackendRef: gwapiv1.BackendRef{
			BackendObjectReference: gwapiv1.BackendObjectReference{
				Name: gwapiv1.ObjectName(name),
				Port: &p,
			},
		},
	}
}

func grpcMethodMatch(matchType gwapiv1.GRPCMethodMatchType, service, method *string) gwapiv1.GRPCRouteMatch {
	return gwapiv1.GRPCRouteMatch{
		Method: &gwapiv1.GRPCMethodMatch{
			Type:    &matchType,
			Service: service,
			Method:  method,
		},
	}
}

func strPtr(s string) *string { return &s }

func TestBuildGRPCIngressRules_ServiceAndMethod(t *testing.T) {
	route := makeGRPCRoute("grpc", "default",
		[]gwapiv1.Hostname{hostname("grpc.example.com")},
		[]gwapiv1.GRPCRouteRule{{
			BackendRefs: []gwapiv1.GRPCBackendRef{makeGRPCBackendRef("grpc-svc", 50051)},
			Matches: []gwapiv1.GRPCRouteMatch{
				grpcMethodMatch(gwapiv1.GRPCMethodMatchExact, strPtr("mypackage.MyService"), strPtr("GetItem")),
			},
		}},
	)

	rules := BuildGRPCIngressRules([]gwapiv1.GRPCRoute{route})

	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].Hostname != "grpc.example.com" {
		t.Errorf("hostname: got %q, want %q", rules[0].Hostname, "grpc.example.com")
	}
	// Dots should be escaped for exact match
	if rules[0].Path != `^/mypackage\.MyService/GetItem$` {
		t.Errorf("path: got %q, want %q", rules[0].Path, `^/mypackage\.MyService/GetItem$`)
	}
	if rules[0].Service != "http://grpc-svc.default:50051" {
		t.Errorf("service: got %q, want %q", rules[0].Service, "http://grpc-svc.default:50051")
	}
}

func TestBuildGRPCIngressRules_ServiceOnly(t *testing.T) {
	route := makeGRPCRoute("grpc", "default",
		[]gwapiv1.Hostname{hostname("grpc.example.com")},
		[]gwapiv1.GRPCRouteRule{{
			BackendRefs: []gwapiv1.GRPCBackendRef{makeGRPCBackendRef("grpc-svc", 50051)},
			Matches: []gwapiv1.GRPCRouteMatch{
				grpcMethodMatch(gwapiv1.GRPCMethodMatchExact, strPtr("mypackage.MyService"), nil),
			},
		}},
	)

	rules := BuildGRPCIngressRules([]gwapiv1.GRPCRoute{route})

	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	// Dots should be escaped for exact match
	if rules[0].Path != `^/mypackage\.MyService/` {
		t.Errorf("path: got %q, want %q", rules[0].Path, `^/mypackage\.MyService/`)
	}
}

func TestBuildGRPCIngressRules_RegexMatch(t *testing.T) {
	// Regex match should pass values through as-is without escaping
	route := makeGRPCRoute("grpc", "default",
		[]gwapiv1.Hostname{hostname("grpc.example.com")},
		[]gwapiv1.GRPCRouteRule{{
			BackendRefs: []gwapiv1.GRPCBackendRef{makeGRPCBackendRef("grpc-svc", 50051)},
			Matches: []gwapiv1.GRPCRouteMatch{
				grpcMethodMatch(gwapiv1.GRPCMethodMatchRegularExpression, strPtr("mypackage\\.My.*"), strPtr("Get.*")),
			},
		}},
	)

	rules := BuildGRPCIngressRules([]gwapiv1.GRPCRoute{route})

	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	// Regex values passed through as-is
	if rules[0].Path != `^/mypackage\.My.*/Get.*$` {
		t.Errorf("path: got %q, want %q", rules[0].Path, `^/mypackage\.My.*/Get.*$`)
	}
}

func TestBuildGRPCIngressRules_NoMatch(t *testing.T) {
	route := makeGRPCRoute("grpc", "default",
		[]gwapiv1.Hostname{hostname("grpc.example.com")},
		[]gwapiv1.GRPCRouteRule{{
			BackendRefs: []gwapiv1.GRPCBackendRef{makeGRPCBackendRef("grpc-svc", 50051)},
			// No matches — should match all
		}},
	)

	rules := BuildGRPCIngressRules([]gwapiv1.GRPCRoute{route})

	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].Path != "" {
		t.Errorf("path should be empty for no-match rule, got %q", rules[0].Path)
	}
	if rules[0].Hostname != "grpc.example.com" {
		t.Errorf("hostname: got %q, want %q", rules[0].Hostname, "grpc.example.com")
	}
}

func TestBuildGRPCIngressRules_Http2Origin(t *testing.T) {
	route := makeGRPCRoute("grpc", "default",
		[]gwapiv1.Hostname{hostname("grpc.example.com")},
		[]gwapiv1.GRPCRouteRule{{
			BackendRefs: []gwapiv1.GRPCBackendRef{makeGRPCBackendRef("grpc-svc", 50051)},
		}},
	)

	rules := BuildGRPCIngressRules([]gwapiv1.GRPCRoute{route})

	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].OriginRequest == nil {
		t.Fatal("expected originRequest to be set")
	}
	if rules[0].OriginRequest.Http2Origin == nil || !*rules[0].OriginRequest.Http2Origin {
		t.Error("expected http2Origin to be true")
	}
}

func TestBuildGRPCIngressRules_NoBackendRef(t *testing.T) {
	route := makeGRPCRoute("grpc", "default",
		[]gwapiv1.Hostname{hostname("grpc.example.com")},
		[]gwapiv1.GRPCRouteRule{{
			// No BackendRefs
		}},
	)

	rules := BuildGRPCIngressRules([]gwapiv1.GRPCRoute{route})

	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].Service != "http_status:503" {
		t.Errorf("service: got %q, want %q", rules[0].Service, "http_status:503")
	}
}
