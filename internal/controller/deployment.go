package controller

import (
	"fmt"

	"github.com/mccormickt/cloudflare-tunnel-controller/internal/cloudflare"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

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

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deployName,
			Namespace: gw.Namespace,
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: gw.APIVersion,
				Kind:       gw.Kind,
				Name:       gw.Name,
				UID:        gw.UID,
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
						Image: cloudflare.ContainerImage,
						Args:  []string{"tunnel", "--metrics", "0.0.0.0:2000", "run"},
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
}
