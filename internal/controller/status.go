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
func PatchGatewayClassStatus(ctx context.Context, c client.Client, gc *gwapiv1.GatewayClass, accepted bool, reason, message string) error {
	status := metav1.ConditionTrue
	if !accepted {
		status = metav1.ConditionFalse
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

	// Build listener statuses. Capture the previous listener slice first so
	// listenerTransitionTime can reuse existing timestamps — overwriting
	// gw.Status.Listeners before reading it would discard every prior timestamp
	// and thrash LastTransitionTime on every reconcile.
	prevListeners := gw.Status.Listeners
	gw.Status.Listeners = make([]gwapiv1.ListenerStatus, 0, len(listenerCounts))
	for _, lc := range listenerCounts {
		supportedKinds := supportedKindsForProtocol(lc.Protocol)

		acceptedStatus := metav1.ConditionTrue
		acceptedReason, acceptedMsg := string(gwapiv1.ListenerReasonAccepted), "Listener is accepted"
		programmedStatus := metav1.ConditionTrue
		programmedReason, programmedMsg := string(gwapiv1.ListenerReasonProgrammed), "Listener is programmed"
		resolvedStatus := metav1.ConditionTrue
		resolvedReason, resolvedMsg := string(gwapiv1.ListenerReasonResolvedRefs), "All references resolved"

		// A protocol with no supported route kinds is not one this controller
		// can serve — report it rejected instead of silently accepting it.
		if len(supportedKinds) == 0 {
			acceptedStatus = metav1.ConditionFalse
			acceptedReason, acceptedMsg = string(gwapiv1.ListenerReasonUnsupportedProtocol), fmt.Sprintf("Protocol %q is not supported", lc.Protocol)
			programmedStatus = metav1.ConditionFalse
			programmedReason, programmedMsg = string(gwapiv1.ListenerReasonInvalid), "Listener is not programmed: unsupported protocol"
			resolvedStatus = metav1.ConditionFalse
			resolvedReason, resolvedMsg = string(gwapiv1.ListenerReasonInvalidRouteKinds), "No supported route kinds for protocol"
		}

		ls := gwapiv1.ListenerStatus{
			Name:           lc.Name,
			AttachedRoutes: lc.Count,
			SupportedKinds: supportedKinds,
			Conditions: []metav1.Condition{
				{
					Type:               string(gwapiv1.ListenerConditionAccepted),
					Status:             acceptedStatus,
					ObservedGeneration: gw.Generation,
					LastTransitionTime: listenerTransitionTime(prevListeners, lc.Name, string(gwapiv1.ListenerConditionAccepted), acceptedStatus),
					Reason:             acceptedReason,
					Message:            acceptedMsg,
				},
				{
					Type:               string(gwapiv1.ListenerConditionProgrammed),
					Status:             programmedStatus,
					ObservedGeneration: gw.Generation,
					LastTransitionTime: listenerTransitionTime(prevListeners, lc.Name, string(gwapiv1.ListenerConditionProgrammed), programmedStatus),
					Reason:             programmedReason,
					Message:            programmedMsg,
				},
				{
					Type:               string(gwapiv1.ListenerConditionResolvedRefs),
					Status:             resolvedStatus,
					ObservedGeneration: gw.Generation,
					LastTransitionTime: listenerTransitionTime(prevListeners, lc.Name, string(gwapiv1.ListenerConditionResolvedRefs), resolvedStatus),
					Reason:             resolvedReason,
					Message:            resolvedMsg,
				},
			},
		}
		gw.Status.Listeners = append(gw.Status.Listeners, ls)
	}

	return c.Status().Update(ctx, gw)
}

// ResolvedRefsResult is a route's computed ResolvedRefs outcome: whether all of
// its backendRefs resolved, plus the Gateway API reason/message to report.
type ResolvedRefsResult struct {
	OK      bool
	Reason  string
	Message string
}

// resolvedRefsResultFor maps an internal XBackend resolution reason (see
// xbackend.go) to a route ResolvedRefs result.
func resolvedRefsResultFor(reason string) ResolvedRefsResult {
	switch reason {
	case reasonResolvedOK:
		return ResolvedRefsResult{OK: true, Reason: string(gwapiv1.RouteReasonResolvedRefs), Message: "All references resolved"}
	case reasonInvalidKind:
		return ResolvedRefsResult{Reason: string(gwapiv1.RouteReasonInvalidKind), Message: "Backend refers to an XBackend but experimental backend support is disabled"}
	case reasonBackendNotFound:
		return ResolvedRefsResult{Reason: string(gwapiv1.RouteReasonBackendNotFound), Message: "Referenced XBackend does not exist"}
	case reasonRefNotPermitted:
		return ResolvedRefsResult{Reason: string(gwapiv1.RouteReasonRefNotPermitted), Message: "Cross-namespace XBackend reference is not permitted by any ReferenceGrant"}
	case reasonUnsupportedProtocol:
		return ResolvedRefsResult{Reason: "UnsupportedProtocol", Message: "Referenced XBackend uses a protocol or TLS mode Cloudflare tunnels cannot serve"}
	default:
		return ResolvedRefsResult{Reason: "InvalidBackend", Message: "Referenced XBackend could not be resolved"}
	}
}

// resolvedRefsRouteCondition builds the ResolvedRefs condition for a route's
// parent status, preserving the transition time when the status is unchanged.
func resolvedRefsRouteCondition(res ResolvedRefsResult, generation int64, existing []gwapiv1.RouteParentStatus, gwName, gwNS string) metav1.Condition {
	status := metav1.ConditionTrue
	if !res.OK {
		status = metav1.ConditionFalse
	}
	return metav1.Condition{
		Type:               string(gwapiv1.RouteConditionResolvedRefs),
		Status:             status,
		ObservedGeneration: generation,
		LastTransitionTime: routeCondTransitionTime(existing, gwName, gwNS, string(gwapiv1.RouteConditionResolvedRefs), status),
		Reason:             res.Reason,
		Message:            res.Message,
	}
}

// partiallyInvalidRouteCondition reports that a route's match used a dimension
// Cloudflare tunnels can't enforce (method, header, or query-param match), so
// routing falls back to hostname+path. Non-fatal: the route stays Accepted.
func partiallyInvalidRouteCondition(generation int64, existing []gwapiv1.RouteParentStatus, gwName, gwNS string) metav1.Condition {
	return metav1.Condition{
		Type:               string(gwapiv1.RouteConditionPartiallyInvalid),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: generation,
		LastTransitionTime: routeCondTransitionTime(existing, gwName, gwNS, string(gwapiv1.RouteConditionPartiallyInvalid), metav1.ConditionTrue),
		Reason:             string(gwapiv1.RouteReasonUnsupportedValue),
		Message:            "Some matches use method, header, or query-param dimensions Cloudflare tunnels cannot enforce; routing falls back to hostname and path",
	}
}

// PatchHTTPRouteStatus sets the Accepted condition for a specific parentRef on an HTTPRoute.
func PatchHTTPRouteStatus(ctx context.Context, c client.Client, route *gwapiv1.HTTPRoute, gwName, gwNS string, accepted, accessAffected, originAffected, partiallyInvalid bool, resolvedRefs ResolvedRefsResult) error {
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
			LastTransitionTime: routeTransitionTime(route.Status.Parents, gwName, gwNS, status),
			Reason:             reason,
			Message:            message,
		}},
	}

	if accessAffected {
		parentStatus.Conditions = append(parentStatus.Conditions, policyAffectedRouteCondition(accessPolicyAffectedConditionType, "CloudflareAccessPolicy", route.Generation, route.Status.Parents, gwName, gwNS))
	}
	if originAffected {
		parentStatus.Conditions = append(parentStatus.Conditions, policyAffectedRouteCondition(originPolicyAffectedConditionType, "CloudflareOriginPolicy", route.Generation, route.Status.Parents, gwName, gwNS))
	}
	if partiallyInvalid {
		parentStatus.Conditions = append(parentStatus.Conditions, partiallyInvalidRouteCondition(route.Generation, route.Status.Parents, gwName, gwNS))
	}
	parentStatus.Conditions = append(parentStatus.Conditions, resolvedRefsRouteCondition(resolvedRefs, route.Generation, route.Status.Parents, gwName, gwNS))
	route.Status.Parents = setParentStatus(route.Status.Parents, parentStatus, gwName, gwNS)
	return c.Status().Update(ctx, route)
}

