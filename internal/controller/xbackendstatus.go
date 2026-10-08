package controller

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
			// "" judges the backend on its own: route-kind compatibility is a
			// property of each reference, not of the XBackend, and is reported on
			// the referencing route instead.
			_, reason := translateXBackend(xb, "")
			changed = upsertXBackendAncestor(&xb.Status, ancestor, cn, xbAcceptedCondition(xb.Generation, reason))
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

// pruneXBackendAncestorStatus removes this Gateway's ancestor entry from every
// XBackend, for use when the Gateway is being deleted and patchXBackendStatuses
// will never run again. Upstream requires a controller to drop the entry once
// the Backend is no longer associated with the parent; without this, a
// create/delete cycle would leak entries until maxXBackendAncestors is reached
// and no further Gateway could record status. Best-effort: returns the first
// error, and treats a missing XBackend CRD as nothing to prune.
func (r *GatewayReconciler) pruneXBackendAncestorStatus(ctx context.Context, gw *gwapiv1.Gateway) error {
	if !r.ExperimentalBackends {
		return nil
	}
	ancestor := gatewayAncestorRef(gw)
	cn := r.ControllerName

	var list apisxv1alpha1.XBackendList
	if err := r.Client.List(ctx, &list); err != nil {
		if apierrors.IsNotFound(err) || isNoMatchError(err) {
			return nil
		}
		return err
	}

	var firstErr error
	for i := range list.Items {
		xb := &list.Items[i]
		if !removeXBackendAncestor(&xb.Status, ancestor, cn) {
			continue
		}
		if err := r.Client.Status().Update(ctx, xb); err != nil && firstErr == nil {
			firstErr = err
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
	condReason, message := "UnsupportedProtocol", "Backend uses a protocol or TLS mode Cloudflare tunnels cannot serve"
	if reason == reasonUnsupportedCACerts {
		condReason = "UnsupportedCACerts"
		message = "Backend pins custom caCertificateRefs, which are not supported; use wellKnownCACertificates: System"
	}
	if reason == reasonUnsupportedTLSValidation {
		condReason = "UnsupportedTLSValidation"
		message = "Backend specifies subjectAltNames, which cannot be enforced; omit subjectAltNames to verify the validation.hostname against system CAs"
	}
	return metav1.Condition{
		Type:               "Accepted",
		Status:             metav1.ConditionFalse,
		ObservedGeneration: generation,
		LastTransitionTime: metav1.Now(),
		Reason:             condReason,
		Message:            message,
	}
}

// upsertXBackendAncestor sets a condition on the BackendAncestorStatus entry for
// the given ancestor/controller, creating the entry if needed. Mirrors
// upsertAncestorCondition for the apisx BackendStatus type. It reports whether
// the status actually changed, so callers skip a no-op API write on every
// reconcile (and when the ancestor cap blocks the insert).
func upsertXBackendAncestor(status *apisxv1alpha1.BackendStatus, ancestor gwapiv1.ParentReference, cn gwapiv1.GatewayController, cond metav1.Condition) bool {
	for i := range status.Ancestors {
		a := &status.Ancestors[i]
		if ancestorRefEqual(a.AncestorRef, ancestor) && a.ControllerName == cn {
			cond.LastTransitionTime = transitionTime(a.Conditions, cond.Type, cond.Status)
			for _, existing := range a.Conditions {
				if existing.Type == cond.Type && conditionEqual(existing, cond) {
					return false
				}
			}
			a.Conditions = setCondition(a.Conditions, cond)
			return true
		}
	}
	if len(status.Ancestors) >= maxXBackendAncestors {
		return false
	}
	status.Ancestors = append(status.Ancestors, apisxv1alpha1.BackendAncestorStatus{
		AncestorRef:    ancestor,
		ControllerName: cn,
		Conditions:     setCondition(nil, cond),
	})
	return true
}

// conditionEqual compares the meaningful fields of two conditions, ignoring
// LastTransitionTime (which the caller has already normalized).
func conditionEqual(a, b metav1.Condition) bool {
	return a.Status == b.Status &&
		a.Reason == b.Reason &&
		a.Message == b.Message &&
		a.ObservedGeneration == b.ObservedGeneration
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
