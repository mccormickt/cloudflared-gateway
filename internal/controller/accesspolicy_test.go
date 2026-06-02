package controller

import (
	"context"
	"testing"

	cfv1alpha1 "github.com/mccormickt/cloudflared-gateway/api/v1alpha1"
	cfclient "github.com/mccormickt/cloudflared-gateway/internal/cloudflare"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func makeAccessPolicy(name, namespace, teamName string, required bool, audTags []string, targetKind, targetName string) *cfv1alpha1.CloudflareAccessPolicy {
	return &cfv1alpha1.CloudflareAccessPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: cfv1alpha1.CloudflareAccessPolicySpec{
			TargetRefs: []gwapiv1.LocalPolicyTargetReference{{
				Group: gwapiv1.Group(gwapiv1.GroupName),
				Kind:  gwapiv1.Kind(targetKind),
				Name:  gwapiv1.ObjectName(targetName),
			}},
			TeamName: teamName,
			Required: required,
			AudTag:   audTags,
		},
	}
}

func TestApplyAccessPolicies_GatewayTarget(t *testing.T) {
	scheme := testScheme()
	gw := makeGateway("test-gw", "default")
	policy := makeAccessPolicy("require-access", "default", "my-org", true,
		[]string{"aud-1", "aud-2"}, "Gateway", "test-gw")

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(policy).
		WithStatusSubresource(policy).
		Build()
	route := makeHTTPRoute("web-route", "default", "test-gw")
	r := &GatewayReconciler{
		Client:           c,
		CloudflareClient: newMockClient(),
		ControllerName:   gwapiv1.GatewayController(ControllerName),
	}

	httpRoutes := []gwapiv1.HTTPRoute{*route}
	rules := cfclient.BuildIngressRules(httpRoutes)

	rules, err := r.applyAccessPolicies(context.Background(), rules, gw, httpRoutes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].OriginRequest == nil || rules[0].OriginRequest.Access == nil {
		t.Fatal("expected access config on Gateway-targeted policy")
	}
	if rules[0].OriginRequest.Access.TeamName != "my-org" {
		t.Errorf("teamName: got %q, want %q", rules[0].OriginRequest.Access.TeamName, "my-org")
	}
	if !rules[0].OriginRequest.Access.Required {
		t.Error("expected required=true")
	}
	if len(rules[0].OriginRequest.Access.AudTag) != 2 {
		t.Errorf("audTag: got %v", rules[0].OriginRequest.Access.AudTag)
	}
}

