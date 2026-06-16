package controller

import (
	"context"
	"strings"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// CheckRouteAttachment checks whether a route in the given namespace with the
// given kind is allowed to attach to the Gateway based on its listener configuration.
func CheckRouteAttachment(ctx context.Context, c client.Client, gw *gwapiv1.Gateway, routeNS, routeKind string) (bool, error) {
	for _, listener := range gw.Spec.Listeners {
		if !isKindAllowed(listener, routeKind) {
			continue
		}

		allowed, err := isNamespaceAllowed(ctx, c, listener, gw.Namespace, routeNS)
		if err != nil {
			return false, err
		}
		if allowed {
			return true, nil
		}
	}
	return false, nil
}

// routeAttachmentHostnames returns the effective hostnames for a route after
// applying parentRef listener selection, allowed route kinds and namespaces,
// and listener hostname intersection. The boolean reports whether the route
// attaches to at least one selected listener.
func routeAttachmentHostnames(
	ctx context.Context,
	c client.Client,
	gw *gwapiv1.Gateway,
	parentRefs []gwapiv1.ParentReference,
	routeNS, routeKind string,
	hostnames []gwapiv1.Hostname,
) ([]gwapiv1.Hostname, bool, error) {
	seen := map[gwapiv1.Hostname]bool{}
	var effective []gwapiv1.Hostname

	for _, listener := range gw.Spec.Listeners {
		if !parentRefsSelectListener(parentRefs, gw, listener) || !isKindAllowed(listener, routeKind) {
			continue
		}

		allowed, err := isNamespaceAllowed(ctx, c, listener, gw.Namespace, routeNS)
		if err != nil {
			return nil, false, err
		}
		if !allowed {
			continue
		}

		intersection, ok := intersectHostnames(hostnames, listener.Hostname)
		if !ok {
			continue
		}
		if len(intersection) == 0 {
			return nil, true, nil
		}
		for _, hostname := range intersection {
			if !seen[hostname] {
				seen[hostname] = true
				effective = append(effective, hostname)
			}
		}
	}

	return effective, len(effective) > 0, nil
}

func parentRefsSelectListener(parentRefs []gwapiv1.ParentReference, gw *gwapiv1.Gateway, listener gwapiv1.Listener) bool {
	for _, ref := range parentRefs {
		if !parentRefReferencesGateway(ref, gw) {
			continue
		}
		if ref.SectionName != nil && *ref.SectionName != listener.Name {
			continue
		}
		if ref.Port != nil && *ref.Port != listener.Port {
			continue
		}
		return true
	}
	return false
}

func parentRefReferencesGateway(ref gwapiv1.ParentReference, gw *gwapiv1.Gateway) bool {
	group := gwapiv1.GroupName
	if ref.Group != nil {
		group = string(*ref.Group)
	}
	kind := "Gateway"
	if ref.Kind != nil {
		kind = string(*ref.Kind)
	}
	if group != gwapiv1.GroupName || kind != "Gateway" {
		return false
	}

	namespace := gw.Namespace
	if ref.Namespace != nil {
		namespace = string(*ref.Namespace)
	}
	return string(ref.Name) == gw.Name && namespace == gw.Namespace
}

func intersectHostnames(routeHostnames []gwapiv1.Hostname, listenerHostname *gwapiv1.Hostname) ([]gwapiv1.Hostname, bool) {
	if listenerHostname == nil {
		return append([]gwapiv1.Hostname(nil), routeHostnames...), true
	}
	if len(routeHostnames) == 0 {
		return []gwapiv1.Hostname{normalizeHostname(*listenerHostname)}, true
	}

	seen := map[gwapiv1.Hostname]bool{}
	var intersection []gwapiv1.Hostname
	for _, routeHostname := range routeHostnames {
		hostname, ok := intersectHostname(routeHostname, *listenerHostname)
		if ok && !seen[hostname] {
			seen[hostname] = true
			intersection = append(intersection, hostname)
		}
	}
	return intersection, len(intersection) > 0
}

func intersectHostname(routeHostname, listenerHostname gwapiv1.Hostname) (gwapiv1.Hostname, bool) {
	route := string(normalizeHostname(routeHostname))
	listener := string(normalizeHostname(listenerHostname))
	routeWildcard := strings.HasPrefix(route, "*.")
	listenerWildcard := strings.HasPrefix(listener, "*.")

	switch {
	case !routeWildcard && !listenerWildcard:
		return gwapiv1.Hostname(route), route == listener
	case routeWildcard && !listenerWildcard:
		return gwapiv1.Hostname(listener), wildcardMatches(route, listener)
	case !routeWildcard && listenerWildcard:
		return gwapiv1.Hostname(route), wildcardMatches(listener, route)
	case route == listener:
		return gwapiv1.Hostname(route), true
	case wildcardMoreSpecific(route, listener):
		return gwapiv1.Hostname(route), true
	case wildcardMoreSpecific(listener, route):
		return gwapiv1.Hostname(listener), true
	default:
		return "", false
	}
}

func normalizeHostname(hostname gwapiv1.Hostname) gwapiv1.Hostname {
	return gwapiv1.Hostname(strings.ToLower(strings.TrimSuffix(string(hostname), ".")))
}

func wildcardMatches(wildcard, hostname string) bool {
	suffix := strings.TrimPrefix(wildcard, "*.")
	return strings.HasSuffix(hostname, "."+suffix)
}

func wildcardMoreSpecific(hostname, other string) bool {
	suffix := strings.TrimPrefix(hostname, "*.")
	otherSuffix := strings.TrimPrefix(other, "*.")
	return strings.HasSuffix(suffix, "."+otherSuffix)
}

func isKindAllowed(listener gwapiv1.Listener, routeKind string) bool {
	if listener.AllowedRoutes == nil || len(listener.AllowedRoutes.Kinds) == 0 {
		return defaultKindForProtocol(listener.Protocol, routeKind)
	}

	for _, allowed := range listener.AllowedRoutes.Kinds {
		if string(allowed.Kind) == routeKind {
			if allowed.Group == nil || *allowed.Group == "" || *allowed.Group == gwapiv1.GroupName {
				return true
			}
		}
	}
	return false
}

func defaultKindForProtocol(protocol gwapiv1.ProtocolType, routeKind string) bool {
	switch protocol {
	case gwapiv1.HTTPProtocolType, gwapiv1.HTTPSProtocolType:
		return routeKind == "HTTPRoute" || routeKind == "GRPCRoute"
	case gwapiv1.TLSProtocolType:
		return routeKind == "TLSRoute"
	case gwapiv1.TCPProtocolType:
		return routeKind == "TCPRoute"
	default:
		return false
	}
}

func isNamespaceAllowed(ctx context.Context, c client.Client, listener gwapiv1.Listener, gwNS, routeNS string) (bool, error) {
	if listener.AllowedRoutes == nil || listener.AllowedRoutes.Namespaces == nil || listener.AllowedRoutes.Namespaces.From == nil {
		return gwNS == routeNS, nil
	}

	switch *listener.AllowedRoutes.Namespaces.From {
	case gwapiv1.NamespacesFromAll:
		return true, nil
	case gwapiv1.NamespacesFromSame:
		return gwNS == routeNS, nil
	case gwapiv1.NamespacesFromSelector:
		return matchNamespaceSelector(ctx, c, routeNS, listener.AllowedRoutes.Namespaces.Selector)
	default:
		return false, nil
	}
}

// matchNamespaceSelector fetches the Namespace and evaluates the label selector.
func matchNamespaceSelector(ctx context.Context, c client.Client, namespace string, selector *metav1.LabelSelector) (bool, error) {
	if selector == nil {
		return true, nil // No selector means match all
	}

	var ns v1.Namespace
	if err := c.Get(ctx, types.NamespacedName{Name: namespace}, &ns); err != nil {
		return false, err
	}

	nsLabels := ns.Labels
	if nsLabels == nil {
		nsLabels = map[string]string{}
	}

	// Check matchLabels — all must match
	for key, value := range selector.MatchLabels {
		if nsLabels[key] != value {
			return false, nil
		}
	}

	// Check matchExpressions — all must match
	for _, expr := range selector.MatchExpressions {
		labelValue, hasLabel := nsLabels[expr.Key]

		switch expr.Operator {
		case metav1.LabelSelectorOpIn:
			if !hasLabel || !containsString(expr.Values, labelValue) {
				return false, nil
			}
		case metav1.LabelSelectorOpNotIn:
			if hasLabel && containsString(expr.Values, labelValue) {
				return false, nil
			}
		case metav1.LabelSelectorOpExists:
			if !hasLabel {
				return false, nil
			}
		case metav1.LabelSelectorOpDoesNotExist:
			if hasLabel {
				return false, nil
			}
		}
	}

	return true, nil
}

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
