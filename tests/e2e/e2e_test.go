package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/envfuncs"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/support/kind"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	ctrl "github.com/mccormickt/cloudflared-gateway/internal/controller"
)

var testenv env.Environment

const testNamespace = "e2e-test"

func TestMain(m *testing.M) {
	testenv = env.New()
	kindClusterName := envconf.RandomName("cf-tunnel-e2e", 16)

	testenv.Setup(
		envfuncs.CreateCluster(kind.NewProvider(), kindClusterName),
		envfuncs.CreateNamespace(testNamespace),
		func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
			// Install Gateway API CRDs (experimental, includes TLSRoute)
			// Use --server-side to avoid annotation size limits on large CRDs like HTTPRoute
			cmd := exec.CommandContext(ctx, "kubectl", "apply", "--server-side", "-f",
				fmt.Sprintf("https://github.com/kubernetes-sigs/gateway-api/releases/download/%s/experimental-install.yaml", gatewayAPIVersion()))
			out, err := cmd.CombinedOutput()
			if err != nil {
				return ctx, fmt.Errorf("installing Gateway API CRDs: %s: %w", string(out), err)
			}
			// Wait for CRDs to be established
			time.Sleep(2 * time.Second)
			return ctx, nil
		},
	)

	testenv.Finish(
		envfuncs.DeleteNamespace(testNamespace),
		envfuncs.DestroyCluster(kindClusterName),
	)

	os.Exit(testenv.Run(m))
}

func gatewayAPIVersion() string {
	out, err := exec.CommandContext(context.Background(), "go", "list", "-m", "-f", "{{.Version}}", "sigs.k8s.io/gateway-api").Output()
	if err != nil {
		panic("failed to find gateway-api module version: " + err.Error())
	}
	return strings.TrimSpace(string(out))
}

func TestE2E_GatewayClassCreation(t *testing.T) {
	f := features.New("GatewayClass lifecycle").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			c, err := client.New(cfg.Client().RESTConfig(), client.Options{})
			if err != nil {
				t.Fatalf("creating client: %v", err)
			}
			utilruntime.Must(gwapiv1.Install(c.Scheme()))

			gc := &gwapiv1.GatewayClass{
				ObjectMeta: metav1.ObjectMeta{Name: "e2e-cloudflare-tunnel"},
				Spec: gwapiv1.GatewayClassSpec{
					ControllerName: gwapiv1.GatewayController(ctrl.ControllerName),
				},
			}
			if err := c.Create(ctx, gc); err != nil {
				t.Fatalf("creating GatewayClass: %v", err)
			}
			return ctx
		}).
		Assess("GatewayClass exists", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			c, _ := client.New(cfg.Client().RESTConfig(), client.Options{})
			utilruntime.Must(gwapiv1.Install(c.Scheme()))

			var gc gwapiv1.GatewayClass
			if err := c.Get(ctx, types.NamespacedName{Name: "e2e-cloudflare-tunnel"}, &gc); err != nil {
				t.Fatalf("getting GatewayClass: %v", err)
			}
			if gc.Spec.ControllerName != gwapiv1.GatewayController(ctrl.ControllerName) {
				t.Errorf("wrong controller name: %s", gc.Spec.ControllerName)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			c, _ := client.New(cfg.Client().RESTConfig(), client.Options{})
			utilruntime.Must(gwapiv1.Install(c.Scheme()))
			c.Delete(ctx, &gwapiv1.GatewayClass{ObjectMeta: metav1.ObjectMeta{Name: "e2e-cloudflare-tunnel"}})
			return ctx
		}).
		Feature()

	testenv.Test(t, f)
}

