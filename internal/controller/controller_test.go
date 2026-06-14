package controller

import (
	"context"
	"fmt"
	"sync"
	"testing"

	cfv1alpha1 "github.com/mccormickt/cloudflared-gateway/api/v1alpha1"
	cfclient "github.com/mccormickt/cloudflared-gateway/internal/cloudflare"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
	gwapiv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"
)

// ---------------------------------------------------------------------------
// Mock Cloudflare client
// ---------------------------------------------------------------------------

type mockCall struct {
	method string
	args   []any
}

type mockCloudflareClient struct {
	mu             sync.Mutex
	calls          []mockCall
	existingTunnel *cfclient.Tunnel
	accountID      string
	createErr      error
	deleteErr      error
	configErr      error
}

func newMockClient() *mockCloudflareClient {
	return &mockCloudflareClient{accountID: "test-account"}
}

func (m *mockCloudflareClient) withExistingTunnel(id, name string) *mockCloudflareClient {
	m.existingTunnel = &cfclient.Tunnel{ID: id, Name: name}
	return m
}

func (m *mockCloudflareClient) record(method string, args ...any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, mockCall{method: method, args: args})
}

func (m *mockCloudflareClient) getCalls() []mockCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]mockCall, len(m.calls))
	copy(result, m.calls)
	return result
}

func (m *mockCloudflareClient) AccountID() string { return m.accountID }

func (m *mockCloudflareClient) CreateTunnel(ctx context.Context, name string, secret []byte) (cfclient.Tunnel, error) {
	m.record("CreateTunnel", name)
	if m.createErr != nil {
		return cfclient.Tunnel{}, m.createErr
	}
	return cfclient.Tunnel{ID: "mock-tunnel-id", Name: name}, nil
}

func (m *mockCloudflareClient) GetTunnelByName(ctx context.Context, name string) (cfclient.Tunnel, error) {
	m.record("GetTunnelByName", name)
	if m.existingTunnel != nil && m.existingTunnel.Name == name {
		return *m.existingTunnel, nil
	}
	return cfclient.Tunnel{}, cfclient.ErrTunnelNotFound
}

func (m *mockCloudflareClient) DeleteTunnel(ctx context.Context, id string) error {
	m.record("DeleteTunnel", id)
	return m.deleteErr
}

func (m *mockCloudflareClient) UpdateTunnelConfiguration(ctx context.Context, tunnelID string, ingress []cfclient.IngressRule) error {
	m.record("UpdateTunnelConfiguration", tunnelID, len(ingress))
	return m.configErr
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(gwapiv1.Install(s))
	utilruntime.Must(gwapiv1alpha2.Install(s))
	utilruntime.Must(gwapiv1beta1.Install(s))
	utilruntime.Must(cfv1alpha1.AddToScheme(s))
	return s
}

func makeGatewayClass() *gwapiv1.GatewayClass {
	return &gwapiv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cloudflare-tunnel",
		},
		Spec: gwapiv1.GatewayClassSpec{
			ControllerName: gwapiv1.GatewayController(ControllerName),
		},
	}
}

func makeGateway(name, namespace string) *gwapiv1.Gateway {
	from := gwapiv1.NamespacesFromSame
	return &gwapiv1.Gateway{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "gateway.networking.k8s.io/v1",
			Kind:       "Gateway",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			UID:        "test-uid",
			Generation: 1,
		},
		Spec: gwapiv1.GatewaySpec{
			GatewayClassName: "cloudflare-tunnel",
			Listeners: []gwapiv1.Listener{{
				Name:     "http",
				Port:     80,
				Protocol: gwapiv1.HTTPProtocolType,
				AllowedRoutes: &gwapiv1.AllowedRoutes{
					Namespaces: &gwapiv1.RouteNamespaces{From: &from},
				},
			}},
		},
	}
}

func makeHTTPRoute(name, namespace, gwName string) *gwapiv1.HTTPRoute {
	gwGroup := gwapiv1.Group(gwapiv1.GroupName)
	gwKind := gwapiv1.Kind("Gateway")
	gwNS := gwapiv1.Namespace(namespace)
	port := gwapiv1.PortNumber(80)

	return &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: gwapiv1.HTTPRouteSpec{
			CommonRouteSpec: gwapiv1.CommonRouteSpec{
				ParentRefs: []gwapiv1.ParentReference{{
					Group:     &gwGroup,
					Kind:      &gwKind,
					Namespace: &gwNS,
					Name:      gwapiv1.ObjectName(gwName),
				}},
			},
			Hostnames: []gwapiv1.Hostname{"example.com"},
			Rules: []gwapiv1.HTTPRouteRule{{
				BackendRefs: []gwapiv1.HTTPBackendRef{{
					BackendRef: gwapiv1.BackendRef{
						BackendObjectReference: gwapiv1.BackendObjectReference{
							Name: "web-svc",
							Port: &port,
						},
					},
				}},
			}},
		},
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestReconcile_NoMatchingGatewayClass(t *testing.T) {
	scheme := testScheme()
	// Gateway exists but GatewayClass doesn't
	gw := makeGateway("test-gw", "default")

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(gw).Build()
	mock := newMockClient()

	r := &GatewayReconciler{
		Client:           c,
		CloudflareClient: mock,
		ControllerName:   gwapiv1.GatewayController(ControllerName),
	}

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-gw", Namespace: "default"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter > 0 {
		t.Error("should not requeue")
	}

	calls := mock.getCalls()
	if len(calls) != 0 {
		t.Errorf("expected no Cloudflare API calls, got %d", len(calls))
	}
}

