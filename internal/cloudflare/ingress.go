package cloudflare

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

// BuildTunnelToken assembles the cloudflared tunnel token.
// Format: base64(json({"a": accountID, "t": tunnelID, "s": base64(secret)}))
func BuildTunnelToken(accountID, tunnelID string, secret []byte) string {
	payload := map[string]string{
		"a": accountID,
		"t": tunnelID,
		"s": base64.StdEncoding.EncodeToString(secret),
	}
	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		panic("BUG: failed to marshal tunnel token payload: " + err.Error())
	}
	return base64.StdEncoding.EncodeToString(jsonBytes)
}

// builtMatch is the per-match intermediate produced by extractMatches: the
// Cloudflare path pattern plus the Gateway API specificity of that match.
type builtMatch struct {
	path        string
	kind        pathKind
	litLen      int
	methodMatch bool
	headerCount int
	queryCount  int
	unsupported bool // match used a dimension Cloudflare can't enforce
}

// BuildIngressRules converts HTTPRoutes into Cloudflare tunnel ingress rules,
// tagged with route provenance and match specificity. It does NOT append a
// catch-all, sort, or convert — callers attach policy by route identity, sort by
// precedence, then call ToIngressRules.
func BuildIngressRules(routes []gwapiv1.HTTPRoute) []BuiltRule {
	var rules []BuiltRule

	for i := range routes {
		route := &routes[i]
		routeNS := routeNamespace(route.Namespace)

		for ri := range route.Spec.Rules {
			rule := &route.Spec.Rules[ri]
			service := backendRefToService(rule.BackendRefs, routeNS)
			originReq := buildOriginRequest(extractHostRewrite(rule.Filters))

			// Map HTTPRoute BackendRequest timeout to Cloudflare connectTimeout.
			// Gateway API admission should prevent invalid durations, so parse
			// errors are logged but not propagated.
			if rule.Timeouts != nil && rule.Timeouts.BackendRequest != nil {
				if timeout, err := parseGatewayDuration(string(*rule.Timeouts.BackendRequest)); err == nil {
					if originReq == nil {
						originReq = &OriginRequest{}
					}
					originReq.ConnectTimeout = timeout
				}
			}

			matches := extractMatches(rule.Matches)
			rules = append(rules, fanOutHTTPFamily("HTTPRoute", route.Name, routeNS, route.CreationTimestamp.Time, ri, route.Spec.Hostnames, service, originReq, matches)...)
		}
	}

	return rules
}

// BuildGRPCIngressRules converts GRPCRoutes into Cloudflare tunnel ingress rules.
// Every rule gets http2Origin=true since gRPC requires HTTP/2.
func BuildGRPCIngressRules(routes []gwapiv1.GRPCRoute) []BuiltRule {
	var rules []BuiltRule
	http2 := true

	for i := range routes {
		route := &routes[i]
		routeNS := routeNamespace(route.Namespace)

		for ri := range route.Spec.Rules {
			rule := &route.Spec.Rules[ri]
			service := grpcBackendRefToService(rule.BackendRefs, routeNS)
			originReq := &OriginRequest{HTTP2Origin: &http2}
			matches := extractGRPCMatches(rule.Matches)
			rules = append(rules, fanOutHTTPFamily("GRPCRoute", route.Name, routeNS, route.CreationTimestamp.Time, ri, route.Spec.Hostnames, service, originReq, matches)...)
		}
	}

	return rules
}