func TestE2E_GatewayAndHTTPRoute(t *testing.T) {
	f := features.New("Gateway + HTTPRoute creation").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			c, err := client.New(cfg.Client().RESTConfig(), client.Options{})
			if err != nil {
				t.Fatalf("creating client: %v", err)
			}
			utilruntime.Must(gwapiv1.Install(c.Scheme()))

			// Create GatewayClass
			gc := &gwapiv1.GatewayClass{
				ObjectMeta: metav1.ObjectMeta{Name: "e2e-tunnel-class"},
				Spec: gwapiv1.GatewayClassSpec{
					ControllerName: gwapiv1.GatewayController(ctrl.ControllerName),
				},
			}
			if err := c.Create(ctx, gc); err != nil {
				t.Fatalf("creating GatewayClass: %v", err)
			}

			// Create Gateway
			from := gwapiv1.NamespacesFromSame
			gw := &gwapiv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{Name: "e2e-tunnel", Namespace: testNamespace},
				Spec: gwapiv1.GatewaySpec{
					GatewayClassName: "e2e-tunnel-class",
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
			if err := c.Create(ctx, gw); err != nil {
				t.Fatalf("creating Gateway: %v", err)
			}

			// Create backend Service
			svc := &v1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "web-svc", Namespace: testNamespace},
				Spec: v1.ServiceSpec{
					Ports:    []v1.ServicePort{{Port: 80}},
					Selector: map[string]string{"app": "web"},
				},
			}
			if err := c.Create(ctx, svc); err != nil {
				t.Fatalf("creating Service: %v", err)
			}

			// Create HTTPRoute
			gwGroup := gwapiv1.Group(gwapiv1.GroupName)
			gwKind := gwapiv1.Kind("Gateway")
			port := gwapiv1.PortNumber(80)
			route := &gwapiv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "e2e-route", Namespace: testNamespace},
				Spec: gwapiv1.HTTPRouteSpec{
					CommonRouteSpec: gwapiv1.CommonRouteSpec{
						ParentRefs: []gwapiv1.ParentReference{{
							Group: &gwGroup,
							Kind:  &gwKind,
							Name:  "e2e-tunnel",
						}},
					},
					Hostnames: []gwapiv1.Hostname{"e2e.example.com"},
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
			if err := c.Create(ctx, route); err != nil {
				t.Fatalf("creating HTTPRoute: %v", err)
			}

			return ctx
		}).
		Assess("Gateway and HTTPRoute exist", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			c, _ := client.New(cfg.Client().RESTConfig(), client.Options{})
			utilruntime.Must(gwapiv1.Install(c.Scheme()))

			var gw gwapiv1.Gateway
			if err := c.Get(ctx, types.NamespacedName{Name: "e2e-tunnel", Namespace: testNamespace}, &gw); err != nil {
				t.Fatalf("getting Gateway: %v", err)
			}

			var route gwapiv1.HTTPRoute
			if err := c.Get(ctx, types.NamespacedName{Name: "e2e-route", Namespace: testNamespace}, &route); err != nil {
				t.Fatalf("getting HTTPRoute: %v", err)
			}

			if len(route.Spec.Hostnames) != 1 || route.Spec.Hostnames[0] != "e2e.example.com" {
				t.Errorf("unexpected hostnames: %v", route.Spec.Hostnames)
			}

			return ctx
		}).
		Assess("Resources are valid for controller", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			c, _ := client.New(cfg.Client().RESTConfig(), client.Options{})
			utilruntime.Must(gwapiv1.Install(c.Scheme()))

			var gw gwapiv1.Gateway
			if err := c.Get(ctx, types.NamespacedName{Name: "e2e-tunnel", Namespace: testNamespace}, &gw); err != nil {
				t.Fatalf("getting Gateway: %v", err)
			}

			// Verify attachment would work
			allowed, err := ctrl.CheckRouteAttachment(ctx, c, &gw, testNamespace, "HTTPRoute")
			if err != nil {
				t.Fatalf("CheckRouteAttachment: %v", err)
			}
			if !allowed {
				t.Error("HTTPRoute should be attachable to Gateway")
			}

			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			c, _ := client.New(cfg.Client().RESTConfig(), client.Options{})
			utilruntime.Must(gwapiv1.Install(c.Scheme()))
			c.Delete(ctx, &gwapiv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "e2e-route", Namespace: testNamespace}})
			c.Delete(ctx, &gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "e2e-tunnel", Namespace: testNamespace}})
			c.Delete(ctx, &gwapiv1.GatewayClass{ObjectMeta: metav1.ObjectMeta{Name: "e2e-tunnel-class"}})
			c.Delete(ctx, &v1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web-svc", Namespace: testNamespace}})
			c.Delete(ctx, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "cloudflared-e2e-tunnel", Namespace: testNamespace}})
			return ctx
		}).
		Feature()

	testenv.Test(t, f)
}