func TestReconcile_WrongControllerName(t *testing.T) {
	scheme := testScheme()
	gw := makeGateway("test-gw", "default")
	gc := &gwapiv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "cloudflare-tunnel"},
		Spec: gwapiv1.GatewayClassSpec{
			ControllerName: "other-controller",
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(gw, gc).Build()
	mock := newMockClient()

	r := &GatewayReconciler{
		Client:           c,
		CloudflareClient: mock,
		ControllerName:   gwapiv1.GatewayController(ControllerName),
	}

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-gw", Namespace: "default"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter > 0 {
		t.Error("should not requeue")
	}

	calls := mock.getCalls()
	if len(calls) != 0 {
		t.Errorf("expected no Cloudflare API calls, got %d", len(calls))
	}
}

func TestReconcile_CreatesNewTunnel(t *testing.T) {
	scheme := testScheme()
	gw := makeGateway("test-gw", "default")
	gc := makeGatewayClass()

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(gw, gc).
		WithStatusSubresource(gw, gc).
		Build()
	mock := newMockClient()

	r := &GatewayReconciler{
		Client:           c,
		CloudflareClient: mock,
		ControllerName:   gwapiv1.GatewayController(ControllerName),
	}

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-gw", Namespace: "default"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	calls := mock.getCalls()

	// Should have: GetTunnelByName, CreateTunnel, UpdateTunnelConfiguration
	var hasGet, hasCreate, hasConfig bool
	for _, call := range calls {
		switch call.method {
		case "GetTunnelByName":
			hasGet = true
		case "CreateTunnel":
			hasCreate = true
		case "UpdateTunnelConfiguration":
			hasConfig = true
		}
	}

	if !hasGet {
		t.Error("expected GetTunnelByName call")
	}
	if !hasCreate {
		t.Error("expected CreateTunnel call")
	}
	if !hasConfig {
		t.Error("expected UpdateTunnelConfiguration call")
	}

	// Verify Secret was created
	var secret v1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      TunnelSecretName("test-gw"),
		Namespace: "default",
	}, &secret); err != nil {
		t.Fatalf("expected tunnel secret to exist: %v", err)
	}

	// Verify tunnel-secret key has 32 bytes
	if len(secret.Data[tunnelSecretKey]) != 32 {
		t.Errorf("tunnel secret should be 32 bytes, got %d", len(secret.Data[tunnelSecretKey]))
	}
}

func TestReconcile_ExistingTunnelUpdatesConfig(t *testing.T) {
	scheme := testScheme()
	gw := makeGateway("test-gw", "default")
	gc := makeGatewayClass()

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(gw, gc).
		WithStatusSubresource(gw, gc).
		Build()
	mock := newMockClient().withExistingTunnel("existing-id", "test-gw")

	r := &GatewayReconciler{
		Client:           c,
		CloudflareClient: mock,
		ControllerName:   gwapiv1.GatewayController(ControllerName),
	}

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-gw", Namespace: "default"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	calls := mock.getCalls()

	// Should NOT create a new tunnel
	for _, call := range calls {
		if call.method == "CreateTunnel" {
			t.Error("should not create tunnel when one exists")
		}
	}

	// Should still push config
	var hasConfig bool
	for _, call := range calls {
		if call.method == "UpdateTunnelConfiguration" {
			hasConfig = true
		}
	}
	if !hasConfig {
		t.Error("expected UpdateTunnelConfiguration call")
	}
}

