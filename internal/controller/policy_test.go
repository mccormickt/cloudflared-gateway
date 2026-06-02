package controller

import (
	"testing"
	"time"

	cfv1alpha1 "github.com/mccormickt/cloudflared-gateway/api/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func originPolicyTargeting(name string, created time.Time, targetKind, targetName string) cfv1alpha1.CloudflareOriginPolicy {
	return cfv1alpha1.CloudflareOriginPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(created),
		},
		Spec: cfv1alpha1.CloudflareOriginPolicySpec{
			TargetRefs: []gwapiv1.LocalPolicyTargetReference{{
				Group: gwapiv1.GroupName,
				Kind:  gwapiv1.Kind(targetKind),
				Name:  gwapiv1.ObjectName(targetName),
			}},
		},
	}
}

func TestEvaluatePolicyAcceptance_OldestWinsConflict(t *testing.T) {
	t0 := time.Unix(1000, 0)
	older := originPolicyTargeting("older", t0, "HTTPRoute", "route")
	newer := originPolicyTargeting("newer", t0.Add(time.Hour), "HTTPRoute", "route")

	all := []policyTarget{
		{obj: &older, refs: older.Spec.TargetRefs},
		{obj: &newer, refs: newer.Spec.TargetRefs},
	}
	valid := map[string]bool{targetKey("HTTPRoute", "route"): true}

	if accepted, reason, _ := evaluatePolicyAcceptance(all[0], all, valid); !accepted || reason != "Accepted" {
		t.Errorf("older policy should be Accepted, got accepted=%v reason=%s", accepted, reason)
	}
	if accepted, reason, _ := evaluatePolicyAcceptance(all[1], all, valid); accepted || reason != "Conflicted" {
		t.Errorf("newer policy should be Conflicted, got accepted=%v reason=%s", accepted, reason)
	}
}

func TestEvaluatePolicyAcceptance_TargetNotFound(t *testing.T) {
	p := originPolicyTargeting("p", time.Unix(1000, 0), "HTTPRoute", "absent")
	all := []policyTarget{{obj: &p, refs: p.Spec.TargetRefs}}
	valid := map[string]bool{targetKey("Gateway", "gw"): true}

	accepted, reason, _ := evaluatePolicyAcceptance(all[0], all, valid)
	if accepted || reason != "TargetNotFound" {
		t.Errorf("policy targeting an absent route should be TargetNotFound, got accepted=%v reason=%s", accepted, reason)
	}
}
