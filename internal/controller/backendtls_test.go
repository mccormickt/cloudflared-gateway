package controller

import (
	"context"
	"testing"

	cf "github.com/cloudflare/cloudflare-go"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

func makeBackendTLSPolicy(name, namespace, serviceName string, hostname string, wellKnownCA *gwapiv1.WellKnownCACertificatesType) *gwapiv1.BackendTLSPolicy {
	policy := &gwapiv1.BackendTLSPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: gwapiv1.BackendTLSPolicySpec{
			TargetRefs: []gwapiv1.LocalPolicyTargetReferenceWithSectionName{{
				LocalPolicyTargetReference: gwapiv1.LocalPolicyTargetReference{
					Group: "",
					Kind:  "Service",
					Name:  gwapiv1.ObjectName(serviceName),
				},
			}},
			Validation: gwapiv1.BackendTLSPolicyValidation{
				Hostname: gwapiv1.PreciseHostname(hostname),
			},
		},
	}
	if wellKnownCA != nil {
		policy.Spec.Validation.WellKnownCACertificates = wellKnownCA
	}
	return policy
}

func TestGetBackendTLSConfig_WithHostname(t *testing.T) {
	scheme := testScheme()
	policy := makeBackendTLSPolicy("tls-policy", "default", "my-svc", "backend.internal", nil)

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy).Build()

	cfg, err := GetBackendTLSConfig(context.Background(), c, "default", "my-svc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.OriginServerName == nil {
		t.Fatal("expected originServerName to be set")
	}
	if *cfg.OriginServerName != "backend.internal" {
		t.Errorf("expected originServerName 'backend.internal', got %q", *cfg.OriginServerName)
	}
	if cfg.NoTLSVerify != nil {
		t.Errorf("noTLSVerify should not be set when a policy exists, got %v", *cfg.NoTLSVerify)
	}
}

func TestGetBackendTLSConfig_SystemCAs(t *testing.T) {
	scheme := testScheme()
	system := gwapiv1.WellKnownCACertificatesSystem
	policy := makeBackendTLSPolicy("tls-policy", "default", "my-svc", "backend.internal", &system)

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy).Build()

	cfg, err := GetBackendTLSConfig(context.Background(), c, "default", "my-svc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.OriginServerName == nil {
		t.Fatal("expected originServerName to be set")
	}
	if *cfg.OriginServerName != "backend.internal" {
		t.Errorf("expected originServerName 'backend.internal', got %q", *cfg.OriginServerName)
	}
	if cfg.NoTLSVerify != nil {
		t.Errorf("noTLSVerify should not be set when using system CAs, got %v", *cfg.NoTLSVerify)
	}
	if cfg.CAPool != nil {
		t.Errorf("caPool should not be set for remote tunnels, got %q", *cfg.CAPool)
	}
}

func TestGetBackendTLSConfig_NoPolicyFallback(t *testing.T) {
	scheme := testScheme()

	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	cfg, err := GetBackendTLSConfig(context.Background(), c, "default", "my-svc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.NoTLSVerify == nil || !*cfg.NoTLSVerify {
		t.Error("expected noTLSVerify=true when no BackendTLSPolicy exists")
	}
	if cfg.OriginServerName != nil {
		t.Errorf("originServerName should not be set without a policy, got %q", *cfg.OriginServerName)
	}
}

func TestGetBackendTLSConfig_PolicyDifferentService(t *testing.T) {
	scheme := testScheme()
	// Policy targets "other-svc", not "my-svc"
	policy := makeBackendTLSPolicy("tls-policy", "default", "other-svc", "other.internal", nil)

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy).Build()

	cfg, err := GetBackendTLSConfig(context.Background(), c, "default", "my-svc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.NoTLSVerify == nil || !*cfg.NoTLSVerify {
		t.Error("expected noTLSVerify=true when no matching policy exists")
	}
}

func TestGetBackendTLSConfig_PolicyDifferentNamespace(t *testing.T) {
	scheme := testScheme()
	// Policy is in "other-ns", looking up in "default"
	policy := makeBackendTLSPolicy("tls-policy", "other-ns", "my-svc", "backend.internal", nil)

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy).Build()

	cfg, err := GetBackendTLSConfig(context.Background(), c, "default", "my-svc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.NoTLSVerify == nil || !*cfg.NoTLSVerify {
		t.Error("expected noTLSVerify=true when policy is in different namespace")
	}
}

func TestGetBackendTLSConfig_WithCACertRefs(t *testing.T) {
	scheme := testScheme()
	policy := makeBackendTLSPolicy("tls-policy", "default", "my-svc", "backend.internal", nil)
	policy.Spec.Validation.CACertificateRefs = []gwapiv1.LocalObjectReference{{
		Group: "",
		Kind:  "ConfigMap",
		Name:  "ca-bundle",
	}}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy).Build()

	cfg, err := GetBackendTLSConfig(context.Background(), c, "default", "my-svc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.OriginServerName == nil || *cfg.OriginServerName != "backend.internal" {
		t.Error("expected originServerName to be set from hostname")
	}
	// caPool is not supported for remotely-managed tunnels
	if cfg.CAPool != nil {
		t.Errorf("caPool should not be set for remote tunnels, got %q", *cfg.CAPool)
	}
	if cfg.NoTLSVerify != nil {
		t.Errorf("noTLSVerify should not be set when a policy exists, got %v", *cfg.NoTLSVerify)
	}
}

// T17: Test applyBackendTLSPolicies integration
func TestApplyBackendTLSPolicies(t *testing.T) {
	scheme := testScheme()
	policy := makeBackendTLSPolicy("tls-policy", "default", "tls-svc", "backend.internal", nil)

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy).Build()
	mock := newMockClient()

	r := &GatewayReconciler{
		Client:           c,
		CloudflareClient: mock,
		ControllerName:   gwapiv1.GatewayController(ControllerName),
	}

	p := gwapiv1.PortNumber(8443)
	tlsRoutes := []gwapiv1alpha2.TLSRoute{{
		ObjectMeta: metav1.ObjectMeta{Name: "tls-route", Namespace: "default"},
		Spec: gwapiv1alpha2.TLSRouteSpec{
			Hostnames: []gwapiv1.Hostname{"secure.example.com"},
			Rules: []gwapiv1alpha2.TLSRouteRule{{
				BackendRefs: []gwapiv1.BackendRef{{
					BackendObjectReference: gwapiv1.BackendObjectReference{
						Name: "tls-svc",
						Port: &p,
					},
				}},
			}},
		},
	}}

	noTLS := true
	rules := []cf.UnvalidatedIngressRule{{
		Hostname:      "secure.example.com",
		Service:       "https://tls-svc.default:8443",
		OriginRequest: &cf.OriginRequestConfig{NoTLSVerify: &noTLS},
	}}

	result, err := r.applyBackendTLSPolicies(context.Background(), rules, tlsRoutes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(result))
	}
	if result[0].OriginRequest == nil {
		t.Fatal("expected originRequest to be set")
	}
	if result[0].OriginRequest.OriginServerName == nil || *result[0].OriginRequest.OriginServerName != "backend.internal" {
		t.Errorf("expected originServerName 'backend.internal', got %v", result[0].OriginRequest.OriginServerName)
	}
	// Policy exists, so noTLSVerify should NOT be set
	if result[0].OriginRequest.NoTLSVerify != nil {
		t.Errorf("noTLSVerify should not be set when a BackendTLSPolicy exists, got %v", *result[0].OriginRequest.NoTLSVerify)
	}
}
