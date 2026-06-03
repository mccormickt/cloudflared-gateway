package controller

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

// validPolicyTargets returns the set of target keys (kind/name) a policy may
// validly attach to for this Gateway: the Gateway itself and every attached route.
func validPolicyTargets(gw *gwapiv1.Gateway, http []gwapiv1.HTTPRoute, grpc []gwapiv1.GRPCRoute, tls []gwapiv1alpha2.TLSRoute, tcp []gwapiv1alpha2.TCPRoute) map[string]bool {
	valid := map[string]bool{targetKey("Gateway", gw.Name): true}
	for i := range http {
		valid[targetKey("HTTPRoute", http[i].Name)] = true
	}
	for i := range grpc {
		valid[targetKey("GRPCRoute", grpc[i].Name)] = true
	}
	for i := range tls {
		valid[targetKey("TLSRoute", tls[i].Name)] = true
	}
	for i := range tcp {
		valid[targetKey("TCPRoute", tcp[i].Name)] = true
	}
	return valid
}

// maxPolicyAncestors caps the ancestors list per GEP-713.
const maxPolicyAncestors = 16

// PolicyAffected condition types set on objects (Gateways, routes) that a policy
// is acting on, for discoverability per GEP-713. The convention is one condition
// type per metaresource kind, named "<Kind>Affected" and namespaced by the API
// group, so observers can tell which policy kind affects the object.
const (
	accessPolicyAffectedConditionType = "cloudflare.jan0ski.net/CloudflareAccessPolicyAffected"
	originPolicyAffectedConditionType = "cloudflare.jan0ski.net/CloudflareOriginPolicyAffected"
)

// setGatewayPolicyAffected sets the per-kind PolicyAffected conditions on a
// Gateway, reflecting whether an accepted policy of each kind currently targets
// it. Both are set unconditionally each reconcile (True or False) so a condition
// is cleared when the last targeting policy is removed; setCondition never
// deletes, so a True-only update would leave it permanently stale. The caller
// persists them via the subsequent Gateway status update.
func setGatewayPolicyAffected(gw *gwapiv1.Gateway, accessAffected, originAffected bool) {
	setGatewayPolicyAffectedCondition(gw, accessPolicyAffectedConditionType, "CloudflareAccessPolicy", accessAffected)
	setGatewayPolicyAffectedCondition(gw, originPolicyAffectedConditionType, "CloudflareOriginPolicy", originAffected)
}

func setGatewayPolicyAffectedCondition(gw *gwapiv1.Gateway, condType, kind string, affected bool) {
	status := metav1.ConditionTrue
	reason := "PolicyAffected"
	message := fmt.Sprintf("Object is affected by a %s", kind)
	if !affected {
		status = metav1.ConditionFalse
		reason = "NoPolicyAttached"
		message = fmt.Sprintf("Object is not affected by any %s", kind)
	}
	gw.Status.Conditions = setCondition(gw.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		ObservedGeneration: gw.Generation,
		LastTransitionTime: transitionTime(gw.Status.Conditions, condType, status),
		Reason:             reason,
		Message:            message,
	})
}

// policyAffectedRouteCondition builds a per-kind PolicyAffected condition for a
// route's parent status, preserving the transition time when already set.
func policyAffectedRouteCondition(condType, kind string, generation int64, parents []gwapiv1.RouteParentStatus, gwName, gwNS string) metav1.Condition {
	return metav1.Condition{
		Type:               condType,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: generation,
		LastTransitionTime: routeCondTransitionTime(parents, gwName, gwNS, condType, metav1.ConditionTrue),
		Reason:             "PolicyAffected",
		Message:            fmt.Sprintf("Object is affected by a %s", kind),
	}
}

// overriddenCondition builds the GEP-713 Overridden condition for an inherited
// policy whose defaults are superseded by a more-specific policy on some routes.
func overriddenCondition(generation int64, overridden bool) metav1.Condition {
	status := metav1.ConditionFalse
	reason := "Accepted"
	message := "Policy is not overridden"
	if overridden {
		status = metav1.ConditionTrue
		reason = "Overridden"
		message = "Gateway-level defaults are overridden by a route-level policy for some routes"
	}
	return metav1.Condition{
		Type:               "Overridden",
		Status:             status,
		ObservedGeneration: generation,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}
}

// gatewayAncestorRef builds the PolicyAncestorStatus ancestorRef for a Gateway.
func gatewayAncestorRef(gw *gwapiv1.Gateway) gwapiv1.ParentReference {
	group := gwapiv1.Group(gwapiv1.GroupName)
	kind := gwapiv1.Kind("Gateway")
	ns := gwapiv1.Namespace(gw.Namespace)
	return gwapiv1.ParentReference{
		Group:     &group,
		Kind:      &kind,
		Namespace: &ns,
		Name:      gwapiv1.ObjectName(gw.Name),
	}
}

func ancestorRefEqual(a, b gwapiv1.ParentReference) bool {
	return a.Name == b.Name &&
		derefNamespace(a.Namespace) == derefNamespace(b.Namespace)
}

func derefNamespace(ns *gwapiv1.Namespace) string {
	if ns == nil {
		return ""
	}
	return string(*ns)
}