func TestReconcile_HTTPRouteIngressRules(t *testing.T) {
	scheme := testScheme()
	gw := makeGateway("test-gw", "default")
	gc := makeGatewayClass()
	route := makeHTTPRoute("web-route", "default", "test-gw")

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(gw, gc, route).
		WithStatusSubresource(gw, gc, route).
		Build()
	mock := newMockClient()

	r := &GatewayReconciler{
		Client:           c,
		CloudflareClient: mock,
		ControllerName:   gwapiv1.GatewayController(ControllerName),
	}

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-gw", Namespace: "default"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check that UpdateTunnelConfiguration was called with 2 rules (1 route rule + 1 catch-all)
	calls := mock.getCalls()
	for _, call := range calls {
		if call.method == "UpdateTunnelConfiguration" {
			ingressLen := call.args[1].(int)
			if ingressLen != 2 {
				t.Errorf("expected 2 ingress rules (1 route + catch-all), got %d", ingressLen)
			}
		}
	}
}

func TestReconcile_GatewayNotFound(t *testing.T) {
	scheme := testScheme()

	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	mock := newMockClient()

	r := &GatewayReconciler{
		Client:           c,
		CloudflareClient: mock,
		ControllerName:   gwapiv1.GatewayController(ControllerName),
	}

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "nonexistent", Namespace: "default"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter > 0 {
		t.Error("should not requeue for deleted gateway")
	}
}

func TestCleanup_BestEffort(t *testing.T) {
	scheme := testScheme()
	gw := makeGateway("test-gw", "default")

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(gw).Build()
	mock := newMockClient().withExistingTunnel("tunnel-id", "test-gw")

	r := &GatewayReconciler{
		Client:           c,
		CloudflareClient: mock,
		ControllerName:   gwapiv1.GatewayController(ControllerName),
	}

	err := r.cleanup(context.Background(), gw)

	// Should succeed (no resources to delete from K8s is OK)
	if err != nil {
		t.Fatalf("cleanup should succeed: %v", err)
	}

	// Verify tunnel deletion was attempted
	calls := mock.getCalls()
	var hasDelete bool
	for _, call := range calls {
		if call.method == "DeleteTunnel" {
			hasDelete = true
			if call.args[0] != "tunnel-id" {
				t.Errorf("expected tunnel-id, got %v", call.args[0])
			}
		}
	}
	if !hasDelete {
		t.Error("expected DeleteTunnel call")
	}
}

func TestRouteAttachment_SameNamespace(t *testing.T) {
	scheme := testScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	ctx := context.Background()

	from := gwapiv1.NamespacesFromSame
	gw := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
		Spec: gwapiv1.GatewaySpec{
			Listeners: []gwapiv1.Listener{{
				Protocol: gwapiv1.HTTPProtocolType,
				AllowedRoutes: &gwapiv1.AllowedRoutes{
					Namespaces: &gwapiv1.RouteNamespaces{From: &from},
				},
			}},
		},
	}

	allowed, err := CheckRouteAttachment(ctx, c, gw, "default", "HTTPRoute")
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Error("same-namespace HTTPRoute should be allowed")
	}

	allowed, err = CheckRouteAttachment(ctx, c, gw, "other", "HTTPRoute")
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Error("different-namespace HTTPRoute should not be allowed")
	}
}

func TestRouteAttachment_ProtocolMismatch(t *testing.T) {
	scheme := testScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	ctx := context.Background()

	from := gwapiv1.NamespacesFromAll
	gw := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
		Spec: gwapiv1.GatewaySpec{
			Listeners: []gwapiv1.Listener{{
				Protocol: gwapiv1.TLSProtocolType,
				AllowedRoutes: &gwapiv1.AllowedRoutes{
					Namespaces: &gwapiv1.RouteNamespaces{From: &from},
				},
			}},
		},
	}

	allowed, _ := CheckRouteAttachment(ctx, c, gw, "default", "HTTPRoute")
	if allowed {
		t.Error("HTTPRoute should not attach to TLS listener")
	}
	allowed, _ = CheckRouteAttachment(ctx, c, gw, "default", "TLSRoute")
	if !allowed {
		t.Error("TLSRoute should attach to TLS listener")
	}
}

// ---------------------------------------------------------------------------
// Tests: Error types and policy
// ---------------------------------------------------------------------------

func TestIsPermanent(t *testing.T) {
	if !IsPermanent(ConfigError("bad spec")) {
		t.Error("ConfigError should be permanent")
	}
	if IsPermanent(KubeError(fmt.Errorf("timeout"))) {
		t.Error("KubeError should not be permanent")
	}
	if IsPermanent(CloudflareError(fmt.Errorf("rate limit"))) {
		t.Error("CloudflareError should not be permanent")
	}
	if IsPermanent(FinalizerError(fmt.Errorf("conflict"))) {
		t.Error("FinalizerError should not be permanent")
	}
	if IsPermanent(fmt.Errorf("plain error")) {
		t.Error("plain error should not be permanent")
	}
}

