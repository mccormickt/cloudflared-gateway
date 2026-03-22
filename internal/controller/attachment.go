package controller

import (
	"context"

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
