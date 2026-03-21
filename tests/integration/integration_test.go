package integration

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	cf "github.com/cloudflare/cloudflare-go"

	ctrl "github.com/mccormickt/cloudflare-tunnel-controller/internal/cloudflare"
	controller "github.com/mccormickt/cloudflare-tunnel-controller/internal/controller"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
	gwapiv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"
)

var (
	testEnv   *envtest.Environment
	k8sClient client.Client
)

func TestMain(m *testing.M) {
	// Find Gateway API CRDs from the module cache
	gwAPICRDs := gatewayAPICRDPath()

	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{gwAPICRDs},
	}

	cfg, err := testEnv.Start()
	if err != nil {
		panic("failed to start envtest: " + err.Error())
	}

	gwapiv1.AddToScheme(scheme.Scheme)
	gwapiv1alpha2.AddToScheme(scheme.Scheme)
	gwapiv1beta1.AddToScheme(scheme.Scheme)

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		panic("failed to create client: " + err.Error())
	}

	code := m.Run()
	testEnv.Stop()
	os.Exit(code)
}

func gatewayAPICRDPath() string {
	// The CRDs are in the gateway-api module's config/crd/experimental directory
	// (experimental includes TLSRoute)
	gomod := os.Getenv("GOMODCACHE")
	if gomod == "" {
		gomod = filepath.Join(os.Getenv("HOME"), "go", "pkg", "mod")
	}
	return filepath.Join(gomod, "sigs.k8s.io", "gateway-api@v1.4.1", "config", "crd", "experimental")
}

// ---------------------------------------------------------------------------
// Mock Cloudflare client (same pattern as unit tests)
// ---------------------------------------------------------------------------

type mockCall struct {
	method string
	args   []any
}

type mockCloudflareClient struct {
	mu             sync.Mutex
	calls          []mockCall
	existingTunnel *cf.Tunnel
	accountID      string
}

func newMockClient() *mockCloudflareClient {
	return &mockCloudflareClient{accountID: "test-account"}
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

func (m *mockCloudflareClient) CreateTunnel(ctx context.Context, name string, secret []byte) (cf.Tunnel, error) {
	m.record("CreateTunnel", name)
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
	return nil
}

func (m *mockCloudflareClient) UpdateTunnelConfiguration(ctx context.Context, tunnelID string, ingress []cf.UnvalidatedIngressRule) error {
	m.record("UpdateTunnelConfiguration", tunnelID, len(ingress))
	return nil
}

var _ ctrl.APIClient = &mockCloudflareClient{}

// ---------------------------------------------------------------------------
// Integration tests
// ---------------------------------------------------------------------------

func TestIntegration_GatewayClassAndGateway(t *testing.T) {
	ctx := context.Background()

	// Create GatewayClass
	gc := &gwapiv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "integration-test-class",
		},
		Spec: gwapiv1.GatewayClassSpec{
			ControllerName: gwapiv1.GatewayController(controller.ControllerName),
		},
	}
	if err := k8sClient.Create(ctx, gc); err != nil {
		t.Fatalf("failed to create GatewayClass: %v", err)
	}
	t.Cleanup(func() { k8sClient.Delete(ctx, gc) })

	// Verify GatewayClass exists via API
	var fetched gwapiv1.GatewayClass
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: gc.Name}, &fetched); err != nil {
		t.Fatalf("failed to get GatewayClass: %v", err)
	}
	if fetched.Spec.ControllerName != gwapiv1.GatewayController(controller.ControllerName) {
		t.Errorf("controller name mismatch: got %s", fetched.Spec.ControllerName)
	}

	// Create Gateway
	from := gwapiv1.NamespacesFromSame
	gw := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "integration-test-gw",
			Namespace: "default",
		},
		Spec: gwapiv1.GatewaySpec{
			GatewayClassName: "integration-test-class",
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
	if err := k8sClient.Create(ctx, gw); err != nil {
		t.Fatalf("failed to create Gateway: %v", err)
	}
	t.Cleanup(func() { k8sClient.Delete(ctx, gw) })

	// Verify Gateway exists
	var fetchedGW gwapiv1.Gateway
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: gw.Name, Namespace: gw.Namespace}, &fetchedGW); err != nil {
		t.Fatalf("failed to get Gateway: %v", err)
	}
	if fetchedGW.Spec.GatewayClassName != "integration-test-class" {
		t.Errorf("GatewayClassName mismatch: got %s", fetchedGW.Spec.GatewayClassName)
	}
}