func TestReconcile_PermanentErrorNoRequeue(t *testing.T) {
	scheme := testScheme()
	// Gateway with empty UID triggers ConfigError
	gw := makeGateway("test-gw", "default")
	gw.UID = "" // clear UID
	gc := makeGatewayClass()

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(gw, gc).
		WithStatusSubresource(gw, gc).
		Build()
	mock := newMockClient()

	r := &GatewayReconciler{
		Client:           c,
		CloudflareClient: mock,
		ControllerName:   gwapiv1.GatewayController(ControllerName),
	}

	// Reconcile should return nil error (permanent error is swallowed)
	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-gw", Namespace: "default"},
	})

	if err != nil {
		t.Errorf("permanent error should not be returned, got: %v", err)
	}
	if result.RequeueAfter > 0 {
		t.Error("should not requeue for permanent error")
	}
}

func TestReconcile_RetriableErrorRequeues(t *testing.T) {
	scheme := testScheme()
	gw := makeGateway("test-gw", "default")
	gc := makeGatewayClass()

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(gw, gc).
		WithStatusSubresource(gw, gc).
		Build()
	mock := newMockClient()
	mock.createErr = fmt.Errorf("API rate limit exceeded")

	r := &GatewayReconciler{
		Client:           c,
		CloudflareClient: mock,
		ControllerName:   gwapiv1.GatewayController(ControllerName),
	}

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-gw", Namespace: "default"},
	})

	if err == nil {
		t.Error("retriable error should be returned for requeue")
	}
}

// ---------------------------------------------------------------------------
// Tests: Namespace selector
// ---------------------------------------------------------------------------

func TestNamespaceSelector_MatchLabels(t *testing.T) {
	scheme := testScheme()
	ns := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "team-a",
			Labels: map[string]string{"team": "alpha", "env": "prod"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ns).Build()
	ctx := context.Background()

	from := gwapiv1.NamespacesFromSelector
	gw := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
		Spec: gwapiv1.GatewaySpec{
			Listeners: []gwapiv1.Listener{{
				Protocol: gwapiv1.HTTPProtocolType,
				AllowedRoutes: &gwapiv1.AllowedRoutes{
					Namespaces: &gwapiv1.RouteNamespaces{
						From: &from,
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"team": "alpha"},
						},
					},
				},
			}},
		},
	}

	allowed, err := CheckRouteAttachment(ctx, c, gw, "team-a", "HTTPRoute")
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Error("namespace with matching labels should be allowed")
	}

	// Non-matching namespace
	nsB := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "team-b",
			Labels: map[string]string{"team": "beta"},
		},
	}
	c = fake.NewClientBuilder().WithScheme(scheme).WithObjects(ns, nsB).Build()

	allowed, err = CheckRouteAttachment(ctx, c, gw, "team-b", "HTTPRoute")
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Error("namespace with non-matching labels should not be allowed")
	}
}

func TestNamespaceSelector_MatchExpressions(t *testing.T) {
	scheme := testScheme()
	ns := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "staging",
			Labels: map[string]string{"env": "staging"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ns).Build()
	ctx := context.Background()

	from := gwapiv1.NamespacesFromSelector
	gw := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
		Spec: gwapiv1.GatewaySpec{
			Listeners: []gwapiv1.Listener{{
				Protocol: gwapiv1.HTTPProtocolType,
				AllowedRoutes: &gwapiv1.AllowedRoutes{
					Namespaces: &gwapiv1.RouteNamespaces{
						From: &from,
						Selector: &metav1.LabelSelector{
							MatchExpressions: []metav1.LabelSelectorRequirement{{
								Key:      "env",
								Operator: metav1.LabelSelectorOpIn,
								Values:   []string{"staging", "prod"},
							}},
						},
					},
				},
			}},
		},
	}

	allowed, err := CheckRouteAttachment(ctx, c, gw, "staging", "HTTPRoute")
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Error("namespace matching In expression should be allowed")
	}

	// DoesNotExist test
	gw.Spec.Listeners[0].AllowedRoutes.Namespaces.Selector = &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{{
			Key:      "restricted",
			Operator: metav1.LabelSelectorOpDoesNotExist,
		}},
	}

	allowed, err = CheckRouteAttachment(ctx, c, gw, "staging", "HTTPRoute")
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Error("namespace without 'restricted' label should match DoesNotExist")
	}
}

// ---------------------------------------------------------------------------
// Tests: ReferenceGrant
// ---------------------------------------------------------------------------

