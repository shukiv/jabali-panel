package reconciler

import (
	"fmt"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func strptr(s string) *string { return &s }

// counterID returns a fresh id each call so a multi-record seed gets distinct
// IDs (the reconciler passes ids.NewULID, which is likewise unique per call).
func counterID() func() string {
	n := 0
	return func() string { n++; return fmt.Sprintf("rec-%d", n) }
}

// GH #1540 (+ IPv6 follow-up): dnsOnlyApexSeeds builds the "@ A <ipv4>" and/or
// "@ AAAA <ipv6>" rows for a web-off DNS zone created with a tenant-chosen apex
// IP. BootstrapRecords skips the apex for a web-off zone, and
// convergeApexAddrRecords is web-gated, so these seeds are the only writer —
// tenant-owned (Managed=false), each family independent.
func TestDNSOnlyApexSeeds(t *testing.T) {
	srv := &models.ServerSettings{PublicIPv4: "192.0.2.1"}

	t.Run("IPv4 only → one tenant-owned @ A", func(t *testing.T) {
		dom := &models.Domain{WebDisabled: true, DNSApexIPv4: strptr("203.0.113.10")}
		recs := dnsOnlyApexSeeds(dom, "zone-1", srv, counterID())
		if len(recs) != 1 {
			t.Fatalf("want 1 record, got %d", len(recs))
		}
		r := recs[0]
		if r.Name != "@" || r.Type != "A" || r.Content != "203.0.113.10" {
			t.Errorf("want @ A 203.0.113.10, got %s %s %s", r.Name, r.Type, r.Content)
		}
		if r.Managed || r.ManagedBy != nil || !r.IsEnabled {
			t.Error("apex seed must be tenant-owned (Managed=false, ManagedBy=nil) and enabled")
		}
		if r.TTL != models.EffectiveDNSTTL(srv) {
			t.Errorf("TTL should follow the server default, got %d", r.TTL)
		}
	})

	t.Run("IPv6 only → one @ AAAA", func(t *testing.T) {
		dom := &models.Domain{WebDisabled: true, DNSApexIPv6: strptr("2001:db8::1")}
		recs := dnsOnlyApexSeeds(dom, "zone-1", srv, counterID())
		if len(recs) != 1 {
			t.Fatalf("want 1 record, got %d", len(recs))
		}
		if recs[0].Type != "AAAA" || recs[0].Content != "2001:db8::1" {
			t.Errorf("want @ AAAA 2001:db8::1, got %s %s", recs[0].Type, recs[0].Content)
		}
	})

	t.Run("both → an A and an AAAA with distinct IDs", func(t *testing.T) {
		dom := &models.Domain{
			WebDisabled: true,
			DNSApexIPv4: strptr("203.0.113.10"),
			DNSApexIPv6: strptr("2001:db8::1"),
		}
		recs := dnsOnlyApexSeeds(dom, "zone-1", srv, counterID())
		if len(recs) != 2 {
			t.Fatalf("want 2 records, got %d", len(recs))
		}
		if recs[0].Type != "A" || recs[1].Type != "AAAA" {
			t.Errorf("want [A, AAAA], got [%s, %s]", recs[0].Type, recs[1].Type)
		}
		if recs[0].ID == recs[1].ID {
			t.Errorf("the two seeds must have distinct IDs, both %q", recs[0].ID)
		}
	})

	t.Run("trims surrounding whitespace", func(t *testing.T) {
		dom := &models.Domain{WebDisabled: true, DNSApexIPv4: strptr("  198.51.100.7 ")}
		recs := dnsOnlyApexSeeds(dom, "zone-1", srv, counterID())
		if len(recs) != 1 || recs[0].Content != "198.51.100.7" {
			t.Fatalf("want trimmed 198.51.100.7, got %+v", recs)
		}
	})

	t.Run("web-enabled domain seeds nothing (apex is panel-managed)", func(t *testing.T) {
		dom := &models.Domain{WebDisabled: false, DNSApexIPv4: strptr("203.0.113.10")}
		if recs := dnsOnlyApexSeeds(dom, "zone-1", srv, counterID()); len(recs) != 0 {
			t.Errorf("a web-enabled domain must seed nothing, got %d", len(recs))
		}
	})

	t.Run("no apex IP seeds nothing", func(t *testing.T) {
		dom := &models.Domain{WebDisabled: true}
		if recs := dnsOnlyApexSeeds(dom, "zone-1", srv, counterID()); len(recs) != 0 {
			t.Errorf("a web-off zone without a custom IP must seed nothing, got %d", len(recs))
		}
	})

	t.Run("empty apex IPs seed nothing", func(t *testing.T) {
		dom := &models.Domain{WebDisabled: true, DNSApexIPv4: strptr("  "), DNSApexIPv6: strptr("")}
		if recs := dnsOnlyApexSeeds(dom, "zone-1", srv, counterID()); len(recs) != 0 {
			t.Errorf("blank custom IPs must seed nothing, got %d", len(recs))
		}
	})

	t.Run("nil domain seeds nothing", func(t *testing.T) {
		if recs := dnsOnlyApexSeeds(nil, "zone-1", srv, counterID()); len(recs) != 0 {
			t.Errorf("nil domain must seed nothing, got %d", len(recs))
		}
	})
}
