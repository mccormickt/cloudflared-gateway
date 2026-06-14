package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	cfv1alpha1 "github.com/mccormickt/cloudflared-gateway/api/v1alpha1"
	cfclient "github.com/mccormickt/cloudflared-gateway/internal/cloudflare"
	controller "github.com/mccormickt/cloudflared-gateway/internal/controller"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
	gwapiv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"
	apisxv1alpha1 "sigs.k8s.io/gateway-api/apisx/v1alpha1"
)

var (
	testEnv    *envtest.Environment
	testCfg    *rest.Config
	testScheme = runtime.NewScheme()
	k8sClient  client.Client
)

func TestMain(m *testing.M) {
	// Register schemes
	utilruntime.Must(clientgoscheme.AddToScheme(testScheme))
	utilruntime.Must(gwapiv1.Install(testScheme))
	utilruntime.Must(gwapiv1alpha2.Install(testScheme))
	utilruntime.Must(gwapiv1beta1.Install(testScheme))
	utilruntime.Must(apisxv1alpha1.Install(testScheme))
	utilruntime.Must(cfv1alpha1.AddToScheme(testScheme))

	// Find Gateway API CRDs and custom CRDs
	gwAPICRDs := gatewayAPICRDPath()
	customCRDs := filepath.Join(projectRoot(), "config", "crd")

	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{gwAPICRDs, customCRDs},
	}

	var err error
	testCfg, err = testEnv.Start()
	if err != nil {
		panic("failed to start envtest: " + err.Error())
	}

	k8sClient, err = client.New(testCfg, client.Options{Scheme: testScheme})
	if err != nil {
		panic("failed to create client: " + err.Error())
	}

	code := m.Run()
	if err := testEnv.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to stop envtest: %v\n", err)
	}
	os.Exit(code)
}

func gatewayAPICRDPath() string {
	out, err := exec.CommandContext(context.Background(), "go", "list", "-m", "-f", "{{.Dir}}", "sigs.k8s.io/gateway-api").Output()
	if err != nil {
		panic("failed to find gateway-api module directory: " + err.Error())
	}
	return filepath.Join(strings.TrimSpace(string(out)), "config", "crd", "experimental")
}

func projectRoot() string {
	// Walk up from tests/integration/ to repo root
	dir, err := os.Getwd()
	if err != nil {
		panic("failed to get working directory: " + err.Error())
	}
	return filepath.Join(dir, "..", "..")
}

// ---------------------------------------------------------------------------
// Mock Cloudflare client for integration tests
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
	lastIngress    []cfclient.IngressRule
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

func (m *mockCloudflareClient) hasCall(method string) bool {
	for _, c := range m.getCalls() {
		if c.method == method {
			return true
		}
	}
	return false
}

func (m *mockCloudflareClient) AccountID() string { return m.accountID }

func (m *mockCloudflareClient) CreateTunnel(_ context.Context, name string, _ []byte) (cfclient.Tunnel, error) {
	m.record("CreateTunnel", name)
	return cfclient.Tunnel{ID: "mock-tunnel-id", Name: name}, nil
}

func (m *mockCloudflareClient) GetTunnelByName(_ context.Context, name string) (cfclient.Tunnel, error) {
	m.record("GetTunnelByName", name)
	m.mu.Lock()
	existing := m.existingTunnel
	m.mu.Unlock()
	if existing != nil && existing.Name == name {
		return *existing, nil
	}
	return cfclient.Tunnel{}, cfclient.ErrTunnelNotFound
}

func (m *mockCloudflareClient) DeleteTunnel(_ context.Context, id string) error {
	m.record("DeleteTunnel", id)
	return nil
}

func (m *mockCloudflareClient) UpdateTunnelConfiguration(_ context.Context, tunnelID string, ingress []cfclient.IngressRule) error {
	m.mu.Lock()
	m.lastIngress = append([]cfclient.IngressRule(nil), ingress...)
	m.mu.Unlock()
	m.record("UpdateTunnelConfiguration", tunnelID, len(ingress))
	return nil
}

func (m *mockCloudflareClient) getLastIngress() []cfclient.IngressRule {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]cfclient.IngressRule(nil), m.lastIngress...)
}

// ---------------------------------------------------------------------------
// Helper: start a manager with the reconciler
// ---------------------------------------------------------------------------

