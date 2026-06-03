package controller

import (
	"context"
	"testing"

	cfv1alpha1 "github.com/mccormickt/cloudflared-gateway/api/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func makeTunnelConfig(name, namespace string, spec cfv1alpha1.CloudflareTunnelConfigSpec) *cfv1alpha1.CloudflareTunnelConfig {
	return &cfv1alpha1.CloudflareTunnelConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       spec,
	}
}

func gcParametersRef(name, namespace string) *gwapiv1.ParametersReference {
	ns := gwapiv1.Namespace(namespace)
	return &gwapiv1.ParametersReference{
		Group:     gwapiv1.Group(cfv1alpha1.GroupVersion.Group),
		Kind:      gwapiv1.Kind(tunnelConfigKind),
		Name:      name,
		Namespace: &ns,
	}
}

func TestResolveTunnelConfig_GatewayClassDefault(t *testing.T) {
	scheme := testScheme()
	cfg := makeTunnelConfig("cfg", "default", cfv1alpha1.CloudflareTunnelConfigSpec{Replicas: ptr(int32(3))})
	gw := makeGateway("gw", "default")
	gc := makeGatewayClass()
	gc.Spec.ParametersRef = gcParametersRef("cfg", "default")

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cfg, gw, gc).Build()
	r := &GatewayReconciler{Client: c, ControllerName: gwapiv1.GatewayController(ControllerName)}

	got, _, _ := r.resolveTunnelConfig(context.Background(), gw, gc)
	if got == nil || got.Replicas == nil || *got.Replicas != 3 {
		t.Fatalf("expected replicas=3 from GatewayClass default, got %+v", got)
	}
}

func TestResolveTunnelConfig_GatewayOverridesClass(t *testing.T) {
	scheme := testScheme()
	classCfg := makeTunnelConfig("class-cfg", "default", cfv1alpha1.CloudflareTunnelConfigSpec{
		Replicas: ptr(int32(3)),
		Image:    ptr("class-image"),
	})
	gwCfg := makeTunnelConfig("gw-cfg", "default", cfv1alpha1.CloudflareTunnelConfigSpec{Replicas: ptr(int32(5))})
	gw := makeGateway("gw", "default")
	gw.Spec.Infrastructure = &gwapiv1.GatewayInfrastructure{
		ParametersRef: &gwapiv1.LocalParametersReference{
			Group: gwapiv1.Group(cfv1alpha1.GroupVersion.Group),
			Kind:  gwapiv1.Kind(tunnelConfigKind),
			Name:  "gw-cfg",
		},
	}
	gc := makeGatewayClass()
	gc.Spec.ParametersRef = gcParametersRef("class-cfg", "default")

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(classCfg, gwCfg, gw, gc).Build()
	r := &GatewayReconciler{Client: c, ControllerName: gwapiv1.GatewayController(ControllerName)}

	got, _, _ := r.resolveTunnelConfig(context.Background(), gw, gc)
	if got == nil {
		t.Fatal("expected resolved config")
	}
	if got.Replicas == nil || *got.Replicas != 5 {
		t.Errorf("Gateway-level replicas=5 should win, got %v", got.Replicas)
	}
	if got.Image == nil || *got.Image != "class-image" {
		t.Errorf("GatewayClass-level image should be inherited, got %v", got.Image)
	}
}

func TestResolveTunnelConfig_InvalidRefUsesDefaults(t *testing.T) {
	scheme := testScheme()
	gw := makeGateway("gw", "default")
	gc := makeGatewayClass()
	gc.Spec.ParametersRef = gcParametersRef("missing", "default")

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(gw, gc).Build()
	r := &GatewayReconciler{Client: c, ControllerName: gwapiv1.GatewayController(ControllerName)}

	got, ok, msg := r.resolveTunnelConfig(context.Background(), gw, gc)
	if got != nil {
		t.Errorf("invalid ref should yield nil config (defaults), got %+v", got)
	}
	if ok {
		t.Error("invalid GatewayClass parametersRef should report classParamsOK=false")
	}
	if msg == "" {
		t.Error("expected a message describing the invalid parametersRef")
	}
}

func TestBuildCloudflaredDeployment_Overlay(t *testing.T) {
	gw := makeGateway("gw", "default")
	cfg := &cfv1alpha1.CloudflareTunnelConfigSpec{
		Replicas:    ptr(int32(4)),
		Image:       ptr("custom/cloudflared:test"),
		LogLevel:    ptr("debug"),
		MetricsPort: ptr(int32(9000)),
	}

	d := BuildCloudflaredDeployment(gw, "secret", cfg)
	if d.Spec.Replicas == nil || *d.Spec.Replicas != 4 {
		t.Errorf("expected replicas=4, got %v", d.Spec.Replicas)
	}
	container := d.Spec.Template.Spec.Containers[0]
	if container.Image != "custom/cloudflared:test" {
		t.Errorf("expected overridden image, got %q", container.Image)
	}
	if container.Ports[0].ContainerPort != 9000 {
		t.Errorf("expected metrics port 9000, got %d", container.Ports[0].ContainerPort)
	}
	foundLogLevel := false
	for i, a := range container.Args {
		if a == "--loglevel" && i+1 < len(container.Args) && container.Args[i+1] == "debug" {
			foundLogLevel = true
		}
	}
	if !foundLogLevel {
		t.Errorf("expected --loglevel debug in args, got %v", container.Args)
	}
	// securityContext is always fixed.
	if container.SecurityContext == nil || container.SecurityContext.RunAsNonRoot == nil || !*container.SecurityContext.RunAsNonRoot {
		t.Error("securityContext RunAsNonRoot must remain enforced")
	}
}

func TestBuildCloudflaredDeployment_DefaultsWhenNil(t *testing.T) {
	gw := makeGateway("gw", "default")
	d := BuildCloudflaredDeployment(gw, "secret", nil)
	if d.Spec.Replicas == nil || *d.Spec.Replicas != 2 {
		t.Errorf("expected default replicas=2, got %v", d.Spec.Replicas)
	}
	if d.Spec.Template.Spec.Containers[0].Image != ContainerImage {
		t.Errorf("expected default image, got %q", d.Spec.Template.Spec.Containers[0].Image)
	}
}