func TestApplyAccessPolicies_RouteTarget(t *testing.T) {
	scheme := testScheme()
	gw := makeGateway("test-gw", "default")
	policy := makeAccessPolicy("route-policy", "default", "route-team", false,
		nil, "HTTPRoute", "web-route")
	route := makeHTTPRoute("web-route", "default", "test-gw")

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(policy).
		WithStatusSubresource(policy).
		Build()
	r := &GatewayReconciler{
		Client:           c,
		CloudflareClient: newMockClient(),
		ControllerName:   gwapiv1.GatewayController(ControllerName),
	}

	httpRoutes := []gwapiv1.HTTPRoute{*route}
	rules := cfclient.BuildIngressRules(httpRoutes)

	rules, err := r.applyAccessPolicies(context.Background(), rules, gw, httpRoutes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rules[0].OriginRequest == nil || rules[0].OriginRequest.Access == nil {
		t.Fatal("expected access config on route-targeted policy")
	}
	if rules[0].OriginRequest.Access.TeamName != "route-team" {
		t.Errorf("teamName: got %q", rules[0].OriginRequest.Access.TeamName)
	}
}

func TestApplyAccessPolicies_NoPolicy(t *testing.T) {
	scheme := testScheme()
	gw := makeGateway("test-gw", "default")
	route := makeHTTPRoute("web-route", "default", "test-gw")

	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &GatewayReconciler{
		Client:           c,
		CloudflareClient: newMockClient(),
		ControllerName:   gwapiv1.GatewayController(ControllerName),
	}

	httpRoutes := []gwapiv1.HTTPRoute{*route}
	rules := cfclient.BuildIngressRules(httpRoutes)

	rules, err := r.applyAccessPolicies(context.Background(), rules, gw, httpRoutes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rules[0].OriginRequest != nil && rules[0].OriginRequest.Access != nil {
		t.Error("expected no access config when no policy exists")
	}
}

func TestApplyAccessPolicies_PolicyTargetsDifferentGateway(t *testing.T) {
	scheme := testScheme()
	gw := makeGateway("test-gw", "default")
	// Policy targets a different gateway
	policy := makeAccessPolicy("other-policy", "default", "other-org", true,
		nil, "Gateway", "other-gw")
	route := makeHTTPRoute("web-route", "default", "test-gw")

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(policy).
		WithStatusSubresource(policy).
		Build()
	r := &GatewayReconciler{
		Client:           c,
		CloudflareClient: newMockClient(),
		ControllerName:   gwapiv1.GatewayController(ControllerName),
	}

	httpRoutes := []gwapiv1.HTTPRoute{*route}
	rules := cfclient.BuildIngressRules(httpRoutes)

	rules, err := r.applyAccessPolicies(context.Background(), rules, gw, httpRoutes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rules[0].OriginRequest != nil && rules[0].OriginRequest.Access != nil {
		t.Error("policy targeting different gateway should not apply")
	}
}

func TestApplyAccessPolicies_PreservesExistingOriginRequest(t *testing.T) {
	scheme := testScheme()
	gw := makeGateway("test-gw", "default")
	policy := makeAccessPolicy("require-access", "default", "my-org", true,
		nil, "HTTPRoute", "web-route")
	route := makeHTTPRoute("web-route", "default", "test-gw")

	// Add URLRewrite filter
	rewriteHostname := gwapiv1.PreciseHostname("rewritten.example.com")
	route.Spec.Rules[0].Filters = append(route.Spec.Rules[0].Filters, gwapiv1.HTTPRouteFilter{
		Type: gwapiv1.HTTPRouteFilterURLRewrite,
		URLRewrite: &gwapiv1.HTTPURLRewriteFilter{
			Hostname: &rewriteHostname,
		},
	})

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(policy).
		WithStatusSubresource(policy).
		Build()
	r := &GatewayReconciler{
		Client:           c,
		CloudflareClient: newMockClient(),
		ControllerName:   gwapiv1.GatewayController(ControllerName),
	}

	httpRoutes := []gwapiv1.HTTPRoute{*route}
	rules := cfclient.BuildIngressRules(httpRoutes)

	rules, err := r.applyAccessPolicies(context.Background(), rules, gw, httpRoutes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rules[0].OriginRequest == nil {
		t.Fatal("expected originRequest")
	}
	if rules[0].OriginRequest.HTTPHostHeader == nil || *rules[0].OriginRequest.HTTPHostHeader != "rewritten.example.com" {
		t.Error("expected HTTPHostHeader preserved")
	}
	if rules[0].OriginRequest.Access == nil || rules[0].OriginRequest.Access.TeamName != "my-org" {
		t.Error("expected access config added alongside existing originRequest")
	}
}

// staleGatewayAncestor returns a PolicyStatus carrying an ancestor entry for gw
// owned by this controller, simulating status written on a prior reconcile.
func staleGatewayAncestor(gw *gwapiv1.Gateway) gwapiv1.PolicyStatus {
	return gwapiv1.PolicyStatus{
		Ancestors: []gwapiv1.PolicyAncestorStatus{{
			AncestorRef:    gatewayAncestorRef(gw),
			ControllerName: gwapiv1.GatewayController(ControllerName),
			Conditions: []metav1.Condition{{
				Type:               "Accepted",
				Status:             metav1.ConditionTrue,
				Reason:             "Accepted",
				Message:            "Policy is accepted",
				LastTransitionTime: metav1.Now(),
			}},
		}},
	}
}

func TestPatchAccessPolicyStatuses_PrunesStaleAncestor(t *testing.T) {
	scheme := testScheme()
	gw := makeGateway("test-gw", "default")

	// Policy used to target the Gateway (so it has an ancestor entry) but now
	// targets a route that is not attached — it no longer applies to this Gateway.
	policy := makeAccessPolicy("retargeted", "default", "team", false, nil, "HTTPRoute", "detached-route")
	policy.Status = staleGatewayAncestor(gw)

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(policy).
		WithStatusSubresource(policy).
		Build()
	r := &GatewayReconciler{
		Client:           c,
		CloudflareClient: newMockClient(),
		ControllerName:   gwapiv1.GatewayController(ControllerName),
	}

	valid := validPolicyTargets(gw, nil, nil, nil, nil) // only Gateway/test-gw is valid
	if _, err := r.patchAccessPolicyStatuses(context.Background(), gw, valid); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got cfv1alpha1.CloudflareAccessPolicy
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(policy), &got); err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if len(got.Status.Ancestors) != 0 {
		t.Errorf("expected stale ancestor pruned, got %d ancestors", len(got.Status.Ancestors))
	}
}

func TestPrunePolicyAncestorStatus_OnCleanup(t *testing.T) {
	scheme := testScheme()
	gw := makeGateway("test-gw", "default")

	access := makeAccessPolicy("access", "default", "team", false, nil, "Gateway", "test-gw")
	access.Status = staleGatewayAncestor(gw)
	origin := &cfv1alpha1.CloudflareOriginPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "origin", Namespace: "default"},
		Spec: cfv1alpha1.CloudflareOriginPolicySpec{
			TargetRefs: []gwapiv1.LocalPolicyTargetReference{{
				Group: gwapiv1.GroupName, Kind: "Gateway", Name: "test-gw",
			}},
		},
		Status: staleGatewayAncestor(gw),
	}

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(access, origin).
		WithStatusSubresource(access, origin).
		Build()
	r := &GatewayReconciler{
		Client:           c,
		CloudflareClient: newMockClient(),
		ControllerName:   gwapiv1.GatewayController(ControllerName),
	}

	r.prunePolicyAncestorStatus(context.Background(), gw)

	var gotAccess cfv1alpha1.CloudflareAccessPolicy
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(access), &gotAccess); err != nil {
		t.Fatalf("get access policy: %v", err)
	}
	if len(gotAccess.Status.Ancestors) != 0 {
		t.Errorf("expected access policy ancestor pruned, got %d", len(gotAccess.Status.Ancestors))
	}

	var gotOrigin cfv1alpha1.CloudflareOriginPolicy
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(origin), &gotOrigin); err != nil {
		t.Fatalf("get origin policy: %v", err)
	}
	if len(gotOrigin.Status.Ancestors) != 0 {
		t.Errorf("expected origin policy ancestor pruned, got %d", len(gotOrigin.Status.Ancestors))
	}
}

