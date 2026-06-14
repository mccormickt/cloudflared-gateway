package controller

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"
)

// CheckReferenceGrant checks if a cross-namespace reference to a resource in the
// core API group (Service, Secret, …) is permitted by a ReferenceGrant in the
// target namespace.
//
// Returns true if a ReferenceGrant exists in toNS that allows references from
// fromNS/fromKind to toKind/toName.
func CheckReferenceGrant(ctx context.Context, c client.Client, fromNS, fromKind, toNS, toKind, toName string) (bool, error) {
	return CheckReferenceGrantTo(ctx, c, fromNS, fromKind, toNS, "", toKind, toName)
}

// CheckReferenceGrantTo is CheckReferenceGrant with an explicit target API
// group, so references to extension resources (e.g. XBackend in
// gateway.networking.x-k8s.io) can be authorized in addition to core resources.
// An empty toGroup denotes the core API group.
func CheckReferenceGrantTo(ctx context.Context, c client.Client, fromNS, fromKind, toNS, toGroup, toKind, toName string) (bool, error) {
	var grantList gwapiv1beta1.ReferenceGrantList
	if err := c.List(ctx, &grantList, client.InNamespace(toNS)); err != nil {
		return false, err
	}

	for _, grant := range grantList.Items {
		if !fromMatches(grant.Spec.From, fromNS, fromKind) {
			continue
		}
		if toMatches(grant.Spec.To, toGroup, toKind, toName) {
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

func toMatches(entries []gwapiv1beta1.ReferenceGrantTo, toGroup, toKind, toName string) bool {
	for _, t := range entries {
		kindOK := string(t.Kind) == toKind
		nameOK := t.Name == nil || string(*t.Name) == toName
		if groupMatches(string(t.Group), toGroup) && kindOK && nameOK {
			return true
		}
	}
	return false
}

// groupMatches reports whether a ReferenceGrant "to" group matches the desired
// group. An empty desired group denotes the core API group, for which the grant
// may spell the group as "" or "core".
func groupMatches(grantGroup, want string) bool {
	if want == "" {
		return grantGroup == "" || grantGroup == "core"
	}
	return grantGroup == want
}
