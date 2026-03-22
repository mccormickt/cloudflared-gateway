package controller

import (
	"context"
	"fmt"

	cf "github.com/cloudflare/cloudflare-go"
	cfv1alpha1 "github.com/mccormickt/cloudflare-tunnel-controller/api/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// applyAccessPolicies looks up CloudflareAccessPolicy resources that target
// the given Gateway or HTTPRoutes (via GEP-713 Policy Attachment targetRefs),
// and sets originRequest.access on matching ingress rules.
func (r *tunnelReconciler) applyAccessPolicies(ctx context.Context, rules []cf.UnvalidatedIngressRule, gw *gwapiv1.Gateway, httpRoutes []gwapiv1.HTTPRoute) ([]cf.UnvalidatedIngressRule, error) {
	logger := log.FromContext(ctx)

	// List all CloudflareAccessPolicy resources in the Gateway's namespace
	var policyList cfv1alpha1.CloudflareAccessPolicyList
	if err := r.client.List(ctx, &policyList, client.InNamespace(gw.Namespace)); err != nil {
		return nil, fmt.Errorf("listing CloudflareAccessPolicy resources: %w", err)
	}

	if len(policyList.Items) == 0 {
		return rules, nil
	}

	// Check for a policy targeting the Gateway itself — applies to ALL ingress rules
	gatewayAccessCfg := findAccessConfigForTarget(policyList.Items, gwapiv1.GroupName, "Gateway", gw.Name)

	if gatewayAccessCfg != nil {
		// Gateway-level policy: apply to all rules
		for i := range rules {
			if rules[i].OriginRequest == nil {
				rules[i].OriginRequest = &cf.OriginRequestConfig{}
			}
			rules[i].OriginRequest.Access = gatewayAccessCfg
		}
		logger.V(1).Info("Applied Gateway-level CloudflareAccessPolicy",
			"gateway", gw.Name, "teamName", gatewayAccessCfg.TeamName,
			"rulesAffected", len(rules))

		// Set status on matching policies
		for i := range policyList.Items {
			if targetsResource(policyList.Items[i].Spec.TargetRefs, gwapiv1.GroupName, "Gateway", gw.Name) {
				if err := PatchAccessPolicyStatus(ctx, r.client, &policyList.Items[i], true); err != nil {
					logger.Error(err, "Failed to patch CloudflareAccessPolicy status", "policy", policyList.Items[i].Name)
				}
			}
		}

		return rules, nil
	}

	// Check for policies targeting individual HTTPRoutes
	idx := 0
	for i := range httpRoutes {
		route := &httpRoutes[i]
		routeAccessCfg := findAccessConfigForTarget(policyList.Items, gwapiv1.GroupName, "HTTPRoute", route.Name)

		for _, rule := range route.Spec.Rules {
			paths := countPaths(rule.Matches)
			ruleCount := rulesProduced(len(route.Spec.Hostnames), paths)

			if routeAccessCfg != nil {
				for j := 0; j < ruleCount && idx+j < len(rules); j++ {
					if rules[idx+j].OriginRequest == nil {
						rules[idx+j].OriginRequest = &cf.OriginRequestConfig{}
					}
					rules[idx+j].OriginRequest.Access = routeAccessCfg
				}
				logger.V(1).Info("Applied route-level CloudflareAccessPolicy",
					"route", route.Name, "teamName", routeAccessCfg.TeamName,
					"rulesAffected", ruleCount)
			}

			idx += ruleCount
		}
	}

	// Set status on all policies that matched targets
	for i := range policyList.Items {
		policy := &policyList.Items[i]
		applied := false
		// Check if this policy targeted the gateway
		if targetsResource(policy.Spec.TargetRefs, gwapiv1.GroupName, "Gateway", gw.Name) {
			applied = true
		}
		// Check if it targeted any of the HTTP routes
		if !applied {
			for _, route := range httpRoutes {
				if targetsResource(policy.Spec.TargetRefs, gwapiv1.GroupName, "HTTPRoute", route.Name) {
					applied = true
					break
				}
			}
		}
		if applied {
			if err := PatchAccessPolicyStatus(ctx, r.client, policy, true); err != nil {
				logger.Error(err, "Failed to patch CloudflareAccessPolicy status", "policy", policy.Name)
			}
		}
	}

	return rules, nil
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

// PatchAccessPolicyStatus sets Accepted and Programmed conditions on a CloudflareAccessPolicy.
func PatchAccessPolicyStatus(ctx context.Context, c client.Client, policy *cfv1alpha1.CloudflareAccessPolicy, accepted bool) error {
	status := metav1.ConditionTrue
	reason := "Accepted"
	message := "Policy is accepted and programmed"
	if !accepted {
		status = metav1.ConditionFalse
		reason = "Invalid"
		message = "Policy target not found"
	}

	now := metav1.Now()
	policy.Status.Conditions = setCondition(policy.Status.Conditions, metav1.Condition{
		Type:               "Accepted",
		Status:             status,
		ObservedGeneration: policy.Generation,
		LastTransitionTime: now,
		Reason:             reason,
		Message:            message,
	})
	policy.Status.Conditions = setCondition(policy.Status.Conditions, metav1.Condition{
		Type:               "Programmed",
		Status:             status,
		ObservedGeneration: policy.Generation,
		LastTransitionTime: now,
		Reason:             reason,
		Message:            message,
	})

	return c.Status().Update(ctx, policy)
}

// findAccessConfigForTarget searches policies for one targeting the given resource.
// Uses the None merge strategy (GEP-713 Direct policy): oldest policy wins.
func findAccessConfigForTarget(policies []cfv1alpha1.CloudflareAccessPolicy, group, kind, name string) *cf.AccessConfig {
	for _, policy := range policies {
		for _, ref := range policy.Spec.TargetRefs {
			if string(ref.Group) == group &&
				string(ref.Kind) == kind &&
				string(ref.Name) == name {
				return &cf.AccessConfig{
					Required: policy.Spec.Required,
					TeamName: policy.Spec.TeamName,
					AudTag:   policy.Spec.AudTag,
				}
			}
		}
	}
	return nil
}

// countPaths returns the number of paths extracted from matches, mirroring
// the logic in extractPaths from ingress.go.
func countPaths(matches []gwapiv1.HTTPRouteMatch) int {
	count := 0
	for _, m := range matches {
		if m.Path == nil {
			continue
		}
		value := "/"
		if m.Path.Value != nil {
			value = *m.Path.Value
		}
		pathType := gwapiv1.PathMatchPathPrefix
		if m.Path.Type != nil {
			pathType = *m.Path.Type
		}
		switch pathType {
		case gwapiv1.PathMatchExact:
			count++
		case gwapiv1.PathMatchPathPrefix:
			if value != "/" {
				count++
			}
		case gwapiv1.PathMatchRegularExpression:
			count++
		}
	}
	return count
}

// rulesProduced calculates how many ingress rules a single HTTPRoute rule
// produces, matching the logic in BuildIngressRules.
func rulesProduced(numHostnames, numPaths int) int {
	if numHostnames == 0 {
		if numPaths == 0 {
			return 1
		}
		return numPaths
	}
	if numPaths == 0 {
		return numHostnames
	}
	return numHostnames * numPaths
}
