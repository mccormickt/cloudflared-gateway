package cloudflare

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	cf "github.com/cloudflare/cloudflare-go"
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
	jsonBytes, _ := json.Marshal(payload)
	return base64.StdEncoding.EncodeToString(jsonBytes)
}

// BuildIngressRules converts HTTPRoutes into Cloudflare tunnel ingress rules.
// Does NOT append a catch-all rule — the caller is responsible for that.
func BuildIngressRules(routes []gwapiv1.HTTPRoute) []cf.UnvalidatedIngressRule {
	var rules []cf.UnvalidatedIngressRule

	for i := range routes {
		route := &routes[i]
		hostnames := route.Spec.Hostnames
		routeNS := route.Namespace
		if routeNS == "" {
			routeNS = "default"
		}

		for _, rule := range route.Spec.Rules {
			service := backendRefToService(rule.BackendRefs, routeNS)
			originReq := buildOriginRequest(extractHostRewrite(rule.Filters))
			paths := extractPaths(rule.Matches)

			if len(hostnames) == 0 {
				if len(paths) == 0 {
					rules = append(rules, cf.UnvalidatedIngressRule{
						Service:       service,
						OriginRequest: originReq,
					})
				} else {
					for _, path := range paths {
						rules = append(rules, cf.UnvalidatedIngressRule{
							Path:          path,
							Service:       service,
							OriginRequest: originReq,
						})
					}
				}
			} else {
				for _, hostname := range hostnames {
					if len(paths) == 0 {
						rules = append(rules, cf.UnvalidatedIngressRule{
							Hostname:      string(hostname),
							Service:       service,
							OriginRequest: originReq,
						})
					} else {
						for _, path := range paths {
							rules = append(rules, cf.UnvalidatedIngressRule{
								Hostname:      string(hostname),
								Path:          path,
								Service:       service,
								OriginRequest: originReq,
							})
						}
					}
				}
			}
		}
	}

	return rules
}

// BuildTLSIngressRules converts TLSRoutes into Cloudflare tunnel ingress rules.
// TLSRoutes map SNI hostnames to HTTPS backends with noTLSVerify.
// Does NOT append a catch-all rule — the caller is responsible for that.
func BuildTLSIngressRules(routes []gwapiv1alpha2.TLSRoute) []cf.UnvalidatedIngressRule {
	var rules []cf.UnvalidatedIngressRule
	noTLSVerify := true

	for i := range routes {
		route := &routes[i]
		hostnames := route.Spec.Hostnames
		routeNS := route.Namespace
		if routeNS == "" {
			routeNS = "default"
		}

		for _, rule := range route.Spec.Rules {
			service := backendRefToTLSService(rule.BackendRefs, routeNS)
			originReq := &cf.OriginRequestConfig{
				NoTLSVerify: &noTLSVerify,
			}

			if len(hostnames) == 0 {
				rules = append(rules, cf.UnvalidatedIngressRule{
					Service:       service,
					OriginRequest: originReq,
				})
			} else {
				for _, hostname := range hostnames {
					rules = append(rules, cf.UnvalidatedIngressRule{
						Hostname:      string(hostname),
						Service:       service,
						OriginRequest: originReq,
					})
				}
			}
		}
	}

	return rules
}

// BuildTCPIngressRules converts TCPRoutes into Cloudflare tunnel ingress rules.
// TCPRoutes have no hostnames — they are port-based and map to tcp:// backends.
// Does NOT append a catch-all rule — the caller is responsible for that.
func BuildTCPIngressRules(routes []gwapiv1alpha2.TCPRoute) []cf.UnvalidatedIngressRule {
	var rules []cf.UnvalidatedIngressRule

	for i := range routes {
		route := &routes[i]
		routeNS := route.Namespace
		if routeNS == "" {
			routeNS = "default"
		}

		for _, rule := range route.Spec.Rules {
			service := backendRefToTCPService(rule.BackendRefs, routeNS)
			rules = append(rules, cf.UnvalidatedIngressRule{
				Service: service,
			})
		}
	}

	return rules
}

