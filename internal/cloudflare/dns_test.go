package cloudflare

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestNormalizeDNSName(t *testing.T) {
	cases := map[string]string{
		"Example.COM.":        "example.com",
		"  app.example.com  ": "app.example.com",
		"APP.Example.Com":     "app.example.com",
		"host.example.com.":   "host.example.com",
		"":                    "",
		".":                   "",
	}
	for in, want := range cases {
		if got := NormalizeDNSName(in); got != want {
			t.Errorf("NormalizeDNSName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestChunkDNSChanges(t *testing.T) {
	var changes DNSChanges
	for i := 0; i < 5; i++ {
		changes.Creates = append(changes.Creates, DNSRecord{Name: fmt.Sprintf("h%d.example.com", i)})
	}

	chunks := chunkDNSChanges(changes, 2)
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks for 5 items at size 2, got %d", len(chunks))
	}
	total := 0
	for _, c := range chunks {
		n := len(c.Creates) + len(c.Updates) + len(c.Deletes)
		if n > 2 {
			t.Errorf("chunk exceeds size: %d", n)
		}
		total += n
	}
	if total != 5 {
		t.Errorf("expected 5 total items across chunks, got %d", total)
	}
}

func TestChunkDNSChanges_OrdersDeletesFirst(t *testing.T) {
	changes := DNSChanges{
		Creates: []DNSRecord{{Name: "new.example.com"}},
		Deletes: []DNSRecord{{ID: "old"}},
	}
	// Size 1 forces one item per chunk; the delete must come before the create so
	// a rename frees the old name first.
	chunks := chunkDNSChanges(changes, 1)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if len(chunks[0].Deletes) != 1 {
		t.Errorf("expected delete in first chunk, got %+v", chunks[0])
	}
	if len(chunks[1].Creates) != 1 {
		t.Errorf("expected create in second chunk, got %+v", chunks[1])
	}
}

func TestToV7BatchParams(t *testing.T) {
	changes := DNSChanges{
		Creates: []DNSRecord{{Name: "a.example.com", Type: "CNAME", Content: "tid.cfargotunnel.com", Proxied: true, Comment: "cloudflared-gateway:owner=uid"}},
		Updates: []DNSRecord{{ID: "rec-1", Name: "b.example.com", Type: "CNAME", Content: "tid.cfargotunnel.com", Proxied: true, Comment: "cloudflared-gateway:owner=uid"}},
		Deletes: []DNSRecord{{ID: "rec-2"}},
	}

	data, err := json.Marshal(toV7BatchParams("zone-1", changes))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)

	for _, want := range []string{
		`"posts"`, `"a.example.com"`, `"tid.cfargotunnel.com"`,
		`"proxied":true`, `"type":"CNAME"`, `"comment":"cloudflared-gateway:owner=uid"`,
		`"ttl":1`, // proxied records use automatic TTL
		`"patches"`, `"id":"rec-1"`, `"b.example.com"`,
		`"deletes"`, `"id":"rec-2"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("batch JSON missing %s\nfull: %s", want, s)
		}
	}
}