func startManager(t *testing.T, mockCF cfclient.APIClient) {
	t.Helper()

	mgr, err := ctrl.NewManager(testCfg, ctrl.Options{
		Scheme: testScheme,
		// Disable the metrics server: tests don't scrape it and the default
		// :8080 bind can collide with other local listeners.
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	reconciler := &controller.GatewayReconciler{
		CloudflareClient: mockCF,
		ControllerName:   gwapiv1.GatewayController(controller.ControllerName),
		// Enabled for the integration manager so XBackend subtests exercise the
		// real reconcile loop. Harmless for Service-only subtests, which never
		// reference an XBackend.
		ExperimentalBackends: true,
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		t.Fatalf("failed to setup controller: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- mgr.Start(ctx)
	}()

	t.Cleanup(func() {
		cancel()
		if err := <-errCh; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("manager exited with unexpected error: %v", err)
		}
	})

	// Wait for cache sync
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		t.Fatal("cache failed to sync")
	}
}

// ---------------------------------------------------------------------------
// Helper: factories
// ---------------------------------------------------------------------------

func makeGatewayClass(name string) *gwapiv1.GatewayClass {
	return &gwapiv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: gwapiv1.GatewayClassSpec{
			ControllerName: gwapiv1.GatewayController(controller.ControllerName),
		},
	}
}

func makeGateway(name, namespace, className string) *gwapiv1.Gateway {
	from := gwapiv1.NamespacesFromSame
	return &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: gwapiv1.GatewaySpec{
			GatewayClassName: gwapiv1.ObjectName(className),
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
	port := gwapiv1.PortNumber(80)

	return &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: gwapiv1.HTTPRouteSpec{
			CommonRouteSpec: gwapiv1.CommonRouteSpec{
				ParentRefs: []gwapiv1.ParentReference{{
					Group: &gwGroup,
					Kind:  &gwKind,
					Name:  gwapiv1.ObjectName(gwName),
				}},
			},
			Hostnames: []gwapiv1.Hostname{"test.example.com"},
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
// Existing integration tests (API interaction validation)
// ---------------------------------------------------------------------------

func TestIntegration_GatewayClassAndGateway(t *testing.T) {
	ctx := context.Background()

	gc := makeGatewayClass("integ-gc-lifecycle")
	if err := k8sClient.Create(ctx, gc); err != nil {
		t.Fatalf("failed to create GatewayClass: %v", err)
	}
	t.Cleanup(func() { k8sClient.Delete(ctx, gc) })

	var fetched gwapiv1.GatewayClass
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: gc.Name}, &fetched); err != nil {
		t.Fatalf("failed to get GatewayClass: %v", err)
	}
	if fetched.Spec.ControllerName != gwapiv1.GatewayController(controller.ControllerName) {
		t.Errorf("controller name mismatch: got %s", fetched.Spec.ControllerName)
	}

	gw := makeGateway("integ-gw-lifecycle", "default", gc.Name)
	if err := k8sClient.Create(ctx, gw); err != nil {
		t.Fatalf("failed to create Gateway: %v", err)
	}
	t.Cleanup(func() { k8sClient.Delete(ctx, gw) })

	var fetchedGW gwapiv1.Gateway
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: gw.Name, Namespace: gw.Namespace}, &fetchedGW); err != nil {
		t.Fatalf("failed to get Gateway: %v", err)
	}
	if string(fetchedGW.Spec.GatewayClassName) != gc.Name {
		t.Errorf("GatewayClassName mismatch: got %s", fetchedGW.Spec.GatewayClassName)
	}
}

func TestIntegration_HTTPRouteAttachment(t *testing.T) {
	ctx := context.Background()

	gc := makeGatewayClass("integ-gc-attach")
	if err := k8sClient.Create(ctx, gc); err != nil {
		t.Fatalf("failed to create GatewayClass: %v", err)
	}
	t.Cleanup(func() { k8sClient.Delete(ctx, gc) })

	gw := makeGateway("integ-gw-attach", "default", gc.Name)
	if err := k8sClient.Create(ctx, gw); err != nil {
		t.Fatalf("failed to create Gateway: %v", err)
	}
	t.Cleanup(func() { k8sClient.Delete(ctx, gw) })

	route := makeHTTPRoute("integ-route-attach", "default", gw.Name)
	if err := k8sClient.Create(ctx, route); err != nil {
		t.Fatalf("failed to create HTTPRoute: %v", err)
	}
	t.Cleanup(func() { k8sClient.Delete(ctx, route) })

	var fetchedGW gwapiv1.Gateway
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: gw.Name, Namespace: gw.Namespace}, &fetchedGW); err != nil {
		t.Fatalf("failed to get Gateway: %v", err)
	}

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

	ns := &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "refgrant-backend"}}
	if err := k8sClient.Create(ctx, ns); err != nil {
		t.Fatalf("failed to create namespace: %v", err)
	}
	t.Cleanup(func() { k8sClient.Delete(ctx, ns) })

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

	allowed, err := controller.CheckReferenceGrant(ctx, k8sClient, "default", "HTTPRoute", "refgrant-backend", "Service", "api-svc")
	if err != nil {
		t.Fatalf("CheckReferenceGrant error: %v", err)
	}
	if !allowed {
		t.Error("ReferenceGrant should allow cross-namespace reference from default to refgrant-backend")
	}

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

	allowed, err := controller.CheckRouteAttachment(ctx, k8sClient, gw, "team-alpha", "HTTPRoute")
	if err != nil {
		t.Fatalf("CheckRouteAttachment error: %v", err)
	}
	if !allowed {
		t.Error("namespace with matching labels should be allowed")
	}

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

