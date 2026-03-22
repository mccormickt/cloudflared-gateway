package controller

import (
	"context"

	cf "github.com/cloudflare/cloudflare-go"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

// GetBackendTLSConfig looks up a BackendTLSPolicy targeting the given service
// and returns the corresponding Cloudflare OriginRequestConfig.
//
// If no matching policy is found, it returns noTLSVerify: true for backward
// compatibility with the previous hardcoded behavior.
func GetBackendTLSConfig(ctx context.Context, c client.Client, serviceNS, serviceName string) *cf.OriginRequestConfig {
	logger := log.FromContext(ctx)

	var policyList gwapiv1.BackendTLSPolicyList
	if err := c.List(ctx, &policyList, client.InNamespace(serviceNS)); err != nil {
		logger.V(1).Info("Failed to list BackendTLSPolicies, falling back to noTLSVerify", "error", err)
		noTLS := true
		return &cf.OriginRequestConfig{NoTLSVerify: &noTLS}
	}

	for _, policy := range policyList.Items {
		if policyTargetsService(policy.Spec.TargetRefs, serviceName) {
			return buildOriginRequestFromPolicy(&policy)
		}
	}

	// No matching policy — backward-compatible default
	noTLS := true
	return &cf.OriginRequestConfig{NoTLSVerify: &noTLS}
}

// policyTargetsService checks whether any of the policy's targetRefs reference
// the named Service (group="" or "core", kind="Service").
func policyTargetsService(refs []gwapiv1.LocalPolicyTargetReferenceWithSectionName, serviceName string) bool {
	for _, ref := range refs {
		group := string(ref.Group)
		if group != "" && group != "core" {
			continue
		}
		if string(ref.Kind) != "Service" {
			continue
		}
		if string(ref.Name) == serviceName {
			return true
		}
	}
	return false
}

// buildOriginRequestFromPolicy maps BackendTLSPolicy validation fields to
// Cloudflare OriginRequestConfig.
//
// Mapping:
//   - validation.hostname → originServerName
//   - wellKnownCACertificates: "System" → trust system CAs (no noTLSVerify, no caPool)
//   - caCertificateRefs present → originServerName only (caPool is not supported
//     for remotely-managed tunnels)
//   - Neither wellKnownCACerts nor caCertRefs → originServerName only
func buildOriginRequestFromPolicy(policy *gwapiv1.BackendTLSPolicy) *cf.OriginRequestConfig {
	cfg := &cf.OriginRequestConfig{}

	hostname := string(policy.Spec.Validation.Hostname)
	if hostname != "" {
		cfg.OriginServerName = &hostname
	}

	// If wellKnownCACertificates is "System", we trust system CAs — no
	// noTLSVerify needed, and caPool can't be set for remote tunnels anyway.
	// If caCertificateRefs are present, we still set originServerName (already
	// done above) but caPool file paths don't work for remote tunnel configs.
	// In both cases we simply don't set noTLSVerify, which means cloudflared
	// will verify the origin cert (the desired behavior when a policy exists).

	return cfg
}

// applyBackendTLSPolicies overrides the originRequest on TLS ingress rules
// based on BackendTLSPolicy resources targeting the backend services.
func (r *tunnelReconciler) applyBackendTLSPolicies(ctx context.Context, rules []cf.UnvalidatedIngressRule, tlsRoutes []gwapiv1alpha2.TLSRoute) []cf.UnvalidatedIngressRule {
	if len(tlsRoutes) == 0 {
		return rules
	}

	// Build a map from "hostname → service" so we can match rules to backends.
	type backendKey struct {
		namespace string
		name      string
	}
	hostnameToBackend := make(map[string]backendKey)

	for i := range tlsRoutes {
		route := &tlsRoutes[i]
		routeNS := route.Namespace
		if routeNS == "" {
			routeNS = "default"
		}
		for _, rule := range route.Spec.Rules {
			if len(rule.BackendRefs) == 0 {
				continue
			}
			ref := rule.BackendRefs[0]
			ns := routeNS
			if ref.Namespace != nil {
				ns = string(*ref.Namespace)
			}
			bk := backendKey{namespace: ns, name: string(ref.Name)}

			if len(route.Spec.Hostnames) == 0 {
				// No hostname — use empty string as key for catch-all
				hostnameToBackend[""] = bk
			} else {
				for _, h := range route.Spec.Hostnames {
					hostnameToBackend[string(h)] = bk
				}
			}
		}
	}

	// Override originRequest for matching rules
	for i := range rules {
		bk, ok := hostnameToBackend[rules[i].Hostname]
		if !ok {
			continue
		}
		cfg := GetBackendTLSConfig(ctx, r.client, bk.namespace, bk.name)
		rules[i].OriginRequest = cfg
	}

	return rules
}
