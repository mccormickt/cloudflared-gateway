package controller

import (
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// CheckRouteAttachment checks whether a route in the given namespace with the
// given kind is allowed to attach to the Gateway based on its listener configuration.
func CheckRouteAttachment(gw *gwapiv1.Gateway, routeNS, routeKind string) bool {
	for _, listener := range gw.Spec.Listeners {
		// Check if the route kind is allowed for this listener
		if !isKindAllowed(listener, routeKind) {
			continue
		}

		// Check namespace policy
		if !isNamespaceAllowed(listener, gw.Namespace, routeNS) {
			continue
		}

		return true
	}
	return false
}

func isKindAllowed(listener gwapiv1.Listener, routeKind string) bool {
	if listener.AllowedRoutes == nil || len(listener.AllowedRoutes.Kinds) == 0 {
		// Default kinds based on protocol
		return defaultKindForProtocol(listener.Protocol, routeKind)
	}

	for _, allowed := range listener.AllowedRoutes.Kinds {
		if string(allowed.Kind) == routeKind {
			// Group must be gateway.networking.k8s.io or empty
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
		return routeKind == "HTTPRoute"
	case gwapiv1.TLSProtocolType:
		return routeKind == "TLSRoute"
	default:
		return false
	}
}

func isNamespaceAllowed(listener gwapiv1.Listener, gwNS, routeNS string) bool {
	if listener.AllowedRoutes == nil || listener.AllowedRoutes.Namespaces == nil || listener.AllowedRoutes.Namespaces.From == nil {
		// Default: Same namespace
		return gwNS == routeNS
	}

	switch *listener.AllowedRoutes.Namespaces.From {
	case gwapiv1.NamespacesFromAll:
		return true
	case gwapiv1.NamespacesFromSame:
		return gwNS == routeNS
	case gwapiv1.NamespacesFromSelector:
		// Selector-based namespace matching would require listing namespaces.
		// For simplicity, allow if selector is set (full selector evaluation
		// requires a client, which we avoid in this pure function).
		return true
	default:
		return false
	}
}