func TestIntegration_HTTPRouteAttachment(t *testing.T) {
	ctx := context.Background()

	// Create GatewayClass + Gateway
	gc := &gwapiv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "attach-test-class"},
		Spec:       gwapiv1.GatewayClassSpec{ControllerName: gwapiv1.GatewayController(controller.ControllerName)},
	}
	if err := k8sClient.Create(ctx, gc); err != nil {
		t.Fatalf("failed to create GatewayClass: %v", err)
	}
	t.Cleanup(func() { k8sClient.Delete(ctx, gc) })

	from := gwapiv1.NamespacesFromSame
	gw := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "attach-test-gw", Namespace: "default"},
		Spec: gwapiv1.GatewaySpec{
			GatewayClassName: "attach-test-class",
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
	if err := k8sClient.Create(ctx, gw); err != nil {
		t.Fatalf("failed to create Gateway: %v", err)
	}
	t.Cleanup(func() { k8sClient.Delete(ctx, gw) })

	// Create HTTPRoute referencing the Gateway
	gwGroup := gwapiv1.Group(gwapiv1.GroupName)
	gwKind := gwapiv1.Kind("Gateway")
	port := gwapiv1.PortNumber(80)

	route := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "attach-test-route", Namespace: "default"},
		Spec: gwapiv1.HTTPRouteSpec{
			CommonRouteSpec: gwapiv1.CommonRouteSpec{
				ParentRefs: []gwapiv1.ParentReference{{
					Group: &gwGroup,
					Kind:  &gwKind,
					Name:  gwapiv1.ObjectName(gw.Name),
				}},
			},
			Hostnames: []gwapiv1.Hostname{"test.example.com"},
			Rules: []gwapiv1.HTTPRouteRule{{
				BackendRefs: []gwapiv1.HTTPBackendRef{{
					BackendRef: gwapiv1.BackendRef{
						BackendObjectReference: gwapiv1.BackendObjectReference{
							Name: "test-svc",
							Port: &port,
						},
					},
				}},
			}},
		},
	}
	if err := k8sClient.Create(ctx, route); err != nil {
		t.Fatalf("failed to create HTTPRoute: %v", err)
	}
	t.Cleanup(func() { k8sClient.Delete(ctx, route) })

	// Verify the route is attachable
	var fetchedGW gwapiv1.Gateway
	k8sClient.Get(ctx, types.NamespacedName{Name: gw.Name, Namespace: gw.Namespace}, &fetchedGW)

	allowed, err := controller.CheckRouteAttachment(ctx, k8sClient, &fetchedGW, "default", "HTTPRoute")
	if err != nil {
		t.Fatalf("CheckRouteAttachment error: %v", err)
	}
	if !allowed {
		t.Error("HTTPRoute in same namespace should be attachable")
	}
}

func TestIntegration_ReferenceGrant(t *testing.T) {
	ctx := context.Background()

	// Create the target namespace
	ns := &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "refgrant-backend"}}
	if err := k8sClient.Create(ctx, ns); err != nil {
		t.Fatalf("failed to create namespace: %v", err)
	}
	t.Cleanup(func() { k8sClient.Delete(ctx, ns) })

	// Create ReferenceGrant in the backend namespace
	grant := &gwapiv1beta1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "allow-frontend-routes",
			Namespace: "refgrant-backend",
		},
		Spec: gwapiv1beta1.ReferenceGrantSpec{
			From: []gwapiv1beta1.ReferenceGrantFrom{{
				Group:     "gateway.networking.k8s.io",
				Kind:      "HTTPRoute",
				Namespace: "default",
			}},
			To: []gwapiv1beta1.ReferenceGrantTo{{
				Group: "",
				Kind:  "Service",
			}},
		},
	}
	if err := k8sClient.Create(ctx, grant); err != nil {
		t.Fatalf("failed to create ReferenceGrant: %v", err)
	}
	t.Cleanup(func() { k8sClient.Delete(ctx, grant) })

	// Check: allowed reference
	allowed, err := controller.CheckReferenceGrant(ctx, k8sClient, "default", "HTTPRoute", "refgrant-backend", "Service", "api-svc")
	if err != nil {
		t.Fatalf("CheckReferenceGrant error: %v", err)
	}
	if !allowed {
		t.Error("ReferenceGrant should allow cross-namespace reference from default to refgrant-backend")
	}

	// Check: denied reference (wrong source namespace)
	allowed, err = controller.CheckReferenceGrant(ctx, k8sClient, "other-ns", "HTTPRoute", "refgrant-backend", "Service", "api-svc")
	if err != nil {
		t.Fatalf("CheckReferenceGrant error: %v", err)
	}
	if allowed {
		t.Error("ReferenceGrant should deny cross-namespace reference from other-ns")
	}
}

func TestIntegration_NamespaceSelectorAttachment(t *testing.T) {
	ctx := context.Background()

	// Create a labeled namespace
	ns := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "team-alpha",
			Labels: map[string]string{"team": "alpha", "env": "staging"},
		},
	}
	if err := k8sClient.Create(ctx, ns); err != nil {
		t.Fatalf("failed to create namespace: %v", err)
	}
	t.Cleanup(func() { k8sClient.Delete(ctx, ns) })

	from := gwapiv1.NamespacesFromSelector
	gw := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "selector-test-gw", Namespace: "default"},
		Spec: gwapiv1.GatewaySpec{
			GatewayClassName: "integration-test-class",
			Listeners: []gwapiv1.Listener{{
				Name:     "http",
				Port:     80,
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

	// Use the Gateway directly (don't need to persist it for attachment checking)
	allowed, err := controller.CheckRouteAttachment(ctx, k8sClient, gw, "team-alpha", "HTTPRoute")
	if err != nil {
		t.Fatalf("CheckRouteAttachment error: %v", err)
	}
	if !allowed {
		t.Error("namespace with matching labels should be allowed")
	}

	// Create a non-matching namespace
	ns2 := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "team-beta",
			Labels: map[string]string{"team": "beta"},
		},
	}
	if err := k8sClient.Create(ctx, ns2); err != nil {
		t.Fatalf("failed to create namespace: %v", err)
	}
	t.Cleanup(func() { k8sClient.Delete(ctx, ns2) })

	allowed, err = controller.CheckRouteAttachment(ctx, k8sClient, gw, "team-beta", "HTTPRoute")
	if err != nil {
		t.Fatalf("CheckRouteAttachment error: %v", err)
	}
	if allowed {
		t.Error("namespace with non-matching labels should not be allowed")
	}
}
