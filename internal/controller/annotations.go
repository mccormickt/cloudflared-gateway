package controller

import (
	"log/slog"

	cf "github.com/cloudflare/cloudflare-go"
	cfclient "github.com/mccormickt/cloudflare-tunnel-controller/internal/cloudflare"

	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

// applyHTTPRouteAnnotations post-processes HTTP ingress rules to merge
// Cloudflare-specific origin request config from route annotations.
// Walks routes in the same order as BuildIngressRules to match rules to routes.
func applyHTTPRouteAnnotations(rules []cf.UnvalidatedIngressRule, httpRoutes []gwapiv1.HTTPRoute) []cf.UnvalidatedIngressRule {
	if len(httpRoutes) == 0 {
		return rules
	}

	idx := 0
	for i := range httpRoutes {
		route := &httpRoutes[i]
		annoCfg, warnings := cfclient.ParseOriginAnnotations(route.Annotations)
		for _, w := range warnings {
			slog.Warn("HTTPRoute annotation warning", "route", route.Name, "warning", w)
		}
		if annoCfg == nil {
			// Still need to advance idx past this route's rules
			for _, rule := range route.Spec.Rules {
				paths := countPaths(rule.Matches)
				idx += rulesProduced(len(route.Spec.Hostnames), paths)
			}
			continue
		}

		for _, rule := range route.Spec.Rules {
			paths := countPaths(rule.Matches)
			ruleCount := rulesProduced(len(route.Spec.Hostnames), paths)
			for j := 0; j < ruleCount && idx+j < len(rules); j++ {
				rules[idx+j].OriginRequest = cfclient.MergeOriginRequest(rules[idx+j].OriginRequest, annoCfg)
			}
			idx += ruleCount
		}
	}

	return rules
}

// applyGRPCRouteAnnotations post-processes gRPC ingress rules to merge
// Cloudflare-specific origin request config from route annotations.
func applyGRPCRouteAnnotations(rules []cf.UnvalidatedIngressRule, grpcRoutes []gwapiv1.GRPCRoute) []cf.UnvalidatedIngressRule {
	if len(grpcRoutes) == 0 {
		return rules
	}

	idx := 0
	for i := range grpcRoutes {
		route := &grpcRoutes[i]
		annoCfg, warnings := cfclient.ParseOriginAnnotations(route.Annotations)
		for _, w := range warnings {
			slog.Warn("GRPCRoute annotation warning", "route", route.Name, "warning", w)
		}
		if annoCfg == nil {
			for _, rule := range route.Spec.Rules {
				paths := countGRPCPaths(rule.Matches)
				idx += rulesProduced(len(route.Spec.Hostnames), paths)
			}
			continue
		}

		for _, rule := range route.Spec.Rules {
			paths := countGRPCPaths(rule.Matches)
			ruleCount := rulesProduced(len(route.Spec.Hostnames), paths)
			for j := 0; j < ruleCount && idx+j < len(rules); j++ {
				rules[idx+j].OriginRequest = cfclient.MergeOriginRequest(rules[idx+j].OriginRequest, annoCfg)
			}
			idx += ruleCount
		}
	}

	return rules
}

// applyTLSRouteAnnotations post-processes TLS ingress rules to merge
// Cloudflare-specific origin request config from route annotations.
func applyTLSRouteAnnotations(rules []cf.UnvalidatedIngressRule, tlsRoutes []gwapiv1alpha2.TLSRoute) []cf.UnvalidatedIngressRule {
	if len(tlsRoutes) == 0 {
		return rules
	}

	idx := 0
	for i := range tlsRoutes {
		route := &tlsRoutes[i]
		annoCfg, warnings := cfclient.ParseOriginAnnotations(route.Annotations)
		for _, w := range warnings {
			slog.Warn("TLSRoute annotation warning", "route", route.Name, "warning", w)
		}
		if annoCfg == nil {
			for range route.Spec.Rules {
				idx += tlsRulesProduced(len(route.Spec.Hostnames))
			}
			continue
		}

		for range route.Spec.Rules {
			ruleCount := tlsRulesProduced(len(route.Spec.Hostnames))
			for j := 0; j < ruleCount && idx+j < len(rules); j++ {
				rules[idx+j].OriginRequest = cfclient.MergeOriginRequest(rules[idx+j].OriginRequest, annoCfg)
			}
			idx += ruleCount
		}
	}

	return rules
}

// applyTCPRouteAnnotations post-processes TCP ingress rules to merge
// Cloudflare-specific origin request config from route annotations.
func applyTCPRouteAnnotations(rules []cf.UnvalidatedIngressRule, tcpRoutes []gwapiv1alpha2.TCPRoute) []cf.UnvalidatedIngressRule {
	if len(tcpRoutes) == 0 {
		return rules
	}

	idx := 0
	for i := range tcpRoutes {
		route := &tcpRoutes[i]
		annoCfg, warnings := cfclient.ParseOriginAnnotations(route.Annotations)
		for _, w := range warnings {
			slog.Warn("TCPRoute annotation warning", "route", route.Name, "warning", w)
		}
		if annoCfg == nil {
			idx += len(route.Spec.Rules)
			continue
		}

		for range route.Spec.Rules {
			if idx < len(rules) {
				rules[idx].OriginRequest = cfclient.MergeOriginRequest(rules[idx].OriginRequest, annoCfg)
			}
			idx++
		}
	}

	return rules
}

// tlsRulesProduced returns how many ingress rules a single TLS route rule
// produces, matching the logic in BuildTLSIngressRules.
func tlsRulesProduced(numHostnames int) int {
	if numHostnames == 0 {
		return 1
	}
	return numHostnames
}

// countGRPCPaths returns the number of paths extracted from gRPC matches,
// mirroring the logic in extractGRPCPaths from ingress.go.
func countGRPCPaths(matches []gwapiv1.GRPCRouteMatch) int {
	count := 0
	for _, m := range matches {
		if m.Method == nil {
			continue
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
		count++
	}
	return count
}
