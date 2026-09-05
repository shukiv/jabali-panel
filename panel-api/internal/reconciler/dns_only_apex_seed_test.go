package reconciler

import (
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func strptr(s string) *string { return &s }

// GH #1540: dnsOnlyApexSeed builds the single "@ A <ip>" row for a web-off DNS
// zone created with a tenant-chosen apex IP. BootstrapRecords skips the apex for
// a web-off zone, and convergeApexAddrRecords is web-gated, so this seed is the
// only writer — it must produce a tenant-owned (Managed=false) apex A only when
// the domain is a web-off zone carrying a non-empty IP.
func TestDNSOnlyApexSeed(t *testing.T) {
	srv := &models.ServerSettings{PublicIPv4: "192.0.2.1"}
	idNew := func() string { return "rec-1" }

	t.Run("web-off zone with an apex IP seeds a tenant-owned @ A", func(t *testing.T) {
		dom := &models.Domain{WebDisabled: true, DNSApexIPv4: strptr("203.0.113.10")}
		rec, ok := dnsOnlyApexSeed(dom, "zone-1", srv, idNew)
		if !ok {
			t.Fatal("expected a seed record")
		}
		if rec.Name != "@" || rec.Type != "A" {
			t.Errorf("want @ A, got %s %s", rec.Name, rec.Type)
		}
		if rec.Content != "203.0.113.10" {
			t.Errorf("content should be the apex IP, got %q", rec.Content)
		}
		if rec.ZoneID != "zone-1" {
			t.Errorf("zone id should be zone-1, got %q", rec.ZoneID)
		}
		if rec.Managed {
			t.Error("a web-off apex is tenant-owned — Managed must be false")
		}
		if rec.ManagedBy != nil {
			t.Error("ManagedBy must be nil")
		}
		if !rec.IsEnabled {
			t.Error("seed must be enabled")
		}
		if rec.TTL != models.EffectiveDNSTTL(srv) {
			t.Errorf("TTL should follow the server default, got %d", rec.TTL)
		}
	})

	t.Run("trims surrounding whitespace on the IP", func(t *testing.T) {
		dom := &models.Domain{WebDisabled: true, DNSApexIPv4: strptr("  198.51.100.7 ")}
		rec, ok := dnsOnlyApexSeed(dom, "zone-1", srv, idNew)
		if !ok || rec.Content != "198.51.100.7" {
			t.Fatalf("want trimmed 198.51.100.7, got ok=%v content=%q", ok, rec.Content)
		}
	})

	t.Run("web-enabled domain never seeds (apex is panel-managed)", func(t *testing.T) {
		dom := &models.Domain{WebDisabled: false, DNSApexIPv4: strptr("203.0.113.10")}
		if _, ok := dnsOnlyApexSeed(dom, "zone-1", srv, idNew); ok {
			t.Error("a web-enabled domain must not seed a custom apex")
		}
	})

	t.Run("nil apex IP does not seed", func(t *testing.T) {
		dom := &models.Domain{WebDisabled: true, DNSApexIPv4: nil}
		if _, ok := dnsOnlyApexSeed(dom, "zone-1", srv, idNew); ok {
			t.Error("a web-off zone without a custom IP must not seed")
		}
	})

	t.Run("empty apex IP does not seed", func(t *testing.T) {
		dom := &models.Domain{WebDisabled: true, DNSApexIPv4: strptr("   ")}
		if _, ok := dnsOnlyApexSeed(dom, "zone-1", srv, idNew); ok {
			t.Error("a blank custom IP must not seed")
		}
	})

	t.Run("nil domain does not seed", func(t *testing.T) {
		if _, ok := dnsOnlyApexSeed(nil, "zone-1", srv, idNew); ok {
			t.Error("nil domain must not seed")
		}
	})
}