// acceptedCondition builds the standard Accepted condition for a policy.
func acceptedCondition(generation int64, accepted bool, reason, message string) metav1.Condition {
	status := metav1.ConditionTrue
	if !accepted {
		status = metav1.ConditionFalse
	}
	return metav1.Condition{
		Type:               "Accepted",
		Status:             status,
		ObservedGeneration: generation,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}
}

// upsertAncestorCondition sets a condition on the PolicyAncestorStatus entry for
// the given ancestor/controller, creating the entry if needed.
func upsertAncestorCondition(status *gwapiv1.PolicyStatus, ancestor gwapiv1.ParentReference, controllerName gwapiv1.GatewayController, cond metav1.Condition) {
	for i := range status.Ancestors {
		a := &status.Ancestors[i]
		if ancestorRefEqual(a.AncestorRef, ancestor) && a.ControllerName == controllerName {
			// Preserve the transition time when the status is unchanged.
			cond.LastTransitionTime = transitionTime(a.Conditions, cond.Type, cond.Status)
			a.Conditions = setCondition(a.Conditions, cond)
			return
		}
	}
	if len(status.Ancestors) >= maxPolicyAncestors {
		return
	}
	status.Ancestors = append(status.Ancestors, gwapiv1.PolicyAncestorStatus{
		AncestorRef:    ancestor,
		ControllerName: controllerName,
		Conditions:     setCondition(nil, cond),
	})
}

// removeAncestor deletes the PolicyAncestorStatus entry for the given ancestor
// and controller, returning true if an entry was removed. Used to prune stale
// ancestor status (GEP-713) when a policy no longer applies to a Gateway —
// because it was retargeted, its attached route was detached, or the Gateway
// was deleted.
func removeAncestor(status *gwapiv1.PolicyStatus, ancestor gwapiv1.ParentReference, controllerName gwapiv1.GatewayController) bool {
	for i := range status.Ancestors {
		a := &status.Ancestors[i]
		if ancestorRefEqual(a.AncestorRef, ancestor) && a.ControllerName == controllerName {
			status.Ancestors = append(status.Ancestors[:i], status.Ancestors[i+1:]...)
			return true
		}
	}
	return false
}

// targetKey is the dedup key for a policy targetRef (kind/name within the group).
func targetKey(kind, name string) string {
	return kind + "/" + name
}

// addAffectedTargets records the valid targetRefs of an accepted policy.
func addAffectedTargets(affected map[string]bool, refs []gwapiv1.LocalPolicyTargetReference, valid map[string]bool) {
	for _, ref := range refs {
		key := targetKey(string(ref.Kind), string(ref.Name))
		if valid[key] {
			affected[key] = true
		}
	}
}

// routeKinds are the route target kinds an inherited policy can be overridden at.
var routeKinds = []string{"HTTPRoute", "GRPCRoute", "TLSRoute", "TCPRoute"}

// targetsAnyRoute reports whether any targetRef points at a route kind.
func targetsAnyRoute(refs []gwapiv1.LocalPolicyTargetReference) bool {
	for _, ref := range refs {
		for _, k := range routeKinds {
			if string(ref.Kind) == k {
				return true
			}
		}
	}
	return false
}

// policyTarget pairs a policy's identity with its targetRefs for acceptance evaluation.
type policyTarget struct {
	obj  metav1.Object
	refs []gwapiv1.LocalPolicyTargetReference
}

// evaluatePolicyAcceptance applies the GEP-713 acceptance/conflict rule for one
// policy against its peers, restricted to the targets valid for the current
// Gateway. Returns (accepted, reason, message). A reason of "TargetNotFound"
// means the policy does not apply to this Gateway and its status should not be
// touched during this reconcile.
func evaluatePolicyAcceptance(self policyTarget, all []policyTarget, valid map[string]bool) (bool, string, string) {
	matchedValid := false
	for _, ref := range self.refs {
		key := targetKey(string(ref.Kind), string(ref.Name))
		if !valid[key] {
			continue
		}
		matchedValid = true
		for _, other := range all {
			if other.obj.GetName() == self.obj.GetName() && other.obj.GetNamespace() == self.obj.GetNamespace() {
				continue
			}
			if targetsResource(other.refs, gwapiv1.GroupName, string(ref.Kind), string(ref.Name)) && policyOlderThan(other.obj, self.obj) {
				return false, "Conflicted", "conflicted by an older policy targeting " + key
			}
		}
	}
	if !matchedValid {
		return false, "TargetNotFound", "no targetRef matches the Gateway or an attached route"
	}
	return true, "Accepted", "Policy is accepted"
}

// policyOlderThan reports whether policy a should win over b under the GEP-713
// conflict rule: the oldest creationTimestamp wins, ties broken by namespaced
// name (namespace, then name).
func policyOlderThan(a, b metav1.Object) bool {
	at := a.GetCreationTimestamp()
	bt := b.GetCreationTimestamp()
	if !at.Equal(&bt) {
		return at.Before(&bt)
	}
	if a.GetNamespace() != b.GetNamespace() {
		return a.GetNamespace() < b.GetNamespace()
	}
	return a.GetName() < b.GetName()
}