// BuildTLSIngressRules converts TLSRoutes into Cloudflare tunnel ingress rules.
// TLSRoutes map SNI hostnames to HTTPS backends with noTLSVerify.
func BuildTLSIngressRules(routes []gwapiv1alpha2.TLSRoute) []BuiltRule {
	var rules []BuiltRule

	for i := range routes {
		route := &routes[i]
		routeNS := routeNamespace(route.Namespace)

		for ri := range route.Spec.Rules {
			rule := &route.Spec.Rules[ri]
			service := backendRefToTLSService(rule.BackendRefs, routeNS)

			emit := func(host string) {
				noTLSVerify := true
				exact, length := hostnameSpecificity(host)
				rules = append(rules, BuiltRule{
					IngressRule: IngressRule{
						Hostname:      host,
						Service:       service,
						OriginRequest: &OriginRequest{NoTLSVerify: &noTLSVerify},
					},
					RouteKind:      "TLSRoute",
					RouteNamespace: routeNS,
					RouteName:      route.Name,
					RouteCreated:   route.CreationTimestamp.Time,
					RuleIndex:      ri,
					Specificity:    MatchSpecificity{HostnameExact: exact, HostnameLen: length, PathKind: pathNone},
				})
			}

			if len(route.Spec.Hostnames) == 0 {
				emit("")
			} else {
				for _, h := range route.Spec.Hostnames {
					emit(string(h))
				}
			}
		}
	}

	return rules
}

// BuildTCPIngressRules converts TCPRoutes into Cloudflare tunnel ingress rules.
// TCPRoutes have no hostnames — they are port-based and map to tcp:// backends.
func BuildTCPIngressRules(routes []gwapiv1alpha2.TCPRoute) []BuiltRule {
	var rules []BuiltRule

	for i := range routes {
		route := &routes[i]
		routeNS := routeNamespace(route.Namespace)

		for ri := range route.Spec.Rules {
			rule := &route.Spec.Rules[ri]
			service := backendRefToTCPService(rule.BackendRefs, routeNS)
			rules = append(rules, BuiltRule{
				IngressRule:    IngressRule{Service: service},
				RouteKind:      "TCPRoute",
				RouteNamespace: routeNS,
				RouteName:      route.Name,
				RouteCreated:   route.CreationTimestamp.Time,
				RuleIndex:      ri,
				Specificity:    MatchSpecificity{PathKind: pathNone},
			})
		}
	}

	return rules
}

// fanOutHTTPFamily expands a single HTTP/gRPC route rule into one BuiltRule per
// (hostname × match) combination, cloning the shared originRequest per rule so
// later in-place merges can't bleed across siblings.
func fanOutHTTPFamily(kind, name, routeNS string, created time.Time, ruleIndex int, hostnames []gwapiv1.Hostname, service string, originReq *OriginRequest, matches []builtMatch) []BuiltRule {
	var rules []BuiltRule

	emit := func(host string, m builtMatch) {
		exact, length := hostnameSpecificity(host)
		rules = append(rules, BuiltRule{
			IngressRule: IngressRule{
				Hostname:      host,
				Path:          m.path,
				Service:       service,
				OriginRequest: cloneOriginRequest(originReq),
			},
			RouteKind:      kind,
			RouteNamespace: routeNS,
			RouteName:      name,
			RouteCreated:   created,
			RuleIndex:      ruleIndex,
			Specificity: MatchSpecificity{
				HostnameExact: exact,
				HostnameLen:   length,
				PathKind:      m.kind,
				PrefixLen:     m.litLen,
				MethodMatch:   m.methodMatch,
				HeaderCount:   m.headerCount,
				QueryCount:    m.queryCount,
			},
			UnsupportedMatch: m.unsupported,
		})
	}

	if len(hostnames) == 0 {
		for _, m := range matches {
			emit("", m)
		}
		return rules
	}
	for _, h := range hostnames {
		for _, m := range matches {
			emit(string(h), m)
		}
	}
	return rules
}

func routeNamespace(ns string) string {
	if ns == "" {
		return "default"
	}
	return ns
}

// hostnameSpecificity returns the Gateway API hostname precedence components:
// exact hostnames outrank wildcards, and longer matching hostnames outrank
// shorter ones. An empty hostname matches all and is least specific.
func hostnameSpecificity(h string) (exact bool, length int) {
	if h == "" {
		return false, 0
	}
	if strings.HasPrefix(h, "*") {
		return false, len(strings.TrimPrefix(h, "*"))
	}
	return true, len(h)
}

