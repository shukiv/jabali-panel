package main

import (
	"context"
	"testing"
)

// JAB-323: the CLI must split a four-field SRV content into the priority column
// + three-field content exactly as the HTTP handler does. It used to persist
// the raw four-field content with priority 0 — a malformed SRV record.
func TestDNSRecordAdd_SRVFourFieldNormalised(t *testing.T) {
	ctx := context.Background()
	zones := newMemDNSZoneRepo(testDNSZone())
	recs := newMemDNSRecordRepo()

	rec, err := dnsRecordAdd(ctx, zones, recs, "example.com", dnsRecordSpec{
		Name: "_sip._tcp", Type: "SRV", Content: "10 5 5060 sip.example.com", Enabled: true,
		// no explicit --priority: derive it from the four-field content
	})
	if err != nil {
		t.Fatalf("add SRV: %v", err)
	}
	if rec.Content != "5 5060 sip.example.com" {
		t.Errorf("content should be the three-field remainder, got %q", rec.Content)
	}
	if rec.Priority != 10 {
		t.Errorf("priority should be split out of the content (10), got %d", rec.Priority)
	}
}

// An explicit --priority is honoured even when the content still carries a
// leading field (mirrors the HTTP *int-priority semantics).
func TestDNSRecordAdd_SRVExplicitPriorityKept(t *testing.T) {
	ctx := context.Background()
	zones := newMemDNSZoneRepo(testDNSZone())
	recs := newMemDNSRecordRepo()

	rec, err := dnsRecordAdd(ctx, zones, recs, "example.com", dnsRecordSpec{
		Name: "_sip._tcp", Type: "SRV", Content: "10 5 5060 sip.example.com",
		Priority: 20, PriorityExplicit: true, Enabled: true,
	})
	if err != nil {
		t.Fatalf("add SRV: %v", err)
	}
	if rec.Content != "5 5060 sip.example.com" {
		t.Errorf("content should still be normalised to three fields, got %q", rec.Content)
	}
	if rec.Priority != 20 {
		t.Errorf("explicit priority must be kept (20), got %d", rec.Priority)
	}
}

func TestDNSRecordUpdate_SRVNormalised(t *testing.T) {
	ctx := context.Background()
	zones := newMemDNSZoneRepo(testDNSZone())
	recs := newMemDNSRecordRepo()

	orig, err := dnsRecordAdd(ctx, zones, recs, "example.com", dnsRecordSpec{
		Name: "_sip._tcp", Type: "SRV", Content: "10 5 5060 sip.example.com", Enabled: true,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Update the content to a new four-field value; it must normalise too.
	upd, err := dnsRecordUpdate(ctx, zones, recs, "example.com", orig.ID,
		dnsRecordSpec{Content: "30 1 443 svc.example.com"},
		map[string]bool{"content": true})
	if err != nil {
		t.Fatalf("update SRV: %v", err)
	}
	if upd.Content != "1 443 svc.example.com" {
		t.Errorf("updated content should be three fields, got %q", upd.Content)
	}
	if upd.Priority != 30 {
		t.Errorf("updated priority should be split out (30), got %d", upd.Priority)
	}
}
