package controller

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// ContainerImage is the cloudflared container image used in the tunnel deployment.
const ContainerImage = "cloudflare/cloudflared:2024.12.2"

// DeploymentName returns the cloudflared Deployment name for a Gateway.
func DeploymentName(gwName string) string {
	return fmt.Sprintf("cloudflared-%s", gwName)
}

// BuildCloudflaredDeployment creates the cloudflared Deployment spec for a Gateway.
// The tunnel token is read from the referenced Secret.
func BuildCloudflaredDeployment(gw *gwapiv1.Gateway, secretName string) *appsv1.Deployment {
	var replicas int32 = 2
	deployName := DeploymentName(gw.Name)
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
				MatchLabels: labels,
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name:  "cloudflared",
						Image: ContainerImage,
						Args:  []string{"tunnel", "--metrics", "0.0.0.0:2000", "run"},
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
							ContainerPort: 2000,
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
