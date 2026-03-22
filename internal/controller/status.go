package controller

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

// PatchGatewayClassStatus sets the Accepted condition on a GatewayClass.
func PatchGatewayClassStatus(ctx context.Context, c client.Client, gc *gwapiv1.GatewayClass, accepted bool) error {
	status := metav1.ConditionTrue
	reason := string(gwapiv1.GatewayClassReasonAccepted)
	message := "GatewayClass is accepted"
	if !accepted {
		status = metav1.ConditionFalse
		reason = "InvalidParameters"
		message = "GatewayClass controller name does not match"
	}

	condition := metav1.Condition{
		Type:               string(gwapiv1.GatewayClassConditionStatusAccepted),
		Status:             status,
		ObservedGeneration: gc.Generation,
		LastTransitionTime: transitionTime(gc.Status.Conditions, string(gwapiv1.GatewayClassConditionStatusAccepted), status),
		Reason:             reason,
		Message:            message,
	}

	gc.Status.Conditions = setCondition(gc.Status.Conditions, condition)
	return c.Status().Update(ctx, gc)
}

// ListenerRouteCount tracks the number of attached routes per listener.
type ListenerRouteCount struct {
	Name     gwapiv1.SectionName
	Protocol gwapiv1.ProtocolType
	Count    int32
}

// PatchGatewayStatus sets Accepted+Programmed conditions and listener statuses.
func PatchGatewayStatus(ctx context.Context, c client.Client, gw *gwapiv1.Gateway, tunnelID string, listenerCounts []ListenerRouteCount) error {
	now := metav1.Now()

	acceptedCond := metav1.Condition{
		Type:               string(gwapiv1.GatewayConditionAccepted),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: gw.Generation,
		LastTransitionTime: transitionTime(gw.Status.Conditions, string(gwapiv1.GatewayConditionAccepted), metav1.ConditionTrue),
		Reason:             string(gwapiv1.GatewayReasonAccepted),
		Message:            "Gateway is accepted",
	}

	programmedCond := metav1.Condition{
		Type:               string(gwapiv1.GatewayConditionProgrammed),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: gw.Generation,
		LastTransitionTime: transitionTime(gw.Status.Conditions, string(gwapiv1.GatewayConditionProgrammed), metav1.ConditionTrue),
		Reason:             string(gwapiv1.GatewayReasonProgrammed),
		Message:            "Tunnel is configured",
	}

	gw.Status.Conditions = setCondition(gw.Status.Conditions, acceptedCond)
	gw.Status.Conditions = setCondition(gw.Status.Conditions, programmedCond)

	// Set tunnel address
	if tunnelID != "" {
		hostname := fmt.Sprintf("%s.cfargotunnel.com", tunnelID)
		addrType := gwapiv1.HostnameAddressType
		gw.Status.Addresses = []gwapiv1.GatewayStatusAddress{{
			Type:  &addrType,
			Value: hostname,
		}}
	}

	// Build listener statuses
	gw.Status.Listeners = nil
	for _, lc := range listenerCounts {
		ls := gwapiv1.ListenerStatus{
			Name:           lc.Name,
			AttachedRoutes: lc.Count,
			SupportedKinds: supportedKindsForProtocol(lc.Protocol),
			Conditions: []metav1.Condition{{
				Type:               string(gwapiv1.ListenerConditionAccepted),
				Status:             metav1.ConditionTrue,
				ObservedGeneration: gw.Generation,
				LastTransitionTime: now,
				Reason:             string(gwapiv1.ListenerReasonAccepted),
				Message:            "Listener is accepted",
			}},
		}
		gw.Status.Listeners = append(gw.Status.Listeners, ls)
	}

	return c.Status().Update(ctx, gw)
}