func TestReferenceGrant_Allowed(t *testing.T) {
	scheme := testScheme()
	grant := &gwapiv1beta1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "allow-routes",
			Namespace: "backend",
		},
		Spec: gwapiv1beta1.ReferenceGrantSpec{
			From: []gwapiv1beta1.ReferenceGrantFrom{{
				Group:     "gateway.networking.k8s.io",
				Kind:      "HTTPRoute",
				Namespace: "frontend",
			}},
			To: []gwapiv1beta1.ReferenceGrantTo{{
				Group: "",
				Kind:  "Service",
			}},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(grant).Build()

	allowed, err := CheckReferenceGrant(context.Background(), c, "frontend", "HTTPRoute", "backend", "Service", "api-svc")
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Error("ReferenceGrant should allow cross-namespace reference")
	}
}

func TestReferenceGrant_Denied(t *testing.T) {
	scheme := testScheme()
	// Grant exists but for different source namespace
	grant := &gwapiv1beta1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "allow-routes",
			Namespace: "backend",
		},
		Spec: gwapiv1beta1.ReferenceGrantSpec{
			From: []gwapiv1beta1.ReferenceGrantFrom{{
				Group:     "gateway.networking.k8s.io",
				Kind:      "HTTPRoute",
				Namespace: "other-ns",
			}},
			To: []gwapiv1beta1.ReferenceGrantTo{{
				Group: "",
				Kind:  "Service",
			}},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(grant).Build()

	allowed, err := CheckReferenceGrant(context.Background(), c, "frontend", "HTTPRoute", "backend", "Service", "api-svc")
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Error("ReferenceGrant should deny when source namespace doesn't match")
	}
}

// ---------------------------------------------------------------------------
// Tests: BuildCloudflaredDeployment — Infrastructure propagation
// ---------------------------------------------------------------------------

func TestBuildDeployment_InfrastructureLabels(t *testing.T) {
	gw := makeGateway("test-gw", "default")
	gw.Spec.Infrastructure = &gwapiv1.GatewayInfrastructure{
		Labels: map[gwapiv1.LabelKey]gwapiv1.LabelValue{
			"team":        "platform",
			"environment": "prod",
		},
		Annotations: map[gwapiv1.AnnotationKey]gwapiv1.AnnotationValue{
			"prometheus.io/scrape": "true",
			"custom.io/owner":      "team-a",
		},
	}

	deploy := BuildCloudflaredDeployment(gw, "test-secret", nil)

	// Check deployment-level labels
	if deploy.Labels["team"] != "platform" {
		t.Errorf("deployment label 'team': got %q, want %q", deploy.Labels["team"], "platform")
	}
	if deploy.Labels["environment"] != "prod" {
		t.Errorf("deployment label 'environment': got %q, want %q", deploy.Labels["environment"], "prod")
	}
	// Original label should still be present
	if deploy.Labels["app"] != "cloudflared-test-gw" {
		t.Errorf("deployment label 'app': got %q, want %q", deploy.Labels["app"], "cloudflared-test-gw")
	}

	// Check pod template labels
	if deploy.Spec.Template.Labels["team"] != "platform" {
		t.Errorf("pod template label 'team': got %q, want %q", deploy.Spec.Template.Labels["team"], "platform")
	}
	if deploy.Spec.Template.Labels["environment"] != "prod" {
		t.Errorf("pod template label 'environment': got %q, want %q", deploy.Spec.Template.Labels["environment"], "prod")
	}

	// Check deployment-level annotations
	if deploy.Annotations["prometheus.io/scrape"] != "true" {
		t.Errorf("deployment annotation 'prometheus.io/scrape': got %q, want %q", deploy.Annotations["prometheus.io/scrape"], "true")
	}
	if deploy.Annotations["custom.io/owner"] != "team-a" {
		t.Errorf("deployment annotation 'custom.io/owner': got %q, want %q", deploy.Annotations["custom.io/owner"], "team-a")
	}

	// Check pod template annotations
	if deploy.Spec.Template.Annotations["prometheus.io/scrape"] != "true" {
		t.Errorf("pod template annotation 'prometheus.io/scrape': got %q, want %q", deploy.Spec.Template.Annotations["prometheus.io/scrape"], "true")
	}
	if deploy.Spec.Template.Annotations["custom.io/owner"] != "team-a" {
		t.Errorf("pod template annotation 'custom.io/owner': got %q, want %q", deploy.Spec.Template.Annotations["custom.io/owner"], "team-a")
	}
}