func TestRulesProduced(t *testing.T) {
	tests := []struct {
		name         string
		numHostnames int
		numPaths     int
		expected     int
	}{
		{"no hostnames, no paths", 0, 0, 1},
		{"no hostnames, 2 paths", 0, 2, 2},
		{"2 hostnames, no paths", 2, 0, 2},
		{"2 hostnames, 3 paths", 2, 3, 6},
		{"1 hostname, 1 path", 1, 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rulesProduced(tt.numHostnames, tt.numPaths); got != tt.expected {
				t.Errorf("rulesProduced(%d, %d) = %d, want %d", tt.numHostnames, tt.numPaths, got, tt.expected)
			}
		})
	}
}

func TestCountPaths(t *testing.T) {
	exact := gwapiv1.PathMatchExact
	prefix := gwapiv1.PathMatchPathPrefix
	regex := gwapiv1.PathMatchRegularExpression
	fooPath := "/foo"
	rootPath := "/"

	tests := []struct {
		name     string
		matches  []gwapiv1.HTTPRouteMatch
		expected int
	}{
		{"nil matches", nil, 0},
		{"no path", []gwapiv1.HTTPRouteMatch{{}}, 0},
		{"exact path", []gwapiv1.HTTPRouteMatch{{Path: &gwapiv1.HTTPPathMatch{Type: &exact, Value: &fooPath}}}, 1},
		{"prefix non-root", []gwapiv1.HTTPRouteMatch{{Path: &gwapiv1.HTTPPathMatch{Type: &prefix, Value: &fooPath}}}, 1},
		{"prefix root (omitted)", []gwapiv1.HTTPRouteMatch{{Path: &gwapiv1.HTTPPathMatch{Type: &prefix, Value: &rootPath}}}, 0},
		{"regex path", []gwapiv1.HTTPRouteMatch{{Path: &gwapiv1.HTTPPathMatch{Type: &regex, Value: &fooPath}}}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := countPaths(tt.matches); got != tt.expected {
				t.Errorf("countPaths() = %d, want %d", got, tt.expected)
			}
		})
	}
}