// PatchHTTPRouteStatus sets the Accepted condition for a specific parentRef on an HTTPRoute.
func PatchHTTPRouteStatus(ctx context.Context, c client.Client, route *gwapiv1.HTTPRoute, gwName, gwNS string, accepted bool) error {
	status := metav1.ConditionTrue
	reason := string(gwapiv1.RouteReasonAccepted)
	message := "Route is accepted"
	if !accepted {
		status = metav1.ConditionFalse
		reason = string(gwapiv1.RouteReasonNotAllowedByListeners)
		message = "Route is not allowed by listeners"
	}

	gwGroup := gwapiv1.Group(gwapiv1.GroupName)
	gwKind := gwapiv1.Kind("Gateway")
	gwNamespace := gwapiv1.Namespace(gwNS)

	parentStatus := gwapiv1.RouteParentStatus{
		ParentRef: gwapiv1.ParentReference{
			Group:     &gwGroup,
			Kind:      &gwKind,
			Namespace: &gwNamespace,
			Name:      gwapiv1.ObjectName(gwName),
		},
		ControllerName: gwapiv1.GatewayController(ControllerName),
		Conditions: []metav1.Condition{{
			Type:               string(gwapiv1.RouteConditionAccepted),
			Status:             status,
			ObservedGeneration: route.Generation,
			LastTransitionTime: metav1.Now(),
			Reason:             reason,
			Message:            message,
		}},
	}

	route.Status.Parents = setParentStatus(route.Status.Parents, parentStatus, gwName, gwNS)
	return c.Status().Update(ctx, route)
}

// PatchTLSRouteStatus sets the Accepted condition for a specific parentRef on a TLSRoute.
func PatchTLSRouteStatus(ctx context.Context, c client.Client, route *gwapiv1alpha2.TLSRoute, gwName, gwNS string, accepted bool) error {
	status := metav1.ConditionTrue
	reason := string(gwapiv1.RouteReasonAccepted)
	message := "Route is accepted"
	if !accepted {
		status = metav1.ConditionFalse
		reason = string(gwapiv1.RouteReasonNotAllowedByListeners)
		message = "Route is not allowed by listeners"
	}

	gwGroup := gwapiv1.Group(gwapiv1.GroupName)
	gwKind := gwapiv1.Kind("Gateway")
	gwNamespace := gwapiv1.Namespace(gwNS)

	parentStatus := gwapiv1.RouteParentStatus{
		ParentRef: gwapiv1.ParentReference{
			Group:     &gwGroup,
			Kind:      &gwKind,
			Namespace: &gwNamespace,
			Name:      gwapiv1.ObjectName(gwName),
		},
		ControllerName: gwapiv1.GatewayController(ControllerName),
		Conditions: []metav1.Condition{{
			Type:               string(gwapiv1.RouteConditionAccepted),
			Status:             status,
			ObservedGeneration: route.Generation,
			LastTransitionTime: metav1.Now(),
			Reason:             reason,
			Message:            message,
		}},
	}

	route.Status.Parents = setParentStatus(route.Status.Parents, parentStatus, gwName, gwNS)
	return c.Status().Update(ctx, route)
}

// PatchTCPRouteStatus sets the Accepted condition for a specific parentRef on a TCPRoute.
func PatchTCPRouteStatus(ctx context.Context, c client.Client, route *gwapiv1alpha2.TCPRoute, gwName, gwNS string, accepted bool) error {
	status := metav1.ConditionTrue
	reason := string(gwapiv1.RouteReasonAccepted)
	message := "Route is accepted"
	if !accepted {
		status = metav1.ConditionFalse
		reason = string(gwapiv1.RouteReasonNotAllowedByListeners)
		message = "Route is not allowed by listeners"
	}

	gwGroup := gwapiv1.Group(gwapiv1.GroupName)
	gwKind := gwapiv1.Kind("Gateway")
	gwNamespace := gwapiv1.Namespace(gwNS)

	parentStatus := gwapiv1.RouteParentStatus{
		ParentRef: gwapiv1.ParentReference{
			Group:     &gwGroup,
			Kind:      &gwKind,
			Namespace: &gwNamespace,
			Name:      gwapiv1.ObjectName(gwName),
		},
		ControllerName: gwapiv1.GatewayController(ControllerName),
		Conditions: []metav1.Condition{{
			Type:               string(gwapiv1.RouteConditionAccepted),
			Status:             status,
			ObservedGeneration: route.Generation,
			LastTransitionTime: metav1.Now(),
			Reason:             reason,
			Message:            message,
		}},
	}

	route.Status.Parents = setParentStatus(route.Status.Parents, parentStatus, gwName, gwNS)
	return c.Status().Update(ctx, route)
}

