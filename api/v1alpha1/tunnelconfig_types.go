package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=cloudflare
//
// CloudflareTunnelConfig customizes the cloudflared Deployment a Gateway runs.
// It is an infrastructure-parameters object (not a policy): it is referenced,
// not attached. Attach it cluster-wide via GatewayClass.spec.parametersRef, or
// per-Gateway via Gateway.spec.infrastructure.parametersRef (which overrides the
// GatewayClass-level default). Unset fields fall through to built-in defaults.
type CloudflareTunnelConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CloudflareTunnelConfigSpec `json:"spec"`
	// +optional
	Status CloudflareTunnelConfigStatus `json:"status,omitempty"`
}

type CloudflareTunnelConfigSpec struct {
	// Replicas is the number of cloudflared pods. Defaults to 2.
	// +kubebuilder:validation:Minimum=1
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// Image overrides the cloudflared container image.
	// +optional
	Image *string `json:"image,omitempty"`

	// Resources sets the cloudflared container resource requests/limits.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// LogLevel sets the cloudflared --loglevel flag.
	// +kubebuilder:validation:Enum=debug;info;warn;error;fatal
	// +optional
	LogLevel *string `json:"logLevel,omitempty"`

	// MetricsPort sets the cloudflared metrics port. Defaults to 2000.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	MetricsPort *int32 `json:"metricsPort,omitempty"`

	// PodLabels are added to the cloudflared pod template.
	// +optional
	PodLabels map[string]string `json:"podLabels,omitempty"`

	// PodAnnotations are added to the cloudflared pod template.
	// +optional
	PodAnnotations map[string]string `json:"podAnnotations,omitempty"`

	// NodeSelector constrains the cloudflared pods to matching nodes.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations are applied to the cloudflared pods.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// Affinity sets scheduling affinity for the cloudflared pods.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`
}

type CloudflareTunnelConfigStatus struct {
	// Conditions describe the current state of the config.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type CloudflareTunnelConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CloudflareTunnelConfig `json:"items"`
}
