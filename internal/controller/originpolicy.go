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

// listOriginPolicies returns all CloudflareOriginPolicy resources in a namespace.
func (r *GatewayReconciler) listOriginPolicies(ctx context.Context, namespace string) ([]cfv1alpha1.CloudflareOriginPolicy, error) {
	var list cfv1alpha1.CloudflareOriginPolicyList
	if err := r.Client.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing CloudflareOriginPolicy resources: %w", err)
	}
	return list.Items, nil
}

// originRequestFromPolicy translates a CloudflareOriginPolicy spec into the
// controller's SDK-agnostic domain OriginRequest. This is the seam that keeps
// the CRD contract decoupled from any cloudflare-go version.
func originRequestFromPolicy(p *cfv1alpha1.CloudflareOriginPolicy) *cfclient.OriginRequest {
	s := &p.Spec
	o := &cfclient.OriginRequest{
		ProxyType:              s.ProxyType,
		DisableChunkedEncoding: s.DisableChunkedEncoding,
		NoHappyEyeballs:        s.NoHappyEyeballs,
		HTTP2Origin:            s.HTTP2Origin,
		MatchSNIToHost:         s.MatchSNIToHost,
	}
	if s.KeepAliveConnections != nil {
		n := int(*s.KeepAliveConnections)
		o.KeepAliveConnections = &n
	}
	if s.KeepAliveTimeout != nil {
		o.KeepAliveTimeout = &s.KeepAliveTimeout.Duration
	}
	if s.TLSTimeout != nil {
		o.TLSTimeout = &s.TLSTimeout.Duration
	}
	if s.TCPKeepAlive != nil {
		o.TCPKeepAlive = &s.TCPKeepAlive.Duration
	}
	return o
}

// originPolicyForTarget returns the winning CloudflareOriginPolicy targeting the
// given group/kind/name, or nil. When multiple policies target the same
// resource, the oldest (by creationTimestamp, then namespaced name) wins.
func originPolicyForTarget(policies []cfv1alpha1.CloudflareOriginPolicy, kind, name string) *cfv1alpha1.CloudflareOriginPolicy {
	var winner *cfv1alpha1.CloudflareOriginPolicy
	for i := range policies {
		if !targetsResource(policies[i].Spec.TargetRefs, gwapiv1.GroupName, kind, name) {
			continue
		}
		if winner == nil || policyOlderThan(&policies[i], winner) {
			winner = &policies[i]
		}
	}
	return winner
}

// effectiveOriginRequest resolves the inherited origin config for a route:
// a Gateway-level policy provides defaults, a route-level policy overrides them.
func effectiveOriginRequest(policies []cfv1alpha1.CloudflareOriginPolicy, gwName, routeKind, routeName string) *cfclient.OriginRequest {
	var gwCfg, routeCfg *cfclient.OriginRequest
	if p := originPolicyForTarget(policies, "Gateway", gwName); p != nil {
		gwCfg = originRequestFromPolicy(p)
	}
	if p := originPolicyForTarget(policies, routeKind, routeName); p != nil {
		routeCfg = originRequestFromPolicy(p)
	}
	// Route-level fields win; Gateway-level fills the rest.
	return cfclient.MergeOriginRequest(routeCfg, gwCfg)
}

// patchOriginPolicyStatuses sets GEP-713 ancestor status on CloudflareOriginPolicy
// resources that target this Gateway or one of its attached routes. Policies that
// do not apply to this Gateway are left untouched. A Gateway-level policy also
// gets an Overridden condition when a route-level policy supersedes it for some
// routes. Returns the set of target keys an accepted policy directly attaches to.
func (r *GatewayReconciler) patchOriginPolicyStatuses(ctx context.Context, policies []cfv1alpha1.CloudflareOriginPolicy, gw *gwapiv1.Gateway, valid map[string]bool) map[string]bool {
	logger := log.FromContext(ctx)
	all := make([]policyTarget, len(policies))
	for i := range policies {
		all[i] = policyTarget{obj: &policies[i], refs: policies[i].Spec.TargetRefs}
	}

	// A Gateway-level default is overridden if any accepted route-level policy
	// targets an attached route.
	routeOverrideExists := false
	for i := range policies {
		if targetsAnyRoute(all[i].refs) {
			if accepted, _, _ := evaluatePolicyAcceptance(all[i], all, valid); accepted {
				routeOverrideExists = true
				break
			}
		}
	}

	ancestor := gatewayAncestorRef(gw)
	affected := map[string]bool{}
	for i := range policies {
		accepted, reason, msg := evaluatePolicyAcceptance(all[i], all, valid)
		if reason == "TargetNotFound" {
			// Policy does not apply to this Gateway. Prune any ancestor entry we
			// wrote on a previous reconcile (it was retargeted or its route detached).
			if removeAncestor(&policies[i].Status, ancestor, r.ControllerName) {
				if err := r.Client.Status().Update(ctx, &policies[i]); err != nil {
					logger.Error(err, "Failed to prune stale CloudflareOriginPolicy ancestor status", "policy", policies[i].Name)
				}
			}
			continue
		}
		upsertAncestorCondition(&policies[i].Status, ancestor, r.ControllerName,
			acceptedCondition(policies[i].Generation, accepted, reason, msg))
		// A Gateway-targeting policy always carries an Overridden condition so it
		// is cleared (set False) when the policy stops applying its defaults —
		// e.g. it loses a conflict and becomes Accepted=False. A non-accepted
		// policy provides no defaults, so it is never "overridden".
		if targetsResource(all[i].refs, gwapiv1.GroupName, "Gateway", gw.Name) {
			upsertAncestorCondition(&policies[i].Status, ancestor, r.ControllerName,
				overriddenCondition(policies[i].Generation, accepted && routeOverrideExists))
		}
		if err := r.Client.Status().Update(ctx, &policies[i]); err != nil {
			logger.Error(err, "Failed to patch CloudflareOriginPolicy status", "policy", policies[i].Name)
		}
		if accepted {
			addAffectedTargets(affected, all[i].refs, valid)
		}
	}
	return affected
}

// applyOriginPolicies merges effective origin config into ingress rules by route
// identity (kind + name), independent of rule order. effectiveOriginRequest is
// cached per route so repeated rules of one route don't recompute it.
func applyOriginPolicies(policies []cfv1alpha1.CloudflareOriginPolicy, gwName string, rules []cfclient.BuiltRule) {
	cache := map[string]*cfclient.OriginRequest{}
	for i := range rules {
		key := rules[i].RouteKind + "/" + rules[i].RouteName
		cfg, ok := cache[key]
		if !ok {
			cfg = effectiveOriginRequest(policies, gwName, rules[i].RouteKind, rules[i].RouteName)
			cache[key] = cfg
		}
		if cfg == nil {
			continue
		}
		// Merge onto a per-rule base so the in-place MergeOriginRequest never
		// shares cfg across rules (cfg is a cached, possibly-aliased pointer
		// used only as the read-only override).
		base := rules[i].OriginRequest
		if base == nil {
			base = &cfclient.OriginRequest{}
		}
		rules[i].OriginRequest = cfclient.MergeOriginRequest(base, cfg)
	}
}