// PatchTLSRouteStatus sets the Accepted condition for a specific parentRef on a TLSRoute.
func PatchTLSRouteStatus(ctx context.Context, c client.Client, route *gwapiv1alpha2.TLSRoute, gwName, gwNS string, accepted, accessAffected, originAffected bool, resolvedRefs ResolvedRefsResult) error {
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
			LastTransitionTime: routeTransitionTime(route.Status.Parents, gwName, gwNS, status),
			Reason:             reason,
			Message:            message,
		}},
	}

	if accessAffected {
		parentStatus.Conditions = append(parentStatus.Conditions, policyAffectedRouteCondition(accessPolicyAffectedConditionType, "CloudflareAccessPolicy", route.Generation, route.Status.Parents, gwName, gwNS))
	}
	if originAffected {
		parentStatus.Conditions = append(parentStatus.Conditions, policyAffectedRouteCondition(originPolicyAffectedConditionType, "CloudflareOriginPolicy", route.Generation, route.Status.Parents, gwName, gwNS))
	}
	parentStatus.Conditions = append(parentStatus.Conditions, resolvedRefsRouteCondition(resolvedRefs, route.Generation, route.Status.Parents, gwName, gwNS))
	route.Status.Parents = setParentStatus(route.Status.Parents, parentStatus, gwName, gwNS)
	return c.Status().Update(ctx, route)
}