func backendRefToTCPService(refs []gwapiv1.BackendRef, routeNS string) string {
	if len(refs) == 0 {
		return "http_status:503"
	}
	ref := refs[0]
	ns := routeNS
	if ref.Namespace != nil {
		ns = string(*ref.Namespace)
	}
	port := 0
	if ref.Port != nil {
		port = int(*ref.Port)
	}
	return fmt.Sprintf("tcp://%s.%s:%d", ref.Name, ns, port)
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

func extractPaths(matches []gwapiv1.HTTPRouteMatch) []string {
	var paths []string
	for _, m := range matches {
		if m.Path == nil {
			continue
		}
		value := "/"
		if m.Path.Value != nil {
			value = *m.Path.Value
		}
		pathType := gwapiv1.PathMatchPathPrefix
		if m.Path.Type != nil {
			pathType = *m.Path.Type
		}

		switch pathType {
		case gwapiv1.PathMatchExact:
			paths = append(paths, "^"+value+"$")
		case gwapiv1.PathMatchPathPrefix:
			if value == "/" {
				// Root prefix matches everything — omit path (empty = match all)
				continue
			}
			paths = append(paths, "^"+value)
		case gwapiv1.PathMatchRegularExpression:
			paths = append(paths, value)
		}
	}
	return paths
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

// BuildGRPCIngressRules converts GRPCRoutes into Cloudflare tunnel ingress rules.
// Every rule gets http2Origin=true since gRPC requires HTTP/2.
// Does NOT append a catch-all rule — the caller is responsible for that.
func BuildGRPCIngressRules(routes []gwapiv1.GRPCRoute) []cf.UnvalidatedIngressRule {
	var rules []cf.UnvalidatedIngressRule
	http2 := true

	for i := range routes {
		route := &routes[i]
		hostnames := route.Spec.Hostnames
		routeNS := route.Namespace
		if routeNS == "" {
			routeNS = "default"
		}

		for _, rule := range route.Spec.Rules {
			service := grpcBackendRefToService(rule.BackendRefs, routeNS)
			originReq := &cf.OriginRequestConfig{
				Http2Origin: &http2,
			}
			paths := extractGRPCPaths(rule.Matches)

			if len(hostnames) == 0 {
				if len(paths) == 0 {
					rules = append(rules, cf.UnvalidatedIngressRule{
						Service:       service,
						OriginRequest: originReq,
					})
				} else {
					for _, path := range paths {
						rules = append(rules, cf.UnvalidatedIngressRule{
							Path:          path,
							Service:       service,
							OriginRequest: originReq,
						})
					}
				}
			} else {
				for _, hostname := range hostnames {
					if len(paths) == 0 {
						rules = append(rules, cf.UnvalidatedIngressRule{
							Hostname:      string(hostname),
							Service:       service,
							OriginRequest: originReq,
						})
					} else {
						for _, path := range paths {
							rules = append(rules, cf.UnvalidatedIngressRule{
								Hostname:      string(hostname),
								Path:          path,
								Service:       service,
								OriginRequest: originReq,
							})
						}
					}
				}
			}
		}
	}

	return rules
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

func extractGRPCPaths(matches []gwapiv1.GRPCRouteMatch) []string {
	var paths []string
	for _, m := range matches {
		if m.Method == nil {
			continue
		}
		matchType := gwapiv1.GRPCMethodMatchExact
		if m.Method.Type != nil {
			matchType = *m.Method.Type
		}

		svc := ""
		if m.Method.Service != nil {
			svc = *m.Method.Service
		}
		method := ""
		if m.Method.Method != nil {
			method = *m.Method.Method
		}

		if svc == "" && method == "" {
			continue
		}

		switch matchType {
		case gwapiv1.GRPCMethodMatchExact:
			if svc != "" && method != "" {
				paths = append(paths, "^/"+svc+"/"+method+"$")
			} else if svc != "" {
				paths = append(paths, "^/"+svc+"/")
			} else {
				// method only
				paths = append(paths, "^.*/"+method+"$")
			}
		case gwapiv1.GRPCMethodMatchRegularExpression:
			if svc != "" && method != "" {
				paths = append(paths, "^/"+svc+"/"+method+"$")
			} else if svc != "" {
				paths = append(paths, "^/"+svc+"/")
			} else {
				paths = append(paths, "^.*/"+method+"$")
			}
		}
	}
	return paths
}

func buildOriginRequest(hostRewrite *string) *cf.OriginRequestConfig {
	if hostRewrite == nil {
		return nil
	}
	return &cf.OriginRequestConfig{
		HTTPHostHeader: hostRewrite,
	}
}
