package controller

import (
	"context"
	"fmt"
	"sync"
	"testing"

	cf "github.com/cloudflare/cloudflare-go"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
	args   []interface{}
}

type mockCloudflareClient struct {
	mu             sync.Mutex
	calls          []mockCall
	existingTunnel *cf.Tunnel
	accountID      string
	createErr      error
	deleteErr      error
	configErr      error
}

func newMockClient() *mockCloudflareClient {
	return &mockCloudflareClient{accountID: "test-account"}
}

func (m *mockCloudflareClient) withExistingTunnel(id, name string) *mockCloudflareClient {
	m.existingTunnel = &cf.Tunnel{ID: id, Name: name}
	return m
}

func (m *mockCloudflareClient) record(method string, args ...interface{}) {
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

func (m *mockCloudflareClient) CreateTunnel(ctx context.Context, name string, secret []byte) (cf.Tunnel, error) {
	m.record("CreateTunnel", name)
	if m.createErr != nil {
		return cf.Tunnel{}, m.createErr
	}
	return cf.Tunnel{ID: "mock-tunnel-id", Name: name}, nil
}

func (m *mockCloudflareClient) GetTunnelByName(ctx context.Context, name string) (*cf.Tunnel, error) {
	m.record("GetTunnelByName", name)
	if m.existingTunnel != nil && m.existingTunnel.Name == name {
		return m.existingTunnel, nil
	}
	return nil, nil
}

func (m *mockCloudflareClient) DeleteTunnel(ctx context.Context, id string) error {
	m.record("DeleteTunnel", id)
	return m.deleteErr
}

func (m *mockCloudflareClient) UpdateTunnelConfiguration(ctx context.Context, tunnelID string, ingress []cf.UnvalidatedIngressRule) error {
	m.record("UpdateTunnelConfiguration", tunnelID, len(ingress))
	return m.configErr
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	clientgoscheme.AddToScheme(s)
	gwapiv1.AddToScheme(s)
	gwapiv1alpha2.AddToScheme(s)
	gwapiv1beta1.AddToScheme(s)
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

	r := &tunnelReconciler{
		client:         c,
		cloudflare:     mock,
		controllerName: gwapiv1.GatewayController(ControllerName),
	}

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-gw", Namespace: "default"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Requeue {
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

	r := &tunnelReconciler{
		client:         c,
		cloudflare:     mock,
		controllerName: gwapiv1.GatewayController(ControllerName),
	}

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-gw", Namespace: "default"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Requeue {
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

	r := &tunnelReconciler{
		client:         c,
		cloudflare:     mock,
		controllerName: gwapiv1.GatewayController(ControllerName),
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

	r := &tunnelReconciler{
		client:         c,
		cloudflare:     mock,
		controllerName: gwapiv1.GatewayController(ControllerName),
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

	r := &tunnelReconciler{
		client:         c,
		cloudflare:     mock,
		controllerName: gwapiv1.GatewayController(ControllerName),
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

	r := &tunnelReconciler{
		client:         c,
		cloudflare:     mock,
		controllerName: gwapiv1.GatewayController(ControllerName),
	}

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "nonexistent", Namespace: "default"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Requeue {
		t.Error("should not requeue for deleted gateway")
	}
}

func TestCleanup_BestEffort(t *testing.T) {
	scheme := testScheme()
	gw := makeGateway("test-gw", "default")

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(gw).Build()
	mock := newMockClient().withExistingTunnel("tunnel-id", "test-gw")

	r := &tunnelReconciler{
		client:         c,
		cloudflare:     mock,
		controllerName: gwapiv1.GatewayController(ControllerName),
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

	r := &tunnelReconciler{
		client:         c,
		cloudflare:     mock,
		controllerName: gwapiv1.GatewayController(ControllerName),
	}

	// Reconcile should return nil error (permanent error is swallowed)
	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-gw", Namespace: "default"},
	})

	if err != nil {
		t.Errorf("permanent error should not be returned, got: %v", err)
	}
	if result.Requeue {
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

	r := &tunnelReconciler{
		client:         c,
		cloudflare:     mock,
		controllerName: gwapiv1.GatewayController(ControllerName),
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
