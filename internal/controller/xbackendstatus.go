package controller

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	apisxv1alpha1 "sigs.k8s.io/gateway-api/apisx/v1alpha1"
)

// maxXBackendAncestors caps the ancestors list reported on an XBackend
// (the CRD allows 32; we stay conservative, matching the policy helpers).
const maxXBackendAncestors = 16

// patchXBackendStatuses reconciles the GEP-713 ancestor status on XBackends for
// this Gateway. XBackends this Gateway serves (referenced by an attached route,
// permitted, and present) get an Accepted ancestor condition; every other
// XBackend has this Gateway's ancestor entry pruned. No-op when experimental
// support is disabled (col == nil). Best-effort: returns the first error.
func (r *GatewayReconciler) patchXBackendStatuses(ctx context.Context, gw *gwapiv1.Gateway, col *xbBackends) error {
	if col == nil {
		return nil
	}
	ancestor := gatewayAncestorRef(gw)
	cn := r.ControllerName

	var list apisxv1alpha1.XBackendList
	if err := r.Client.List(ctx, &list); err != nil {
		return err
	}

	var firstErr error
	for i := range list.Items {
		xb := &list.Items[i]
		managed := col.fetched[xbKey{xb.Namespace, xb.Name}] != nil

		var changed bool
		if managed {
			_, reason := translateXBackend(xb)
			upsertXBackendAncestor(&xb.Status, ancestor, cn, xbAcceptedCondition(xb.Generation, reason))
			changed = true
		} else {
			changed = removeXBackendAncestor(&xb.Status, ancestor, cn)
		}

		if changed {
			if err := r.Client.Status().Update(ctx, xb); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// xbAcceptedCondition builds the XBackend Accepted condition from a resolution
// reason ("" means accepted).
func xbAcceptedCondition(generation int64, reason string) metav1.Condition {
	if reason == reasonResolvedOK {
		return metav1.Condition{
			Type:               "Accepted",
			Status:             metav1.ConditionTrue,
			ObservedGeneration: generation,
			LastTransitionTime: metav1.Now(),
			Reason:             "Accepted",
			Message:            "Backend is accepted",
		}
	}
	return metav1.Condition{
		Type:               "Accepted",
		Status:             metav1.ConditionFalse,
		ObservedGeneration: generation,
		LastTransitionTime: metav1.Now(),
		Reason:             "UnsupportedProtocol",
		Message:            "Backend uses a protocol or TLS mode Cloudflare tunnels cannot serve",
	}
}

// upsertXBackendAncestor sets a condition on the BackendAncestorStatus entry for
// the given ancestor/controller, creating the entry if needed. Mirrors
// upsertAncestorCondition for the apisx BackendStatus type.
func upsertXBackendAncestor(status *apisxv1alpha1.BackendStatus, ancestor gwapiv1.ParentReference, cn gwapiv1.GatewayController, cond metav1.Condition) {
	for i := range status.Ancestors {
		a := &status.Ancestors[i]
		if ancestorRefEqual(a.AncestorRef, ancestor) && a.ControllerName == cn {
			cond.LastTransitionTime = transitionTime(a.Conditions, cond.Type, cond.Status)
			a.Conditions = setCondition(a.Conditions, cond)
			return
		}
	}
	if len(status.Ancestors) >= maxXBackendAncestors {
		return
	}
	status.Ancestors = append(status.Ancestors, apisxv1alpha1.BackendAncestorStatus{
		AncestorRef:    ancestor,
		ControllerName: cn,
		Conditions:     setCondition(nil, cond),
	})
}

// removeXBackendAncestor deletes this controller's ancestor entry for the given
// Gateway, returning true if an entry was removed.
func removeXBackendAncestor(status *apisxv1alpha1.BackendStatus, ancestor gwapiv1.ParentReference, cn gwapiv1.GatewayController) bool {
	for i := range status.Ancestors {
		a := &status.Ancestors[i]
		if ancestorRefEqual(a.AncestorRef, ancestor) && a.ControllerName == cn {
			status.Ancestors = append(status.Ancestors[:i], status.Ancestors[i+1:]...)
			return true
		}
	}
	return false
}
