package controller

import (
	"context"
	"fmt"

	cfv1alpha1 "github.com/mccormickt/cloudflared-gateway/api/v1alpha1"
	cfclient "github.com/mccormickt/cloudflared-gateway/internal/cloudflare"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// applyAccessPolicies looks up CloudflareAccessPolicy resources that target
// the given Gateway or its routes (via GEP-713 Policy Attachment targetRefs) and
// sets originRequest.access on matching ingress rules by route identity. A
// Gateway-level policy wins over route-level and applies to every rule (covering
// all HTTP-family route kinds, including gRPC); otherwise each rule is matched to
// a policy targeting its own route.
func (r *GatewayReconciler) applyAccessPolicies(ctx context.Context, rules []cfclient.BuiltRule, gw *gwapiv1.Gateway) ([]cfclient.BuiltRule, error) {
	var policyList cfv1alpha1.CloudflareAccessPolicyList
	if err := r.Client.List(ctx, &policyList, client.InNamespace(gw.Namespace)); err != nil {
		return nil, fmt.Errorf("listing CloudflareAccessPolicy resources: %w", err)
	}
	if len(policyList.Items) == 0 {
		return rules, nil
	}

	gatewayAccessCfg := findAccessConfigForTarget(policyList.Items, gwapiv1.GroupName, "Gateway", gw.Name)

	for i := range rules {
		cfg := gatewayAccessCfg
		if cfg == nil {
			cfg = findAccessConfigForTarget(policyList.Items, gwapiv1.GroupName, rules[i].RouteKind, rules[i].RouteName)
		}
		if cfg == nil {
			continue
		}
		if rules[i].OriginRequest == nil {
			rules[i].OriginRequest = &cfclient.OriginRequest{}
		}
		rules[i].OriginRequest.Access = cfg
	}

	return rules, nil
}

// patchAccessPolicyStatuses sets GEP-713 ancestor status on CloudflareAccessPolicy
// resources that target this Gateway or one of its attached routes. Policies that
// do not apply to this Gateway are left untouched. Returns the set of target keys
// (kind/name) that an accepted policy directly attaches to, for PolicyAffected.
func (r *GatewayReconciler) patchAccessPolicyStatuses(ctx context.Context, gw *gwapiv1.Gateway, valid map[string]bool) (map[string]bool, error) {
	logger := log.FromContext(ctx)
	var policyList cfv1alpha1.CloudflareAccessPolicyList
	if err := r.Client.List(ctx, &policyList, client.InNamespace(gw.Namespace)); err != nil {
		return nil, fmt.Errorf("listing CloudflareAccessPolicy resources: %w", err)
	}

	all := make([]policyTarget, len(policyList.Items))
	for i := range policyList.Items {
		all[i] = policyTarget{obj: &policyList.Items[i], refs: policyList.Items[i].Spec.TargetRefs}
	}
	ancestor := gatewayAncestorRef(gw)
	affected := map[string]bool{}
	for i := range policyList.Items {
		accepted, reason, msg := evaluatePolicyAcceptance(all[i], all, valid)
		if reason == "TargetNotFound" {
			// Policy does not apply to this Gateway. Prune any ancestor entry we
			// wrote on a previous reconcile (it was retargeted or its route detached).
			if removeAncestor(&policyList.Items[i].Status, ancestor, r.ControllerName) {
				if err := r.Client.Status().Update(ctx, &policyList.Items[i]); err != nil {
					logger.Error(err, "Failed to prune stale CloudflareAccessPolicy ancestor status", "policy", policyList.Items[i].Name)
				}
			}
			continue
		}
		upsertAncestorCondition(&policyList.Items[i].Status, ancestor, r.ControllerName,
			acceptedCondition(policyList.Items[i].Generation, accepted, reason, msg))
		if err := r.Client.Status().Update(ctx, &policyList.Items[i]); err != nil {
			logger.Error(err, "Failed to patch CloudflareAccessPolicy status", "policy", policyList.Items[i].Name)
		}
		if accepted {
			addAffectedTargets(affected, all[i].refs, valid)
		}
	}
	return affected, nil
}

// prunePolicyAncestorStatus removes this Gateway's ancestor entry from every
// CloudflareAccessPolicy and CloudflareOriginPolicy in the Gateway's namespace.
// Called on Gateway deletion so a removed Gateway does not leak stale GEP-713
// ancestor status (the per-reconcile prune cannot run once the Gateway is gone).
// Best-effort: errors are logged, not returned, so they never block finalizer
// removal.
func (r *GatewayReconciler) prunePolicyAncestorStatus(ctx context.Context, gw *gwapiv1.Gateway) {
	logger := log.FromContext(ctx)
	ancestor := gatewayAncestorRef(gw)

	var accessList cfv1alpha1.CloudflareAccessPolicyList
	if err := r.Client.List(ctx, &accessList, client.InNamespace(gw.Namespace)); err != nil {
		logger.Error(err, "Cleanup: failed to list CloudflareAccessPolicy for status pruning")
	} else {
		for i := range accessList.Items {
			if removeAncestor(&accessList.Items[i].Status, ancestor, r.ControllerName) {
				if err := r.Client.Status().Update(ctx, &accessList.Items[i]); err != nil {
					logger.Error(err, "Cleanup: failed to prune CloudflareAccessPolicy ancestor status", "policy", accessList.Items[i].Name)
				}
			}
		}
	}

	originPolicies, err := r.listOriginPolicies(ctx, gw.Namespace)
	if err != nil {
		logger.Error(err, "Cleanup: failed to list CloudflareOriginPolicy for status pruning")
		return
	}
	for i := range originPolicies {
		if removeAncestor(&originPolicies[i].Status, ancestor, r.ControllerName) {
			if err := r.Client.Status().Update(ctx, &originPolicies[i]); err != nil {
				logger.Error(err, "Cleanup: failed to prune CloudflareOriginPolicy ancestor status", "policy", originPolicies[i].Name)
			}
		}
	}
}

// targetsResource checks if any targetRef matches the given group/kind/name.
func targetsResource(refs []gwapiv1.LocalPolicyTargetReference, group, kind, name string) bool {
	for _, ref := range refs {
		if string(ref.Group) == group && string(ref.Kind) == kind && string(ref.Name) == name {
			return true
		}
	}
	return false
}

// findAccessConfigForTarget searches policies for one targeting the given resource.
// Uses the None merge strategy (GEP-713 Direct policy): the oldest policy wins
// (by creationTimestamp, then namespaced name). This must match the conflict
// resolution in evaluatePolicyAcceptance/policyOlderThan so the config actually
// pushed to Cloudflare is the same policy reported Accepted in status — client
// List order is not creationTimestamp-ordered and cannot be relied on here.
func findAccessConfigForTarget(policies []cfv1alpha1.CloudflareAccessPolicy, group, kind, name string) *cfclient.AccessConfig {
	var winner *cfv1alpha1.CloudflareAccessPolicy
	for i := range policies {
		if !targetsResource(policies[i].Spec.TargetRefs, group, kind, name) {
			continue
		}
		if winner == nil || policyOlderThan(&policies[i], winner) {
			winner = &policies[i]
		}
	}
	if winner == nil {
		return nil
	}
	return &cfclient.AccessConfig{
		Required: winner.Spec.Required,
		TeamName: winner.Spec.TeamName,
		AudTag:   winner.Spec.AudTag,
	}
}
