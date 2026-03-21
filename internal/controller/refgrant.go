package controller

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"
)

// CheckReferenceGrant checks if a cross-namespace reference is permitted by a
// ReferenceGrant in the target namespace.
//
// Returns true if a ReferenceGrant exists in toNS that allows references from
// fromNS/fromKind to toKind/toName.
func CheckReferenceGrant(ctx context.Context, c client.Client, fromNS, fromKind, toNS, toKind, toName string) (bool, error) {
	var grantList gwapiv1beta1.ReferenceGrantList
	if err := c.List(ctx, &grantList, client.InNamespace(toNS)); err != nil {
		return false, err
	}

	for _, grant := range grantList.Items {
		if !fromMatches(grant.Spec.From, fromNS, fromKind) {
			continue
		}
		if toMatches(grant.Spec.To, toKind, toName) {
			return true, nil
		}
	}

	return false, nil
}

func fromMatches(entries []gwapiv1beta1.ReferenceGrantFrom, fromNS, fromKind string) bool {
	for _, f := range entries {
		if string(f.Group) == "gateway.networking.k8s.io" &&
			string(f.Kind) == fromKind &&
			string(f.Namespace) == fromNS {
			return true
		}
	}
	return false
}

func toMatches(entries []gwapiv1beta1.ReferenceGrantTo, toKind, toName string) bool {
	for _, t := range entries {
		// Empty group means core API group (Service, Secret, etc.)
		groupOK := string(t.Group) == "" || string(t.Group) == "core"
		kindOK := string(t.Kind) == toKind
		nameOK := t.Name == nil || string(*t.Name) == toName
		if groupOK && kindOK && nameOK {
			return true
		}
	}
	return false
}