func TestBuildDeployment_NoInfrastructure(t *testing.T) {
	gw := makeGateway("test-gw", "default")
	// No Infrastructure set (nil)

	deploy := BuildCloudflaredDeployment(gw, "test-secret", nil)

	// Original labels should be present and unchanged
	if deploy.Labels["app"] != "cloudflared-test-gw" {
		t.Errorf("deployment label 'app': got %q, want %q", deploy.Labels["app"], "cloudflared-test-gw")
	}
	if len(deploy.Labels) != 1 {
		t.Errorf("expected 1 deployment label, got %d", len(deploy.Labels))
	}
	if deploy.Spec.Template.Labels["app"] != "cloudflared-test-gw" {
		t.Errorf("pod template label 'app': got %q, want %q", deploy.Spec.Template.Labels["app"], "cloudflared-test-gw")
	}
	if len(deploy.Spec.Template.Labels) != 1 {
		t.Errorf("expected 1 pod template label, got %d", len(deploy.Spec.Template.Labels))
	}

	// Annotations should be nil
	if deploy.Annotations != nil {
		t.Errorf("expected nil deployment annotations, got %v", deploy.Annotations)
	}
	if deploy.Spec.Template.Annotations != nil {
		t.Errorf("expected nil pod template annotations, got %v", deploy.Spec.Template.Annotations)
	}
}

func TestBuildDeployment_InfrastructureLabelsOnly(t *testing.T) {
	gw := makeGateway("test-gw", "default")
	gw.Spec.Infrastructure = &gwapiv1.GatewayInfrastructure{
		Labels: map[gwapiv1.LabelKey]gwapiv1.LabelValue{
			"team": "platform",
		},
		// No annotations
	}

	deploy := BuildCloudflaredDeployment(gw, "test-secret", nil)

	if deploy.Labels["team"] != "platform" {
		t.Errorf("deployment label 'team': got %q, want %q", deploy.Labels["team"], "platform")
	}
	// Annotations should remain nil since none were specified
	if deploy.Annotations != nil {
		t.Errorf("expected nil deployment annotations, got %v", deploy.Annotations)
	}
	if deploy.Spec.Template.Annotations != nil {
		t.Errorf("expected nil pod template annotations, got %v", deploy.Spec.Template.Annotations)
	}
}

func TestReferenceGrant_NamedTarget(t *testing.T) {
	scheme := testScheme()
	targetName := gwapiv1.ObjectName("specific-svc")
	grant := &gwapiv1beta1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "allow-specific",
			Namespace: "backend",
		},
		Spec: gwapiv1beta1.ReferenceGrantSpec{
			From: []gwapiv1beta1.ReferenceGrantFrom{{
				Group:     "gateway.networking.k8s.io",
				Kind:      "HTTPRoute",
				Namespace: "frontend",
			}},
			To: []gwapiv1beta1.ReferenceGrantTo{{
				Group: "",
				Kind:  "Service",
				Name:  &targetName,
			}},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(grant).Build()

	// Matching name
	allowed, _ := CheckReferenceGrant(context.Background(), c, "frontend", "HTTPRoute", "backend", "Service", "specific-svc")
	if !allowed {
		t.Error("should allow when target name matches")
	}

	// Non-matching name
	allowed, _ = CheckReferenceGrant(context.Background(), c, "frontend", "HTTPRoute", "backend", "Service", "other-svc")
	if allowed {
		t.Error("should deny when target name doesn't match")
	}
}

// ---------------------------------------------------------------------------
// T19: Cleanup failure continuation
// ---------------------------------------------------------------------------

func TestCleanup_FailureContinuation(t *testing.T) {
	scheme := testScheme()
	gw := makeGateway("test-gw", "default")

	// Create a deployment and secret that cleanup should try to delete
	deployName := DeploymentName("test-gw")
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deployName,
			Namespace: "default",
		},
	}
	secretName := TunnelSecretName("test-gw")
	secret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: "default",
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(gw, deploy, secret).Build()
	mock := newMockClient().withExistingTunnel("tunnel-id", "test-gw")
	mock.deleteErr = fmt.Errorf("tunnel delete API error")

	r := &GatewayReconciler{
		Client:           c,
		CloudflareClient: mock,
		ControllerName:   gwapiv1.GatewayController(ControllerName),
	}

	err := r.cleanup(context.Background(), gw)

	// Should return the first error (tunnel deletion)
	if err == nil {
		t.Fatal("cleanup should return error when tunnel deletion fails")
	}
	if err.Error() != "tunnel delete API error" {
		t.Errorf("expected tunnel delete error, got: %v", err)
	}

	// Verify all three cleanup steps were attempted despite the tunnel delete error
	calls := mock.getCalls()
	var hasGet, hasDelete bool
	for _, call := range calls {
		switch call.method {
		case "GetTunnelByName":
			hasGet = true
		case "DeleteTunnel":
			hasDelete = true
		}
	}
	if !hasGet {
		t.Error("expected GetTunnelByName call")
	}
	if !hasDelete {
		t.Error("expected DeleteTunnel call")
	}

	// Deployment and secret should still have been attempted for deletion
	// (they would succeed since the fake client allows it)
	var existingDeploy appsv1.Deployment
	deployErr := c.Get(context.Background(), types.NamespacedName{Name: deployName, Namespace: "default"}, &existingDeploy)
	if deployErr == nil {
		t.Error("deployment should have been deleted despite tunnel delete failure")
	}

	var existingSecret v1.Secret
	secretErr := c.Get(context.Background(), types.NamespacedName{Name: secretName, Namespace: "default"}, &existingSecret)
	if secretErr == nil {
		t.Error("secret should have been deleted despite tunnel delete failure")
	}
}