// ---------------------------------------------------------------------------
// Full controller loop tests (manager + reconciler against envtest)
// ---------------------------------------------------------------------------

// TestIntegration_ControllerLoop starts a single manager with the reconciler
// and runs subtests against it. Subtests share the manager to avoid controller
// name conflicts (controller-runtime requires unique names per process).
func TestIntegration_ControllerLoop(t *testing.T) {
	ctx := context.Background()
	mock := newMockClient()
	startManager(t, mock)

	gc := makeGatewayClass("integ-gc-loop")
	if err := k8sClient.Create(ctx, gc); err != nil {
		t.Fatalf("failed to create GatewayClass: %v", err)
	}
	t.Cleanup(func() { k8sClient.Delete(ctx, gc) })

	t.Run("CreatesResources", func(t *testing.T) {
		gw := makeGateway("integ-gw-creates", "default", gc.Name)
		if err := k8sClient.Create(ctx, gw); err != nil {
			t.Fatalf("failed to create Gateway: %v", err)
		}
		t.Cleanup(func() { k8sClient.Delete(ctx, gw) })

		route := makeHTTPRoute("integ-route-creates", "default", gw.Name)
		if err := k8sClient.Create(ctx, route); err != nil {
			t.Fatalf("failed to create HTTPRoute: %v", err)
		}
		t.Cleanup(func() { k8sClient.Delete(ctx, route) })

		// Wait for the finalizer to be added (proves reconciler ran)
		requireEventually(t, func() bool {
			var fetched gwapiv1.Gateway
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: gw.Name, Namespace: gw.Namespace}, &fetched); err != nil {
				return false
			}
			return controllerutil.ContainsFinalizer(&fetched, "cloudflared-gateway.jan0ski.net/cleanup")
		}, 10*time.Second, 100*time.Millisecond, "finalizer should be added to Gateway")

		// Verify tunnel was created via mock
		requireEventually(t, func() bool {
			return mock.hasCall("CreateTunnel")
		}, 10*time.Second, 100*time.Millisecond, "CreateTunnel should be called")

		// Verify config was pushed
		requireEventually(t, func() bool {
			return mock.hasCall("UpdateTunnelConfiguration")
		}, 10*time.Second, 100*time.Millisecond, "UpdateTunnelConfiguration should be called")

		// Verify tunnel secret was created
		requireEventually(t, func() bool {
			var secret v1.Secret
			return k8sClient.Get(ctx, types.NamespacedName{
				Name:      controller.TunnelSecretName(gw.Name),
				Namespace: gw.Namespace,
			}, &secret) == nil
		}, 10*time.Second, 100*time.Millisecond, "tunnel secret should be created")

		// Verify cloudflared Deployment was created
		requireEventually(t, func() bool {
			var deploy appsv1.Deployment
			return k8sClient.Get(ctx, types.NamespacedName{
				Name:      controller.DeploymentName(gw.Name),
				Namespace: gw.Namespace,
			}, &deploy) == nil
		}, 10*time.Second, 100*time.Millisecond, "cloudflared deployment should be created")

		// Verify ingress config was pushed with correct rule count (1 route + catch-all)
		for _, call := range mock.getCalls() {
			if call.method == "UpdateTunnelConfiguration" {
				ingressLen := call.args[1].(int)
				if ingressLen != 2 {
					t.Errorf("expected 2 ingress rules (1 route + catch-all), got %d", ingressLen)
				}
			}
		}
	})

	t.Run("OriginPolicyStatus", func(t *testing.T) {
		gw := makeGateway("integ-gw-origin", "default", gc.Name)
		if err := k8sClient.Create(ctx, gw); err != nil {
			t.Fatalf("failed to create Gateway: %v", err)
		}
		t.Cleanup(func() { k8sClient.Delete(ctx, gw) })

		route := makeHTTPRoute("integ-route-origin", "default", gw.Name)
		if err := k8sClient.Create(ctx, route); err != nil {
			t.Fatalf("failed to create HTTPRoute: %v", err)
		}
		t.Cleanup(func() { k8sClient.Delete(ctx, route) })

		proxyType := "socks"
		policy := &cfv1alpha1.CloudflareOriginPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "integ-origin-pol", Namespace: "default"},
			Spec: cfv1alpha1.CloudflareOriginPolicySpec{
				TargetRefs: []gwapiv1.LocalPolicyTargetReference{{
					Group: gwapiv1.GroupName,
					Kind:  "HTTPRoute",
					Name:  gwapiv1.ObjectName(route.Name),
				}},
				ProxyType: &proxyType,
			},
		}
		if err := k8sClient.Create(ctx, policy); err != nil {
			t.Fatalf("failed to create CloudflareOriginPolicy: %v", err)
		}
		t.Cleanup(func() { k8sClient.Delete(ctx, policy) })

		// Policy should get an ancestor Accepted=True condition.
		requireEventually(t, func() bool {
			var p cfv1alpha1.CloudflareOriginPolicy
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: policy.Name, Namespace: policy.Namespace}, &p); err != nil {
				return false
			}
			for _, a := range p.Status.Ancestors {
				for _, c := range a.Conditions {
					if c.Type == "Accepted" && c.Status == metav1.ConditionTrue {
						return true
					}
				}
			}
			return false
		}, 10*time.Second, 100*time.Millisecond, "origin policy should report Accepted ancestor status")

		// The targeted route should get the per-kind PolicyAffected condition
		// for the CloudflareOriginPolicy acting on it.
		requireEventually(t, func() bool {
			var rt gwapiv1.HTTPRoute
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: route.Name, Namespace: route.Namespace}, &rt); err != nil {
				return false
			}
			for _, p := range rt.Status.Parents {
				for _, c := range p.Conditions {
					if c.Type == "cloudflare.jan0ski.net/CloudflareOriginPolicyAffected" && c.Status == metav1.ConditionTrue {
						return true
					}
				}
			}
			return false
		}, 10*time.Second, 100*time.Millisecond, "targeted route should report CloudflareOriginPolicyAffected")
	})

	t.Run("RuleOrderingAndPolicyAttachment", func(t *testing.T) {
		gw := makeGateway("integ-gw-order", "default", gc.Name)
		if err := k8sClient.Create(ctx, gw); err != nil {
			t.Fatalf("failed to create Gateway: %v", err)
		}
		t.Cleanup(func() { k8sClient.Delete(ctx, gw) })

		gwGroup := gwapiv1.Group(gwapiv1.GroupName)
		gwKind := gwapiv1.Kind("Gateway")
		port := gwapiv1.PortNumber(80)
		exact := gwapiv1.PathMatchExact
		prefix := gwapiv1.PathMatchPathPrefix
		rootPath, apiHealth := "/", "/api/health"

		// One rule, two matches declared least-specific-first: a match-all prefix
		// then an exact path. After the precedence sort, the exact rule must come
		// first so Cloudflare's first-match evaluation serves /api/health to it.
		route := &gwapiv1.HTTPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "integ-route-order", Namespace: "default"},
			Spec: gwapiv1.HTTPRouteSpec{
				CommonRouteSpec: gwapiv1.CommonRouteSpec{
					ParentRefs: []gwapiv1.ParentReference{{Group: &gwGroup, Kind: &gwKind, Name: gwapiv1.ObjectName(gw.Name)}},
				},
				Hostnames: []gwapiv1.Hostname{"order.example.com"},
				Rules: []gwapiv1.HTTPRouteRule{{
					Matches: []gwapiv1.HTTPRouteMatch{
						{Path: &gwapiv1.HTTPPathMatch{Type: &prefix, Value: &rootPath}},
						{Path: &gwapiv1.HTTPPathMatch{Type: &exact, Value: &apiHealth}},
					},
					BackendRefs: []gwapiv1.HTTPBackendRef{{BackendRef: gwapiv1.BackendRef{
						BackendObjectReference: gwapiv1.BackendObjectReference{Name: "web-svc", Port: &port},
					}}},
				}},
			},
		}
		if err := k8sClient.Create(ctx, route); err != nil {
			t.Fatalf("failed to create HTTPRoute: %v", err)
		}
		t.Cleanup(func() { k8sClient.Delete(ctx, route) })

		socks := "socks"
		policy := &cfv1alpha1.CloudflareOriginPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "integ-origin-order", Namespace: "default"},
			Spec: cfv1alpha1.CloudflareOriginPolicySpec{
				TargetRefs: []gwapiv1.LocalPolicyTargetReference{{
					Group: gwapiv1.GroupName, Kind: "HTTPRoute", Name: gwapiv1.ObjectName(route.Name),
				}},
				ProxyType: &socks,
			},
		}
		if err := k8sClient.Create(ctx, policy); err != nil {
			t.Fatalf("failed to create CloudflareOriginPolicy: %v", err)
		}
		t.Cleanup(func() { k8sClient.Delete(ctx, policy) })

		// Wait until the pushed config reflects this route's exact rule.
		requireEventually(t, func() bool {
			for _, r := range mock.getLastIngress() {
				if r.Hostname == "order.example.com" && r.Path == "^/api/health$" {
					return true
				}
			}
			return false
		}, 10*time.Second, 100*time.Millisecond, "config should include the exact /api/health rule")

		ing := mock.getLastIngress()
		exactIdx, matchAllIdx := -1, -1
		for i, r := range ing {
			if r.Hostname != "order.example.com" {
				continue
			}
			switch r.Path {
			case "^/api/health$":
				exactIdx = i
			case "":
				matchAllIdx = i
			}
		}
		if exactIdx == -1 || matchAllIdx == -1 {
			t.Fatalf("expected both exact and match-all rules for the route, got %+v", ing)
		}
		if exactIdx > matchAllIdx {
			t.Errorf("exact rule (idx %d) must precede match-all (idx %d) after precedence sort", exactIdx, matchAllIdx)
		}

		// Identity-based policy attachment must survive the sort: every rule of
		// the route carries the origin policy's proxyType.
		for _, r := range ing {
			if r.Hostname != "order.example.com" {
				continue
			}
			if r.OriginRequest == nil || r.OriginRequest.ProxyType == nil || *r.OriginRequest.ProxyType != "socks" {
				t.Errorf("rule %q should carry origin policy proxyType=socks, got %+v", r.Path, r.OriginRequest)
			}
		}
	})

	t.Run("UnsupportedMatchCondition", func(t *testing.T) {
		gw := makeGateway("integ-gw-unsupported", "default", gc.Name)
		if err := k8sClient.Create(ctx, gw); err != nil {
			t.Fatalf("failed to create Gateway: %v", err)
		}
		t.Cleanup(func() { k8sClient.Delete(ctx, gw) })

		gwGroup := gwapiv1.Group(gwapiv1.GroupName)
		gwKind := gwapiv1.Kind("Gateway")
		port := gwapiv1.PortNumber(80)

		// A header match is a dimension Cloudflare can't enforce.
		route := &gwapiv1.HTTPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "integ-route-unsupported", Namespace: "default"},
			Spec: gwapiv1.HTTPRouteSpec{
				CommonRouteSpec: gwapiv1.CommonRouteSpec{
					ParentRefs: []gwapiv1.ParentReference{{Group: &gwGroup, Kind: &gwKind, Name: gwapiv1.ObjectName(gw.Name)}},
				},
				Hostnames: []gwapiv1.Hostname{"canary.example.com"},
				Rules: []gwapiv1.HTTPRouteRule{{
					Matches: []gwapiv1.HTTPRouteMatch{{
						Headers: []gwapiv1.HTTPHeaderMatch{{Name: gwapiv1.HTTPHeaderName("X-Canary"), Value: "yes"}},
					}},
					BackendRefs: []gwapiv1.HTTPBackendRef{{BackendRef: gwapiv1.BackendRef{
						BackendObjectReference: gwapiv1.BackendObjectReference{Name: "web-svc", Port: &port},
					}}},
				}},
			},
		}
		if err := k8sClient.Create(ctx, route); err != nil {
			t.Fatalf("failed to create HTTPRoute: %v", err)
		}
		t.Cleanup(func() { k8sClient.Delete(ctx, route) })

		requireEventually(t, func() bool {
			var rt gwapiv1.HTTPRoute
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: route.Name, Namespace: "default"}, &rt); err != nil {
				return false
			}
			for _, p := range rt.Status.Parents {
				for _, c := range p.Conditions {
					if c.Type == string(gwapiv1.RouteConditionPartiallyInvalid) && c.Status == metav1.ConditionTrue {
						return true
					}
				}
			}
			return false
		}, 10*time.Second, 100*time.Millisecond, "route with a header match should report PartiallyInvalid")
	})

	t.Run("XBackendExternalOrigin", func(t *testing.T) {
		gw := makeGateway("integ-gw-xbackend", "default", gc.Name)
		if err := k8sClient.Create(ctx, gw); err != nil {
			t.Fatalf("failed to create Gateway: %v", err)
		}
		t.Cleanup(func() { k8sClient.Delete(ctx, gw) })

		systemCA := gwapiv1.WellKnownCACertificatesSystem
		xb := &apisxv1alpha1.XBackend{
			ObjectMeta: metav1.ObjectMeta{Name: "integ-openai", Namespace: "default"},
			Spec: apisxv1alpha1.BackendSpec{
				Type:             apisxv1alpha1.BackendTypeExternalHostname,
				Port:             apisxv1alpha1.BackendPort{Port: 443},
				ExternalHostname: &apisxv1alpha1.ExternalHostnameBackend{Hostname: "api.example.com"},
				TLS: &apisxv1alpha1.BackendTLS{
					Mode: apisxv1alpha1.BackendTLSModeServerOnly,
					Validation: gwapiv1.BackendTLSPolicyValidation{
						Hostname:                "api.example.com",
						WellKnownCACertificates: &systemCA,
					},
				},
			},
		}
		if err := k8sClient.Create(ctx, xb); err != nil {
			t.Fatalf("failed to create XBackend: %v", err)
		}
		t.Cleanup(func() { k8sClient.Delete(ctx, xb) })

		xbGroup := gwapiv1.Group(apisxv1alpha1.GroupName)
		xbKind := gwapiv1.Kind("XBackend")
		xbPort := gwapiv1.PortNumber(443)
		route := makeHTTPRoute("integ-route-xbackend", "default", gw.Name)
		route.Spec.Hostnames = []gwapiv1.Hostname{"proxy.example.com"}
		route.Spec.Rules = []gwapiv1.HTTPRouteRule{{
			BackendRefs: []gwapiv1.HTTPBackendRef{{
				BackendRef: gwapiv1.BackendRef{
					BackendObjectReference: gwapiv1.BackendObjectReference{
						Group: &xbGroup,
						Kind:  &xbKind,
						Name:  "integ-openai",
						Port:  &xbPort,
					},
				},
			}},
		}}
		if err := k8sClient.Create(ctx, route); err != nil {
			t.Fatalf("failed to create HTTPRoute: %v", err)
		}
		t.Cleanup(func() { k8sClient.Delete(ctx, route) })

		// The pushed config must route to the external HTTPS origin with the
		// validation hostname as the SNI server name and origin verification on.
		requireEventually(t, func() bool {
			for _, r := range mock.getLastIngress() {
				if r.Hostname == "proxy.example.com" && r.Service == "https://api.example.com:443" {
					return r.OriginRequest != nil &&
						r.OriginRequest.OriginServerName != nil &&
						*r.OriginRequest.OriginServerName == "api.example.com" &&
						r.OriginRequest.NoTLSVerify == nil
				}
			}
			return false
		}, 10*time.Second, 100*time.Millisecond, "config should route to the external XBackend origin over HTTPS")

		// The route must report ResolvedRefs=True.
		requireEventually(t, func() bool {
			var fetched gwapiv1.HTTPRoute
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: route.Name, Namespace: route.Namespace}, &fetched); err != nil {
				return false
			}
			for _, p := range fetched.Status.Parents {
				for _, c := range p.Conditions {
					if c.Type == string(gwapiv1.RouteConditionResolvedRefs) {
						return c.Status == metav1.ConditionTrue && c.Reason == string(gwapiv1.RouteReasonResolvedRefs)
					}
				}
			}
			return false
		}, 10*time.Second, 100*time.Millisecond, "HTTPRoute should report ResolvedRefs=True")

		// The XBackend must carry an Accepted ancestor condition for this Gateway.
		requireEventually(t, func() bool {
			var fetched apisxv1alpha1.XBackend
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: xb.Name, Namespace: xb.Namespace}, &fetched); err != nil {
				return false
			}
			for _, a := range fetched.Status.Ancestors {
				if string(a.AncestorRef.Name) != gw.Name {
					continue
				}
				for _, c := range a.Conditions {
					if c.Type == "Accepted" {
						return c.Status == metav1.ConditionTrue
					}
				}
			}
			return false
		}, 10*time.Second, 100*time.Millisecond, "XBackend should report Accepted ancestor status for the Gateway")
	})

	t.Run("XBackendCrossNamespace", func(t *testing.T) {
		gw := makeGateway("integ-gw-xbackend-xns", "default", gc.Name)
		if err := k8sClient.Create(ctx, gw); err != nil {
			t.Fatalf("failed to create Gateway: %v", err)
		}
		t.Cleanup(func() { k8sClient.Delete(ctx, gw) })

		systemCA := gwapiv1.WellKnownCACertificatesSystem
		makeXB := func(ns string) *apisxv1alpha1.XBackend {
			return &apisxv1alpha1.XBackend{
				ObjectMeta: metav1.ObjectMeta{Name: "integ-xns-backend", Namespace: ns},
				Spec: apisxv1alpha1.BackendSpec{
					Type:             apisxv1alpha1.BackendTypeExternalHostname,
					Port:             apisxv1alpha1.BackendPort{Port: 443},
					ExternalHostname: &apisxv1alpha1.ExternalHostnameBackend{Hostname: "api.example.com"},
					TLS: &apisxv1alpha1.BackendTLS{
						Mode: apisxv1alpha1.BackendTLSModeServerOnly,
						Validation: gwapiv1.BackendTLSPolicyValidation{
							Hostname:                "api.example.com",
							WellKnownCACertificates: &systemCA,
						},
					},
				},
			}
		}

		xbGroup := gwapiv1.Group(apisxv1alpha1.GroupName)
		xbKind := gwapiv1.Kind("XBackend")
		xbPort := gwapiv1.PortNumber(443)
		makeXNSRoute := func(name, hostname, backendNS string) *gwapiv1.HTTPRoute {
			ns := gwapiv1.Namespace(backendNS)
			route := makeHTTPRoute(name, "default", gw.Name)
			route.Spec.Hostnames = []gwapiv1.Hostname{gwapiv1.Hostname(hostname)}
			route.Spec.Rules = []gwapiv1.HTTPRouteRule{{
				BackendRefs: []gwapiv1.HTTPBackendRef{{
					BackendRef: gwapiv1.BackendRef{
						BackendObjectReference: gwapiv1.BackendObjectReference{
							Group:     &xbGroup,
							Kind:      &xbKind,
							Namespace: &ns,
							Name:      "integ-xns-backend",
							Port:      &xbPort,
						},
					},
				}},
			}}
			return route
		}

		routeResolvedRefs := func(name string) (metav1.ConditionStatus, string, bool) {
			var rt gwapiv1.HTTPRoute
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, &rt); err != nil {
				return "", "", false
			}
			for _, p := range rt.Status.Parents {
				for _, c := range p.Conditions {
					if c.Type == string(gwapiv1.RouteConditionResolvedRefs) {
						return c.Status, c.Reason, true
					}
				}
			}
			return "", "", false
		}

		// Scenario A: no ReferenceGrant in the backend namespace. The cross-namespace
		// XBackend ref must be denied -> ResolvedRefs=False, reason RefNotPermitted.
		nsNoGrant := &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "integ-xb-nogrant"}}
		if err := k8sClient.Create(ctx, nsNoGrant); err != nil {
			t.Fatalf("failed to create namespace: %v", err)
		}
		t.Cleanup(func() { k8sClient.Delete(ctx, nsNoGrant) })

		xbNoGrant := makeXB(nsNoGrant.Name)
		if err := k8sClient.Create(ctx, xbNoGrant); err != nil {
			t.Fatalf("failed to create XBackend: %v", err)
		}
		t.Cleanup(func() { k8sClient.Delete(ctx, xbNoGrant) })

		routeDenied := makeXNSRoute("integ-route-xns-denied", "denied.example.com", nsNoGrant.Name)
		if err := k8sClient.Create(ctx, routeDenied); err != nil {
			t.Fatalf("failed to create HTTPRoute: %v", err)
		}
		t.Cleanup(func() { k8sClient.Delete(ctx, routeDenied) })

		requireEventually(t, func() bool {
			status, reason, ok := routeResolvedRefs(routeDenied.Name)
			return ok && status == metav1.ConditionFalse && reason == string(gwapiv1.RouteReasonRefNotPermitted)
		}, 10*time.Second, 100*time.Millisecond, "cross-namespace XBackend ref without a ReferenceGrant should report RefNotPermitted")

		// Scenario B: a ReferenceGrant authorizes the cross-namespace ref. Created
		// before the route so the first reconcile already sees the grant (the
		// controller does not watch ReferenceGrant) -> ResolvedRefs=True.
		nsGrant := &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "integ-xb-grant"}}
		if err := k8sClient.Create(ctx, nsGrant); err != nil {
			t.Fatalf("failed to create namespace: %v", err)
		}
		t.Cleanup(func() { k8sClient.Delete(ctx, nsGrant) })

		xbGrant := makeXB(nsGrant.Name)
		if err := k8sClient.Create(ctx, xbGrant); err != nil {
			t.Fatalf("failed to create XBackend: %v", err)
		}
		t.Cleanup(func() { k8sClient.Delete(ctx, xbGrant) })

		grant := &gwapiv1beta1.ReferenceGrant{
			ObjectMeta: metav1.ObjectMeta{Name: "allow-xbackend", Namespace: nsGrant.Name},
			Spec: gwapiv1beta1.ReferenceGrantSpec{
				From: []gwapiv1beta1.ReferenceGrantFrom{{
					Group:     "gateway.networking.k8s.io",
					Kind:      "HTTPRoute",
					Namespace: "default",
				}},
				To: []gwapiv1beta1.ReferenceGrantTo{{
					Group: "gateway.networking.x-k8s.io",
					Kind:  "XBackend",
				}},
			},
		}
		if err := k8sClient.Create(ctx, grant); err != nil {
			t.Fatalf("failed to create ReferenceGrant: %v", err)
		}
		t.Cleanup(func() { k8sClient.Delete(ctx, grant) })

		routeAllowed := makeXNSRoute("integ-route-xns-allowed", "allowed.example.com", nsGrant.Name)
		if err := k8sClient.Create(ctx, routeAllowed); err != nil {
			t.Fatalf("failed to create HTTPRoute: %v", err)
		}
		t.Cleanup(func() { k8sClient.Delete(ctx, routeAllowed) })

		requireEventually(t, func() bool {
			status, reason, ok := routeResolvedRefs(routeAllowed.Name)
			return ok && status == metav1.ConditionTrue && reason == string(gwapiv1.RouteReasonResolvedRefs)
		}, 10*time.Second, 100*time.Millisecond, "cross-namespace XBackend ref with a ReferenceGrant should report ResolvedRefs=True")
	})

	t.Run("Cleanup", func(t *testing.T) {
		// Reset mock calls for this subtest
		mock.mu.Lock()
		mock.calls = nil
		mock.existingTunnel = nil
		mock.mu.Unlock()

		gw := makeGateway("integ-gw-cleanup", "default", gc.Name)
		if err := k8sClient.Create(ctx, gw); err != nil {
			t.Fatalf("failed to create Gateway: %v", err)
		}

		// Wait for reconciliation to complete (finalizer added)
		requireEventually(t, func() bool {
			var fetched gwapiv1.Gateway
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: gw.Name, Namespace: gw.Namespace}, &fetched); err != nil {
				return false
			}
			return controllerutil.ContainsFinalizer(&fetched, "cloudflared-gateway.jan0ski.net/cleanup")
		}, 10*time.Second, 100*time.Millisecond, "finalizer should be added before deletion")

		// Set existing tunnel so mock returns it during cleanup
		mock.mu.Lock()
		mock.existingTunnel = &cfclient.Tunnel{ID: "mock-tunnel-id", Name: gw.Name}
		mock.mu.Unlock()

		// Delete the Gateway
		if err := k8sClient.Delete(ctx, gw); err != nil {
			t.Fatalf("failed to delete Gateway: %v", err)
		}

		// Wait for the Gateway to be fully removed
		requireEventually(t, func() bool {
			var fetched gwapiv1.Gateway
			err := k8sClient.Get(ctx, types.NamespacedName{Name: gw.Name, Namespace: gw.Namespace}, &fetched)
			return err != nil // NotFound means cleanup completed
		}, 10*time.Second, 100*time.Millisecond, "Gateway should be deleted after cleanup")

		if !mock.hasCall("DeleteTunnel") {
			t.Error("DeleteTunnel should have been called during cleanup")
		}

		// Verify Deployment was cleaned up
		requireEventually(t, func() bool {
			var deploy appsv1.Deployment
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      controller.DeploymentName(gw.Name),
				Namespace: gw.Namespace,
			}, &deploy)
			return err != nil // NotFound means cleanup succeeded
		}, 10*time.Second, 100*time.Millisecond, "deployment should be cleaned up")

		// Verify Secret was cleaned up
		requireEventually(t, func() bool {
			var secret v1.Secret
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      controller.TunnelSecretName(gw.Name),
				Namespace: gw.Namespace,
			}, &secret)
			return err != nil // NotFound means cleanup succeeded
		}, 10*time.Second, 100*time.Millisecond, "tunnel secret should be cleaned up")
	})

	t.Run("SkipsWrongController", func(t *testing.T) {
		// Reset mock calls
		mock.mu.Lock()
		mock.calls = nil
		mock.existingTunnel = nil
		mock.mu.Unlock()

		otherGC := &gwapiv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "integ-gc-other"},
			Spec: gwapiv1.GatewayClassSpec{
				ControllerName: "other-controller/not-ours",
			},
		}
		if err := k8sClient.Create(ctx, otherGC); err != nil {
			t.Fatalf("failed to create GatewayClass: %v", err)
		}
		t.Cleanup(func() { k8sClient.Delete(ctx, otherGC) })

		gw := makeGateway("integ-gw-other", "default", otherGC.Name)
		if err := k8sClient.Create(ctx, gw); err != nil {
			t.Fatalf("failed to create Gateway: %v", err)
		}
		t.Cleanup(func() { k8sClient.Delete(ctx, gw) })

		// Give the reconciler time to process
		time.Sleep(2 * time.Second)

		if mock.hasCall("CreateTunnel") {
			t.Error("CreateTunnel should NOT be called for a gateway with a different controller")
		}

		var fetched gwapiv1.Gateway
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: gw.Name, Namespace: gw.Namespace}, &fetched); err != nil {
			t.Fatalf("failed to get Gateway: %v", err)
		}
		if controllerutil.ContainsFinalizer(&fetched, "cloudflared-gateway.jan0ski.net/cleanup") {
			t.Error("finalizer should NOT be added to a gateway with a different controller")
		}
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func requireEventually(t *testing.T, condition func() bool, timeout, interval time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(interval)
	}
	t.Fatalf("timed out waiting: %s", msg)
}