// PatchTCPRouteStatus sets the Accepted condition for a specific parentRef on a TCPRoute.
func PatchTCPRouteStatus(ctx context.Context, c client.Client, route *gwapiv1alpha2.TCPRoute, gwName, gwNS string, accepted, accessAffected, originAffected bool, resolvedRefs ResolvedRefsResult) error {
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
			LastTransitionTime: routeTransitionTime(route.Status.Parents, gwName, gwNS, status),
			Reason:             reason,
			Message:            message,
		}},
	}

	if accessAffected {
		parentStatus.Conditions = append(parentStatus.Conditions, policyAffectedRouteCondition(accessPolicyAffectedConditionType, "CloudflareAccessPolicy", route.Generation, route.Status.Parents, gwName, gwNS))
	}
	if originAffected {
		parentStatus.Conditions = append(parentStatus.Conditions, policyAffectedRouteCondition(originPolicyAffectedConditionType, "CloudflareOriginPolicy", route.Generation, route.Status.Parents, gwName, gwNS))
	}
	parentStatus.Conditions = append(parentStatus.Conditions, resolvedRefsRouteCondition(resolvedRefs, route.Generation, route.Status.Parents, gwName, gwNS))
	route.Status.Parents = setParentStatus(route.Status.Parents, parentStatus, gwName, gwNS)
	return c.Status().Update(ctx, route)
}

// PatchGRPCRouteStatus sets the Accepted condition for a specific parentRef on a GRPCRoute.
func PatchGRPCRouteStatus(ctx context.Context, c client.Client, route *gwapiv1.GRPCRoute, gwName, gwNS string, accepted, accessAffected, originAffected, partiallyInvalid bool, resolvedRefs ResolvedRefsResult) error {
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
			LastTransitionTime: routeTransitionTime(route.Status.Parents, gwName, gwNS, status),
			Reason:             reason,
			Message:            message,
		}},
	}

	if accessAffected {
		parentStatus.Conditions = append(parentStatus.Conditions, policyAffectedRouteCondition(accessPolicyAffectedConditionType, "CloudflareAccessPolicy", route.Generation, route.Status.Parents, gwName, gwNS))
	}
	if originAffected {
		parentStatus.Conditions = append(parentStatus.Conditions, policyAffectedRouteCondition(originPolicyAffectedConditionType, "CloudflareOriginPolicy", route.Generation, route.Status.Parents, gwName, gwNS))
	}
	if partiallyInvalid {
		parentStatus.Conditions = append(parentStatus.Conditions, partiallyInvalidRouteCondition(route.Generation, route.Status.Parents, gwName, gwNS))
	}
	parentStatus.Conditions = append(parentStatus.Conditions, resolvedRefsRouteCondition(resolvedRefs, route.Generation, route.Status.Parents, gwName, gwNS))
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

// listenerTransitionTime returns the existing lastTransitionTime for a listener
// condition if the type+status match, or metav1.Now() otherwise.
func listenerTransitionTime(listeners []gwapiv1.ListenerStatus, name gwapiv1.SectionName, condType string, newStatus metav1.ConditionStatus) metav1.Time {
	for _, l := range listeners {
		if l.Name != name {
			continue
		}
		for _, c := range l.Conditions {
			if c.Type == condType && c.Status == newStatus {
				return c.LastTransitionTime
			}
		}
	}
	return metav1.Now()
}

// routeTransitionTime returns the existing lastTransitionTime for a route's
// Accepted parent condition if the status matches, or metav1.Now() otherwise.
func routeTransitionTime(parents []gwapiv1.RouteParentStatus, gwName, gwNS string, newStatus metav1.ConditionStatus) metav1.Time {
	for _, p := range parents {
		if string(p.ParentRef.Name) != gwName || p.ControllerName != gwapiv1.GatewayController(ControllerName) {
			continue
		}
		if p.ParentRef.Namespace != nil && string(*p.ParentRef.Namespace) != gwNS {
			continue
		}
		for _, c := range p.Conditions {
			if c.Type == string(gwapiv1.RouteConditionAccepted) && c.Status == newStatus {
				return c.LastTransitionTime
			}
		}
	}
	return metav1.Now()
}

// routeCondTransitionTime returns the existing lastTransitionTime for a route's
// parent condition of the given type if the status matches, or metav1.Now().
func routeCondTransitionTime(parents []gwapiv1.RouteParentStatus, gwName, gwNS, condType string, newStatus metav1.ConditionStatus) metav1.Time {
	for _, p := range parents {
		if string(p.ParentRef.Name) != gwName || p.ControllerName != gwapiv1.GatewayController(ControllerName) {
			continue
		}
		if p.ParentRef.Namespace != nil && string(*p.ParentRef.Namespace) != gwNS {
			continue
		}
		for _, c := range p.Conditions {
			if c.Type == condType && c.Status == newStatus {
				return c.LastTransitionTime
			}
		}
	}
	return metav1.Now()
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
