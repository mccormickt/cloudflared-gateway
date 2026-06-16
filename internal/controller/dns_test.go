package controller

import (
	"context"
	"fmt"
	"testing"

	cfclient "github.com/mccormickt/cloudflared-gateway/internal/cloudflare"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

func TestCollectRouteHostnames(t *testing.T) {
	http := []gwapiv1.HTTPRoute{
		{Spec: gwapiv1.HTTPRouteSpec{Hostnames: []gwapiv1.Hostname{"a.example.com", "A.Example.com"}}},
		{Spec: gwapiv1.HTTPRouteSpec{Hostnames: []gwapiv1.Hostname{"b.example.com", ""}}},
	}
	grpc := []gwapiv1.GRPCRoute{
		{Spec: gwapiv1.GRPCRouteSpec{Hostnames: []gwapiv1.Hostname{"grpc.example.com", "b.example.com"}}},
	}
	tls := []gwapiv1alpha2.TLSRoute{
		{Spec: gwapiv1alpha2.TLSRouteSpec{Hostnames: []gwapiv1.Hostname{"tls.example.com."}}},
	}

	got := collectRouteHostnames(http, grpc, tls)

	want := map[string]bool{
		"a.example.com":    true,
		"b.example.com":    true,
		"grpc.example.com": true,
		"tls.example.com":  true,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d hostnames %v, want %d", len(got), got, len(want))
	}
	for _, h := range got {
		if !want[h] {
			t.Errorf("unexpected hostname %q", h)
		}
	}
}

func TestMatchZone(t *testing.T) {
	zones := []cfclient.Zone{
		{ID: "z-root", Name: "example.com"},
		{ID: "z-sub", Name: "sub.example.com"},
	}
	cases := []struct {
		host   string
		wantID string
		wantOK bool
	}{
		{"a.sub.example.com", "z-sub", true}, // longest suffix wins
		{"a.example.com", "z-root", true},    // only root matches
		{"example.com", "z-root", true},      // apex
		{"*.example.com", "z-root", true},    // wildcard
		{"other.net", "", false},             // no zone
		{"notexample.com", "", false},        // not a suffix boundary
	}
	for _, tc := range cases {
		z, ok := matchZone(tc.host, zones)
		if ok != tc.wantOK || z.ID != tc.wantID {
			t.Errorf("matchZone(%q) = (%q, %v), want (%q, %v)", tc.host, z.ID, ok, tc.wantID, tc.wantOK)
		}
	}
}

func TestPlanDNSChanges(t *testing.T) {
	const owner = "cloudflared-gateway:owner=uid"
	const target = "tid.cfargotunnel.com"
	desired := func(name string) cfclient.DNSRecord {
		return cfclient.DNSRecord{Name: name, Type: "CNAME", Content: target, Proxied: true, Comment: owner}
	}

	t.Run("create when absent", func(t *testing.T) {
		changes, conflicts := planDNSChanges([]cfclient.DNSRecord{desired("a.example.com")}, nil, owner)
		if len(changes.Creates) != 1 || len(changes.Updates) != 0 || len(changes.Deletes) != 0 {
			t.Fatalf("expected one create, got %+v", changes)
		}
		if len(conflicts) != 0 {
			t.Errorf("unexpected conflicts %v", conflicts)
		}
	})

	t.Run("noop when identical and owned", func(t *testing.T) {
		current := []cfclient.DNSRecord{{ID: "r1", Name: "a.example.com", Type: "CNAME", Content: target, Proxied: true, Comment: owner}}
		changes, _ := planDNSChanges([]cfclient.DNSRecord{desired("a.example.com")}, current, owner)
		if !changes.Empty() {
			t.Fatalf("expected no changes, got %+v", changes)
		}
	})

	t.Run("update when content differs", func(t *testing.T) {
		current := []cfclient.DNSRecord{{ID: "r1", Name: "a.example.com", Type: "CNAME", Content: "stale.cfargotunnel.com", Proxied: true, Comment: owner}}
		changes, _ := planDNSChanges([]cfclient.DNSRecord{desired("a.example.com")}, current, owner)
		if len(changes.Updates) != 1 || changes.Updates[0].ID != "r1" {
			t.Fatalf("expected one update carrying id r1, got %+v", changes)
		}
	})

	t.Run("conflict when unowned", func(t *testing.T) {
		current := []cfclient.DNSRecord{{ID: "r1", Name: "a.example.com", Type: "CNAME", Content: "other.net", Comment: "someone-else"}}
		changes, conflicts := planDNSChanges([]cfclient.DNSRecord{desired("a.example.com")}, current, owner)
		if !changes.Empty() {
			t.Fatalf("expected no changes for unowned record, got %+v", changes)
		}
		if len(conflicts) != 1 || conflicts[0] != "a.example.com" {
			t.Errorf("expected conflict for a.example.com, got %v", conflicts)
		}
	})

	t.Run("delete owned stale", func(t *testing.T) {
		current := []cfclient.DNSRecord{
			{ID: "r1", Name: "keep.example.com", Type: "CNAME", Content: target, Proxied: true, Comment: owner},
			{ID: "r2", Name: "stale.example.com", Type: "CNAME", Content: target, Proxied: true, Comment: owner},
			{ID: "r3", Name: "theirs.example.com", Type: "CNAME", Content: target, Proxied: true, Comment: "someone-else"},
		}
		changes, _ := planDNSChanges([]cfclient.DNSRecord{desired("keep.example.com")}, current, owner)
		if len(changes.Deletes) != 1 || changes.Deletes[0].ID != "r2" {
			t.Fatalf("expected to delete only stale owned record r2, got %+v", changes.Deletes)
		}
	})
}

func TestReconcileDNS_Disabled(t *testing.T) {
	gw := makeGateway("gw", "default")
	mock := newMockClient().withZones(cfclient.Zone{ID: "z1", Name: "example.com"})

	r := &GatewayReconciler{CloudflareClient: mock, ManageDNS: false}
	if err := r.reconcileDNS(context.Background(), gw, "tid", []string{"a.example.com"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls := mock.getCalls(); len(calls) != 0 {
		t.Errorf("expected no Cloudflare calls when DNS disabled, got %v", calls)
	}
}

func TestReconcileDNS_GatewayOptOut(t *testing.T) {
	gw := makeGateway("gw", "default")
	gw.Annotations = map[string]string{annotationDNSManaged: "false"}
	mock := newMockClient().withZones(cfclient.Zone{ID: "z1", Name: "example.com"})

	r := &GatewayReconciler{CloudflareClient: mock, ManageDNS: true}
	if err := r.reconcileDNS(context.Background(), gw, "tid", []string{"a.example.com"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls := mock.getCalls(); len(calls) != 0 {
		t.Errorf("expected no Cloudflare calls when Gateway opts out, got %v", calls)
	}
}

func TestReconcileDNS_CreateThenPrune(t *testing.T) {
	gw := makeGateway("gw", "default")
	mock := newMockClient().withZones(cfclient.Zone{ID: "z1", Name: "example.com"})
	r := &GatewayReconciler{CloudflareClient: mock, ManageDNS: true}

	if err := r.reconcileDNS(context.Background(), gw, "tid", []string{"a.example.com", "b.example.com"}); err != nil {
		t.Fatalf("first pass error: %v", err)
	}
	recs := mock.dnsRecordsFor("z1")
	if len(recs) != 2 {
		t.Fatalf("expected 2 records after create, got %d: %+v", len(recs), recs)
	}
	for _, rec := range recs {
		if rec.Content != "tid.cfargotunnel.com" || !rec.Proxied || rec.Comment != ownerComment(gw) {
			t.Errorf("unexpected record %+v", rec)
		}
	}

	// Drop b.example.com → it should be pruned.
	if err := r.reconcileDNS(context.Background(), gw, "tid", []string{"a.example.com"}); err != nil {
		t.Fatalf("second pass error: %v", err)
	}
	recs = mock.dnsRecordsFor("z1")
	if len(recs) != 1 || recs[0].Name != "a.example.com" {
		t.Fatalf("expected only a.example.com after prune, got %+v", recs)
	}

	// Drop the last hostname; its record must also be pruned.
	if err := r.reconcileDNS(context.Background(), gw, "tid", nil); err != nil {
		t.Fatalf("reconcileDNS() final prune error = %v", err)
	}
	if recs = mock.dnsRecordsFor("z1"); len(recs) != 0 {
		t.Fatalf("reconcileDNS() after final prune = %+v, want no records", recs)
	}
}

func TestReconcileDNS_PrunesRecordsFromOldZone(t *testing.T) {
	gw := makeGateway("gw", "default")
	mock := newMockClient().withZones(
		cfclient.Zone{ID: "z-example", Name: "example.com"},
		cfclient.Zone{ID: "z-other", Name: "other.net"},
	)
	r := &GatewayReconciler{CloudflareClient: mock, ManageDNS: true}

	if err := r.reconcileDNS(context.Background(), gw, "tid", []string{"old.example.com"}); err != nil {
		t.Fatalf("reconcileDNS() initial error = %v", err)
	}
	if err := r.reconcileDNS(context.Background(), gw, "tid", []string{"new.other.net"}); err != nil {
		t.Fatalf("reconcileDNS() move error = %v", err)
	}
	if got := mock.dnsRecordsFor("z-example"); len(got) != 0 {
		t.Errorf("reconcileDNS() old-zone records = %+v, want none", got)
	}
	got := mock.dnsRecordsFor("z-other")
	if len(got) != 1 || got[0].Name != "new.other.net" {
		t.Errorf("reconcileDNS() new-zone records = %+v, want new.other.net", got)
	}
}

func TestReconcileDNS_SkipsWildcard(t *testing.T) {
	gw := makeGateway("gw", "default")
	mock := newMockClient().withZones(cfclient.Zone{ID: "z1", Name: "example.com"})
	r := &GatewayReconciler{CloudflareClient: mock, ManageDNS: true}

	// A wildcard alongside a concrete hostname: only the concrete one is created.
	if err := r.reconcileDNS(context.Background(), gw, "tid", []string{"*.example.com", "app.example.com"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	recs := mock.dnsRecordsFor("z1")
	if len(recs) != 1 || recs[0].Name != "app.example.com" {
		t.Fatalf("expected only app.example.com, got %+v", recs)
	}
}

func TestReconcileDNS_IdempotentNoChurn(t *testing.T) {
	gw := makeGateway("gw", "default")
	mock := newMockClient().withZones(cfclient.Zone{ID: "z1", Name: "example.com"})
	r := &GatewayReconciler{CloudflareClient: mock, ManageDNS: true}

	if err := r.reconcileDNS(context.Background(), gw, "tid", []string{"a.example.com"}); err != nil {
		t.Fatalf("first pass error: %v", err)
	}
	// A converged second pass must not mutate anything.
	mock.mu.Lock()
	mock.calls = nil
	mock.mu.Unlock()
	if err := r.reconcileDNS(context.Background(), gw, "tid", []string{"a.example.com"}); err != nil {
		t.Fatalf("second pass error: %v", err)
	}
	for _, c := range mock.getCalls() {
		if c.method == "ApplyDNSChanges" {
			t.Errorf("converged reconcile should not call ApplyDNSChanges, got %v", c.args)
		}
	}
}

func TestReconcileDNS_ReportsUnownedRecordConflict(t *testing.T) {
	gw := makeGateway("gw", "default")
	mock := newMockClient().withZones(cfclient.Zone{ID: "z1", Name: "example.com"})
	mock.seedDNSRecord("z1", cfclient.DNSRecord{Name: "a.example.com", Type: "CNAME", Content: "other.example.net", Comment: "someone-else"})

	r := &GatewayReconciler{CloudflareClient: mock, ManageDNS: true}
	if err := r.reconcileDNS(context.Background(), gw, "tid", []string{"a.example.com"}); err == nil {
		t.Fatal("reconcileDNS() error = nil, want ownership conflict")
	}

	recs := mock.dnsRecordsFor("z1")
	if len(recs) != 1 {
		t.Fatalf("expected unowned record untouched, got %+v", recs)
	}
	if recs[0].Content != "other.example.net" || recs[0].Comment != "someone-else" {
		t.Errorf("unowned record was modified: %+v", recs[0])
	}
}

func TestTeardownDNS_DeletesOnlyOwned(t *testing.T) {
	gw := makeGateway("gw", "default")
	owner := ownerComment(gw)
	mock := newMockClient().withZones(cfclient.Zone{ID: "z1", Name: "example.com"})
	mock.seedDNSRecord("z1", cfclient.DNSRecord{Name: "mine.example.com", Type: "CNAME", Comment: owner})
	mock.seedDNSRecord("z1", cfclient.DNSRecord{Name: "theirs.example.com", Type: "CNAME", Comment: "someone-else"})

	r := &GatewayReconciler{CloudflareClient: mock, ManageDNS: true}
	if err := r.teardownDNS(context.Background(), gw); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	recs := mock.dnsRecordsFor("z1")
	if len(recs) != 1 || recs[0].Comment != "someone-else" {
		t.Fatalf("expected only the unowned record to remain, got %+v", recs)
	}
}

func TestCleanup_DNSFailureIsRetried(t *testing.T) {
	gw := makeGateway("gw", "default")
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(gw).Build()
	mock := newMockClient()
	mock.zonesErr = fmt.Errorf("zone API unavailable")
	r := &GatewayReconciler{
		Client:           c,
		CloudflareClient: mock,
		ManageDNS:        true,
	}

	if err := r.cleanup(context.Background(), gw); err == nil {
		t.Fatal("cleanup() error = nil, want DNS teardown error")
	}
}

func TestReconcile_ManageDNSCreatesRecord(t *testing.T) {
	scheme := testScheme()
	gw := makeGateway("test-gw", "default")
	gc := makeGatewayClass()
	route := makeHTTPRoute("web", "default", "test-gw") // hostname example.com

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(gw, gc, route).
		WithStatusSubresource(gw, gc, route).
		Build()
	mock := newMockClient().withZones(cfclient.Zone{ID: "z1", Name: "example.com"})

	r := &GatewayReconciler{
		Client:           c,
		CloudflareClient: mock,
		ControllerName:   gwapiv1.GatewayController(ControllerName),
		ManageDNS:        true,
	}

	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-gw", Namespace: "default"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	recs := mock.dnsRecordsFor("z1")
	if len(recs) != 1 {
		t.Fatalf("expected 1 DNS record, got %d: %+v", len(recs), recs)
	}
	got := recs[0]
	if got.Name != "example.com" {
		t.Errorf("record name = %q, want example.com", got.Name)
	}
	if got.Content != "mock-tunnel-id.cfargotunnel.com" {
		t.Errorf("record content = %q, want mock-tunnel-id.cfargotunnel.com", got.Content)
	}
	if !got.Proxied {
		t.Error("expected proxied record")
	}
	if got.Comment != ownerComment(gw) {
		t.Errorf("record comment = %q, want %q", got.Comment, ownerComment(gw))
	}
}

func TestReconcile_DNSFailureMarksProgrammedFalse(t *testing.T) {
	scheme := testScheme()
	gw := makeGateway("test-gw", "default")
	gc := makeGatewayClass()
	route := makeHTTPRoute("web", "default", "test-gw")

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(gw, gc, route).
		WithStatusSubresource(gw, gc, route).
		Build()
	mock := newMockClient().withZones(cfclient.Zone{ID: "z1", Name: "example.com"})
	mock.applyErr = fmt.Errorf("cloudflare batch rejected")

	r := &GatewayReconciler{
		Client:           c,
		CloudflareClient: mock,
		ControllerName:   gwapiv1.GatewayController(ControllerName),
		ManageDNS:        true,
	}

	// DNS failure is retriable, so Reconcile returns an error...
	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-gw", Namespace: "default"},
	}); err == nil {
		t.Fatal("expected a retriable error when DNS apply fails")
	}

	// ...but the Gateway status must not falsely advertise Programmed=True.
	var fetched gwapiv1.Gateway
	if err := c.Get(context.Background(), types.NamespacedName{Name: "test-gw", Namespace: "default"}, &fetched); err != nil {
		t.Fatalf("get gateway: %v", err)
	}
	cond := apimeta.FindStatusCondition(fetched.Status.Conditions, string(gwapiv1.GatewayConditionProgrammed))
	if cond == nil {
		t.Fatal("expected a Programmed condition")
	}
	if cond.Status != metav1.ConditionFalse || cond.Reason != "DNSNotReady" {
		t.Errorf("Programmed = %v/%s, want False/DNSNotReady", cond.Status, cond.Reason)
	}
}