// cloneOriginRequest returns a shallow copy so per-rule merges don't mutate a
// shared struct. The pointer fields are reassigned (never mutated in place) by
// MergeOriginRequest, so a shallow copy is sufficient.
func cloneOriginRequest(o *OriginRequest) *OriginRequest {
	if o == nil {
		return nil
	}
	c := *o
	return &c
}

func backendRefToTCPService(refs []gwapiv1.BackendRef, routeNS string) string {
	if len(refs) == 0 {
		return "http_status:503"
	}
	ref := refs[0]
	if ref.Port == nil {
		return "http_status:503"
	}
	ns := routeNS
	if ref.Namespace != nil {
		ns = string(*ref.Namespace)
	}
	return fmt.Sprintf("tcp://%s.%s:%d", ref.Name, ns, int(*ref.Port))
}

func backendRefToService(refs []gwapiv1.HTTPBackendRef, routeNS string) string {
	if len(refs) == 0 {
		return "http_status:503"
	}
	ref := refs[0]
	ns := routeNS
	if ref.Namespace != nil {
		ns = string(*ref.Namespace)
	}
	port := 80
	if ref.Port != nil {
		port = int(*ref.Port)
	}
	return fmt.Sprintf("http://%s.%s:%d", ref.Name, ns, port)
}

func backendRefToTLSService(refs []gwapiv1.BackendRef, routeNS string) string {
	if len(refs) == 0 {
		return "http_status:503"
	}
	ref := refs[0]
	ns := routeNS
	if ref.Namespace != nil {
		ns = string(*ref.Namespace)
	}
	port := 443
	if ref.Port != nil {
		port = int(*ref.Port)
	}
	return fmt.Sprintf("https://%s.%s:%d", ref.Name, ns, port)
}

// extractMatches converts an HTTPRoute rule's matches into per-match path
// patterns and specificity. A rule with no matches yields a single match-all
// entry so the rule still produces a (hostname-only) ingress rule.
func extractMatches(matches []gwapiv1.HTTPRouteMatch) []builtMatch {
	if len(matches) == 0 {
		return []builtMatch{{kind: pathNone}}
	}

	out := make([]builtMatch, 0, len(matches))
	for _, m := range matches {
		bm := builtMatch{
			headerCount: len(m.Headers),
			queryCount:  len(m.QueryParams),
			methodMatch: m.Method != nil,
		}
		// Cloudflare matches only hostname+path; method/header/query dimensions
		// can't be enforced, so flag them for status while still emitting the
		// best-effort hostname+path rule.
		bm.unsupported = bm.methodMatch || bm.headerCount > 0 || bm.queryCount > 0
		applyPathMatch(&bm, m.Path)
		out = append(out, bm)
	}
	return out
}

// applyPathMatch fills the path pattern + path specificity of a builtMatch from
// an HTTPRoute path match. Cloudflare's path field is a Go regex, so literal
// Exact/Prefix values are regex-escaped; prefixes are anchored to a segment
// boundary so "/foo" matches "/foo" and "/foo/bar" but not "/foobar".
func applyPathMatch(bm *builtMatch, p *gwapiv1.HTTPPathMatch) {
	if p == nil {
		bm.kind = pathNone
		return
	}
	value := "/"
	if p.Value != nil {
		value = *p.Value
	}
	pathType := gwapiv1.PathMatchPathPrefix
	if p.Type != nil {
		pathType = *p.Type
	}

	switch pathType {
	case gwapiv1.PathMatchExact:
		bm.path = "^" + regexp.QuoteMeta(value) + "$"
		bm.kind = pathExact
		bm.litLen = len(value)
	case gwapiv1.PathMatchPathPrefix:
		if value == "/" {
			// Root prefix matches everything — emit no path (match all).
			bm.kind = pathNone
			return
		}
		prefix := strings.TrimRight(value, "/")
		bm.path = "^" + regexp.QuoteMeta(prefix) + "(/.*)?$"
		bm.kind = pathPrefix
		bm.litLen = len(prefix)
	case gwapiv1.PathMatchRegularExpression:
		bm.path = value
		bm.kind = pathRegex
		bm.litLen = len(value)
	}
}

