package controller

import (
	"fmt"

	cfv1alpha1 "github.com/mccormickt/cloudflared-gateway/api/v1alpha1"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// ContainerImage is the default cloudflared container image used in the tunnel deployment.
const ContainerImage = "cloudflare/cloudflared:2024.12.2"

// defaultMetricsPort is the default cloudflared metrics port.
const defaultMetricsPort int32 = 2000

// DeploymentName returns the cloudflared Deployment name for a Gateway.
func DeploymentName(gwName string) string {
	return fmt.Sprintf("cloudflared-%s", gwName)
}

// BuildCloudflaredDeployment creates the cloudflared Deployment spec for a Gateway.
// The tunnel token is read from the referenced Secret. Non-nil fields of cfg
// (resolved from a CloudflareTunnelConfig via parametersRef) override the
// built-in defaults; the pod security context is always fixed.
func BuildCloudflaredDeployment(gw *gwapiv1.Gateway, secretName string, cfg *cfv1alpha1.CloudflareTunnelConfigSpec) *appsv1.Deployment {
	replicas := int32(2)
	image := ContainerImage
	metricsPort := defaultMetricsPort
	if cfg != nil {
		if cfg.Replicas != nil {
			replicas = *cfg.Replicas
		}
		if cfg.Image != nil {
			image = *cfg.Image
		}
		if cfg.MetricsPort != nil {
			metricsPort = *cfg.MetricsPort
		}
	}

	args := []string{"tunnel", "--metrics", fmt.Sprintf("0.0.0.0:%d", metricsPort)}
	if cfg != nil && cfg.LogLevel != nil {
		args = append(args, "--loglevel", *cfg.LogLevel)
	}
	args = append(args, "run")

	deployName := DeploymentName(gw.Name)
	// selectorLabels is immutable on a Deployment, so it must stay fixed: it only
	// ever carries the identity label. PodLabels and Gateway infrastructure labels
	// are layered onto the object/template labels only, never the selector.
	selectorLabels := map[string]string{"app": deployName}
	labels := map[string]string{"app": deployName}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deployName,
			Namespace: gw.Namespace,
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         "gateway.networking.k8s.io/v1",
				Kind:               "Gateway",
				Name:               gw.Name,
				UID:                gw.UID,
				Controller:         boolPtr(true),
				BlockOwnerDeletion: boolPtr(true),
			}},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: selectorLabels,
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name:  "cloudflared",
						Image: image,
						Args:  args,
						SecurityContext: &v1.SecurityContext{
							AllowPrivilegeEscalation: boolPtr(false),
							RunAsNonRoot:             boolPtr(true),
							RunAsUser:                int64Ptr(65532),
							RunAsGroup:               int64Ptr(65532),
							Capabilities: &v1.Capabilities{
								Drop: []v1.Capability{"ALL"},
							},
							SeccompProfile: &v1.SeccompProfile{
								Type: v1.SeccompProfileTypeRuntimeDefault,
							},
						},
						Env: []v1.EnvVar{{
							Name: "TUNNEL_TOKEN",
							ValueFrom: &v1.EnvVarSource{
								SecretKeyRef: &v1.SecretKeySelector{
									LocalObjectReference: v1.LocalObjectReference{
										Name: secretName,
									},
									Key: tunnelTokenKey,
								},
							},
						}},
						Ports: []v1.ContainerPort{{
							Name:          "metrics",
							ContainerPort: metricsPort,
						}},
					}},
				},
			},
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxUnavailable: &intstr.IntOrString{
						Type:   intstr.String,
						StrVal: "50%",
					},
					MaxSurge: &intstr.IntOrString{
						Type:   intstr.String,
						StrVal: "100%",
					},
				},
			},
		},
	}

	// Overlay CloudflareTunnelConfig customization onto the pod template.
	if cfg != nil {
		container := &deployment.Spec.Template.Spec.Containers[0]
		if cfg.Resources != nil {
			container.Resources = *cfg.Resources
		}
		pod := &deployment.Spec.Template
		for k, v := range cfg.PodLabels {
			pod.Labels[k] = v
		}
		if len(cfg.PodAnnotations) > 0 {
			if pod.Annotations == nil {
				pod.Annotations = make(map[string]string)
			}
			for k, v := range cfg.PodAnnotations {
				pod.Annotations[k] = v
			}
		}
		pod.Spec.NodeSelector = cfg.NodeSelector
		pod.Spec.Tolerations = cfg.Tolerations
		pod.Spec.Affinity = cfg.Affinity
	}

	// Propagate Gateway infrastructure labels and annotations to the Deployment and pod template.
	if gw.Spec.Infrastructure != nil {
		for k, v := range gw.Spec.Infrastructure.Labels {
			deployment.Labels[string(k)] = string(v)
			deployment.Spec.Template.Labels[string(k)] = string(v)
		}
		if len(gw.Spec.Infrastructure.Annotations) > 0 {
			if deployment.Annotations == nil {
				deployment.Annotations = make(map[string]string)
			}
			if deployment.Spec.Template.Annotations == nil {
				deployment.Spec.Template.Annotations = make(map[string]string)
			}
			for k, v := range gw.Spec.Infrastructure.Annotations {
				deployment.Annotations[string(k)] = string(v)
				deployment.Spec.Template.Annotations[string(k)] = string(v)
			}
		}
	}

	return deployment
}