// PatchGRPCRouteStatus sets the Accepted condition for a specific parentRef on a GRPCRoute.
func PatchGRPCRouteStatus(ctx context.Context, c client.Client, route *gwapiv1.GRPCRoute, gwName, gwNS string, accepted bool) error {
	status := metav1.ConditionTrue
	reason := string(gwapiv1.RouteReasonAccepted)
	message := "Route is accepted"
	if !accepted {
		status = metav1.ConditionFalse
		reason = string(gwapiv1.RouteReasonNotAllowedByListeners)
		message = "Route is not allowed by listeners"
	}

	gwGroup := gwapiv1.Group(gwapiv1.GroupName)
	gwKind := gwapiv1.Kind("Gateway")
	gwNamespace := gwapiv1.Namespace(gwNS)

	parentStatus := gwapiv1.RouteParentStatus{
		ParentRef: gwapiv1.ParentReference{
			Group:     &gwGroup,
			Kind:      &gwKind,
			Namespace: &gwNamespace,
			Name:      gwapiv1.ObjectName(gwName),
		},
		ControllerName: gwapiv1.GatewayController(ControllerName),
		Conditions: []metav1.Condition{{
			Type:               string(gwapiv1.RouteConditionAccepted),
			Status:             status,
			ObservedGeneration: route.Generation,
			LastTransitionTime: metav1.Now(),
			Reason:             reason,
			Message:            message,
		}},
	}

	route.Status.Parents = setParentStatus(route.Status.Parents, parentStatus, gwName, gwNS)
	return c.Status().Update(ctx, route)
}

// supportedKindsForProtocol returns the route kinds supported by a listener protocol.
func supportedKindsForProtocol(protocol gwapiv1.ProtocolType) []gwapiv1.RouteGroupKind {
	group := gwapiv1.Group(gwapiv1.GroupName)
	switch protocol {
	case gwapiv1.HTTPProtocolType:
		return []gwapiv1.RouteGroupKind{
			{Group: &group, Kind: "HTTPRoute"},
			{Group: &group, Kind: "GRPCRoute"},
		}
	case gwapiv1.HTTPSProtocolType:
		return []gwapiv1.RouteGroupKind{
			{Group: &group, Kind: "HTTPRoute"},
			{Group: &group, Kind: "GRPCRoute"},
			{Group: &group, Kind: "TLSRoute"},
		}
	case gwapiv1.TLSProtocolType:
		return []gwapiv1.RouteGroupKind{{Group: &group, Kind: "TLSRoute"}}
	case gwapiv1.TCPProtocolType:
		return []gwapiv1.RouteGroupKind{{Group: &group, Kind: "TCPRoute"}}
	default:
		return nil
	}
}

// transitionTime returns the appropriate LastTransitionTime.
// Reuses existing timestamp when the condition type+status hasn't changed to prevent thrashing.
func transitionTime(existing []metav1.Condition, condType string, newStatus metav1.ConditionStatus) metav1.Time {
	for _, c := range existing {
		if c.Type == condType && c.Status == newStatus {
			return c.LastTransitionTime
		}
	}
	return metav1.Now()
}

// setCondition upserts a condition by type.
func setCondition(conditions []metav1.Condition, condition metav1.Condition) []metav1.Condition {
	for i, c := range conditions {
		if c.Type == condition.Type {
			conditions[i] = condition
			return conditions
		}
	}
	return append(conditions, condition)
}

// setParentStatus upserts a RouteParentStatus by gateway name/namespace.
func setParentStatus(statuses []gwapiv1.RouteParentStatus, status gwapiv1.RouteParentStatus, gwName, gwNS string) []gwapiv1.RouteParentStatus {
	for i, s := range statuses {
		if string(s.ParentRef.Name) == gwName &&
			s.ParentRef.Namespace != nil && string(*s.ParentRef.Namespace) == gwNS &&
			s.ControllerName == status.ControllerName {
			statuses[i] = status
			return statuses
		}
	}
	return append(statuses, status)
}