// ---------------------------------------------------------------------------
// T20: Infrastructure label collision with 'app' label
// ---------------------------------------------------------------------------

func TestBuildDeployment_InfrastructureLabelCollision(t *testing.T) {
	gw := makeGateway("test-gw", "default")
	gw.Spec.Infrastructure = &gwapiv1.GatewayInfrastructure{
		Labels: map[gwapiv1.LabelKey]gwapiv1.LabelValue{
			"app": "override",
		},
	}

	deploy := BuildCloudflaredDeployment(gw, "test-secret", nil)

	// Infrastructure labels are layered onto the object/template labels, so the
	// 'app' label there reflects the override.
	if deploy.Labels["app"] != "override" {
		t.Errorf("infrastructure 'app' label should override built-in object label, got %q", deploy.Labels["app"])
	}

	// The selector is immutable on a Deployment, so it must never be affected by
	// infrastructure labels — it always keeps the built-in identity label.
	wantSelector := DeploymentName(gw.Name)
	if got := deploy.Spec.Selector.MatchLabels["app"]; got != wantSelector {
		t.Errorf("selector 'app' label must stay %q, got %q", wantSelector, got)
	}
}

func TestBuildDeployment_PodLabelsDoNotPolluteSelector(t *testing.T) {
	gw := makeGateway("test-gw", "default")
	cfg := &cfv1alpha1.CloudflareTunnelConfigSpec{
		PodLabels: map[string]string{"team": "alpha", "app": "should-not-leak"},
	}

	deploy := BuildCloudflaredDeployment(gw, "test-secret", cfg)

	// PodLabels apply to the pod template.
	if deploy.Spec.Template.Labels["team"] != "alpha" {
		t.Errorf("expected pod template to carry podLabel team=alpha, got %q", deploy.Spec.Template.Labels["team"])
	}

	// The immutable selector must not pick up any pod labels — otherwise a later
	// change to PodLabels makes the Deployment update fail (selector is immutable).
	wantSelector := DeploymentName(gw.Name)
	if got := deploy.Spec.Selector.MatchLabels["app"]; got != wantSelector {
		t.Errorf("selector 'app' must stay %q, got %q", wantSelector, got)
	}
	if _, leaked := deploy.Spec.Selector.MatchLabels["team"]; leaked {
		t.Errorf("podLabel 'team' must not leak into the immutable selector: %v", deploy.Spec.Selector.MatchLabels)
	}
}

// ---------------------------------------------------------------------------
// T18: CloudflareOriginPolicy application tests
// ---------------------------------------------------------------------------

func ptr[T any](v T) *T { return &v }

func makeOriginPolicy(name, namespace, targetKind, targetName string, spec cfv1alpha1.CloudflareOriginPolicySpec) cfv1alpha1.CloudflareOriginPolicy {
	spec.TargetRefs = []gwapiv1.LocalPolicyTargetReference{{
		Group: gwapiv1.GroupName,
		Kind:  gwapiv1.Kind(targetKind),
		Name:  gwapiv1.ObjectName(targetName),
	}}
	return cfv1alpha1.CloudflareOriginPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       spec,
	}
}

func TestFindAccessConfigForTarget_OldestWins(t *testing.T) {
	mkPolicy := func(name, team string, createdUnix int64) cfv1alpha1.CloudflareAccessPolicy {
		return cfv1alpha1.CloudflareAccessPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:              name,
				Namespace:         "default",
				CreationTimestamp: metav1.Unix(createdUnix, 0),
			},
			Spec: cfv1alpha1.CloudflareAccessPolicySpec{
				TargetRefs: []gwapiv1.LocalPolicyTargetReference{{
					Group: gwapiv1.GroupName,
					Kind:  "Gateway",
					Name:  "gw",
				}},
				TeamName: team,
			},
		}
	}

	// 'zzz' is the older policy and must win, even though 'aaa' sorts first and
	// would be returned by a naive first-match (client List order is not
	// creationTimestamp-ordered). This must agree with the Conflicted/Accepted
	// status decided by evaluatePolicyAcceptance/policyOlderThan.
	older := mkPolicy("zzz", "good-team", 100)
	newer := mkPolicy("aaa", "evil-team", 200)

	for _, order := range [][]cfv1alpha1.CloudflareAccessPolicy{
		{newer, older},
		{older, newer},
	} {
		cfg := findAccessConfigForTarget(order, gwapiv1.GroupName, "Gateway", "gw")
		if cfg == nil {
			t.Fatalf("expected a matching access config, got nil")
		}
		if cfg.TeamName != "good-team" {
			t.Errorf("oldest policy should win: want team good-team, got %q (order %v)", cfg.TeamName, order)
		}
	}
}