// extractHostRewrite checks HTTPRoute filters for host rewrite directives.
// Checks URLRewrite hostname first, then RequestHeaderModifier set Host.
func extractHostRewrite(filters []gwapiv1.HTTPRouteFilter) *string {
	for _, filter := range filters {
		switch filter.Type {
		case gwapiv1.HTTPRouteFilterURLRewrite:
			if filter.URLRewrite != nil && filter.URLRewrite.Hostname != nil {
				hostname := string(*filter.URLRewrite.Hostname)
				return &hostname
			}
		case gwapiv1.HTTPRouteFilterRequestHeaderModifier:
			if filter.RequestHeaderModifier != nil {
				for _, h := range filter.RequestHeaderModifier.Set {
					if strings.EqualFold(string(h.Name), "host") {
						value := h.Value
						return &value
					}
				}
			}
		}
	}
	return nil
}

func grpcBackendRefToService(refs []gwapiv1.GRPCBackendRef, routeNS string) string {
	if len(refs) == 0 {
		return "http_status:503"
	}
	ref := refs[0]
	ns := routeNS
	if ref.Namespace != nil {
		ns = string(*ref.Namespace)
	}
	port := 80
	if ref.Port != nil {
		port = int(*ref.Port)
	}
	return fmt.Sprintf("http://%s.%s:%d", ref.Name, ns, port)
}

// extractGRPCMatches converts a GRPCRoute rule's matches into per-match path
// patterns. gRPC method matches map to a path regex (so they ARE expressible by
// Cloudflare); a match with no usable method becomes a match-all entry.
func extractGRPCMatches(matches []gwapiv1.GRPCRouteMatch) []builtMatch {
	if len(matches) == 0 {
		return []builtMatch{{kind: pathNone}}
	}

	out := make([]builtMatch, 0, len(matches))
	for _, m := range matches {
		bm := builtMatch{}
		if path := grpcMethodPath(m.Method); path != "" {
			bm.path = path
			bm.kind = pathRegex
			bm.litLen = len(path)
			bm.methodMatch = true
		}
		out = append(out, bm)
	}
	return out
}

// grpcMethodPath builds the Cloudflare path regex for a gRPC method match, or ""
// when there is nothing to match. Exact matches escape dots; regex matches pass
// through as-is.
func grpcMethodPath(mm *gwapiv1.GRPCMethodMatch) string {
	if mm == nil {
		return ""
	}
	matchType := gwapiv1.GRPCMethodMatchExact
	if mm.Type != nil {
		matchType = *mm.Type
	}
	svc := ""
	if mm.Service != nil {
		svc = *mm.Service
	}
	method := ""
	if mm.Method != nil {
		method = *mm.Method
	}
	if svc == "" && method == "" {
		return ""
	}

	switch matchType {
	case gwapiv1.GRPCMethodMatchExact:
		escapedSvc := strings.ReplaceAll(svc, ".", "\\.")
		escapedMethod := strings.ReplaceAll(method, ".", "\\.")
		switch {
		case svc != "" && method != "":
			return "^/" + escapedSvc + "/" + escapedMethod + "$"
		case svc != "":
			return "^/" + escapedSvc + "/"
		default:
			return "^.*/" + escapedMethod + "$"
		}
	case gwapiv1.GRPCMethodMatchRegularExpression:
		switch {
		case svc != "" && method != "":
			return "^/" + svc + "/" + method + "$"
		case svc != "":
			return "^/" + svc + "/"
		default:
			return "^.*/" + method + "$"
		}
	}
	return ""
}

func buildOriginRequest(hostRewrite *string) *OriginRequest {
	if hostRewrite == nil {
		return nil
	}
	return &OriginRequest{
		HTTPHostHeader: hostRewrite,
	}
}

// parseGatewayDuration parses a Gateway API Duration string (e.g. "10s", "500ms")
// into a time.Duration. Returns an error if parsing fails.
func parseGatewayDuration(s string) (*time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return nil, fmt.Errorf("parsing gateway duration %q: %w", s, err)
	}
	return &d, nil
}
