package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=cloudflare
//
// CloudflareAccessPolicy is a Direct Attached Policy (per GEP-713) that
// configures Cloudflare Access JWT enforcement on targeted Gateway API resources.
// It uses the Policy Attachment pattern with targetRefs to specify which
// Gateway, HTTPRoute, or Service resources should have Access enforcement.
type CloudflareAccessPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CloudflareAccessPolicySpec   `json:"spec"`
	Status            CloudflareAccessPolicyStatus `json:"status,omitempty"`
}

type CloudflareAccessPolicySpec struct {
	// TargetRefs identifies the Gateway API resources this policy applies to.
	// Supported kinds: Gateway, HTTPRoute, GRPCRoute, Service.
	//
	// When targeting a Gateway, all routes attached to it get Access enforcement.
	// When targeting a route, only that route's ingress rules get Access enforcement.
	//
	// +listType=map
	// +listMapKey=group
	// +listMapKey=kind
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	TargetRefs []gwapiv1.LocalPolicyTargetReference `json:"targetRefs"`

	// TeamName is the Cloudflare organization team name for JWT validation.
	TeamName string `json:"teamName"`
	// Required enforces Access JWT validation on all requests.
	Required bool `json:"required,omitempty"`
	// AudTag is the audience tags to verify against Access JWT aud claim.
	AudTag []string `json:"audTag,omitempty"`
}

type CloudflareAccessPolicyStatus struct {
	// Conditions describe the current state of the policy.
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type CloudflareAccessPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CloudflareAccessPolicy `json:"items"`
}