func httpRouteWithBackend(name string, hostnames ...gwapiv1.Hostname) gwapiv1.HTTPRoute {
	port := gwapiv1.PortNumber(80)
	return gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: gwapiv1.HTTPRouteSpec{
			Hostnames: hostnames,
			Rules: []gwapiv1.HTTPRouteRule{{
				BackendRefs: []gwapiv1.HTTPBackendRef{{
					BackendRef: gwapiv1.BackendRef{
						BackendObjectReference: gwapiv1.BackendObjectReference{
							Name: gwapiv1.ObjectName("svc-" + name),
							Port: &port,
						},
					},
				}},
			}},
		},
	}
}

func TestApplyOriginPolicies_OnlyTargetedRoute(t *testing.T) {
	routes := []gwapiv1.HTTPRoute{
		httpRouteWithBackend("route-untargeted", "a.example.com"),
		httpRouteWithBackend("route-targeted", "b.example.com"),
	}
	rules := cfclient.BuildIngressRules(routes)
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rules))
	}

	policies := []cfv1alpha1.CloudflareOriginPolicy{
		makeOriginPolicy("p", "default", "HTTPRoute", "route-targeted",
			cfv1alpha1.CloudflareOriginPolicySpec{ProxyType: ptr("socks")}),
	}
	applyOriginPolicies(policies, "gw", rules)

	if rules[0].OriginRequest != nil {
		t.Errorf("untargeted route should have nil originRequest, got %+v", rules[0].OriginRequest)
	}
	if rules[1].OriginRequest == nil || rules[1].OriginRequest.ProxyType == nil || *rules[1].OriginRequest.ProxyType != "socks" {
		t.Errorf("expected proxyType 'socks' on targeted route, got %+v", rules[1].OriginRequest)
	}
}

func TestApplyOriginPolicies_MultiHostname(t *testing.T) {
	routes := []gwapiv1.HTTPRoute{httpRouteWithBackend("multi-host", "a.example.com", "b.example.com")}
	rules := cfclient.BuildIngressRules(routes)
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules (one per hostname), got %d", len(rules))
	}

	policies := []cfv1alpha1.CloudflareOriginPolicy{
		makeOriginPolicy("p", "default", "HTTPRoute", "multi-host",
			cfv1alpha1.CloudflareOriginPolicySpec{DisableChunkedEncoding: ptr(true)}),
	}
	applyOriginPolicies(policies, "gw", rules)

	for i, rule := range rules {
		if rule.OriginRequest == nil || rule.OriginRequest.DisableChunkedEncoding == nil || !*rule.OriginRequest.DisableChunkedEncoding {
			t.Errorf("rule %d: expected disableChunkedEncoding=true, got %+v", i, rule.OriginRequest)
		}
	}
}

// Inherited precedence: a Gateway-level policy is the default; a route-level
// policy overrides it for that route.
func TestApplyOriginPolicies_InheritedPrecedence(t *testing.T) {
	routes := []gwapiv1.HTTPRoute{httpRouteWithBackend("route", "a.example.com")}
	rules := cfclient.BuildIngressRules(routes)

	policies := []cfv1alpha1.CloudflareOriginPolicy{
		makeOriginPolicy("gw-default", "default", "Gateway", "gw",
			cfv1alpha1.CloudflareOriginPolicySpec{
				NoHappyEyeballs:        ptr(true),
				DisableChunkedEncoding: ptr(true),
			}),
		makeOriginPolicy("route-override", "default", "HTTPRoute", "route",
			cfv1alpha1.CloudflareOriginPolicySpec{NoHappyEyeballs: ptr(false)}),
	}
	applyOriginPolicies(policies, "gw", rules)

	or := rules[0].OriginRequest
	if or == nil {
		t.Fatal("expected originRequest")
	}
	// Route-level wins for noHappyEyeballs.
	if or.NoHappyEyeballs == nil || *or.NoHappyEyeballs {
		t.Errorf("route-level noHappyEyeballs=false should win, got %v", or.NoHappyEyeballs)
	}
	// Gateway-level default inherited where the route is silent.
	if or.DisableChunkedEncoding == nil || !*or.DisableChunkedEncoding {
		t.Errorf("expected gateway-level disableChunkedEncoding=true inherited, got %v", or.DisableChunkedEncoding)
	}
}
