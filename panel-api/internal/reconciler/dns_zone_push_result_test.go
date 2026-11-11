package reconciler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// GH #2377: after a migration the DNS Zones page showed every zone as in
// place, while PowerDNS's domains table had none of them. The page only knew
// that a dns_zones row existed; a failed dns.zone.upsert was logged and
// retried, and nothing a person could see recorded it. The reconciler now
// records the outcome of every push on the zone row.

const pdnsDenied = "powerdns: Error 1045 (28000): Access denied for user 'pdns'@'localhost' (using password: YES)"

func TestReconcileDNSZone_FailedPushIsRecordedThenClearedBySuccess(t *testing.T) {
	r, agent, zones, _ := dnsModuleFixture(t, true, true)
	ctx := context.Background()
	agent.errByMethod = map[string]error{"dns.zone.upsert": errors.New(pdnsDenied)}

	if err := r.ReconcileAll(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countMethod(agent, "dns.zone.upsert"); got != 1 {
		t.Fatalf("dns.zone.upsert calls = %d, want 1", got)
	}
	pushedAt, pushErr, _ := zones.pushState("domain-1")
	if pushErr == nil || !strings.Contains(*pushErr, "Access denied") {
		t.Fatalf("a failed push must be recorded on the zone row; last_push_error = %v", derefPushErr(pushErr))
	}
	if pushedAt != nil {
		t.Fatalf("the zone never reached PowerDNS, but last_pushed_at = %v", *pushedAt)
	}

	// The cause is fixed (credentials restored, agent restarted): the next
	// pass pushes again, because nothing was stamped, and the row says so.
	agent.set(func(f *fakeAgent) { f.errByMethod = nil })
	if err := r.ReconcileAll(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countMethod(agent, "dns.zone.upsert"); got != 2 {
		t.Fatalf("dns.zone.upsert calls = %d, want 2 (the failed push is retried)", got)
	}
	pushedAt, pushErr, _ = zones.pushState("domain-1")
	if pushedAt == nil {
		t.Fatal("a successful push must stamp last_pushed_at")
	}
	if pushErr != nil {
		t.Fatalf("a successful push must clear last_push_error, got %q", *pushErr)
	}
}

func TestReconcileDNSZone_SameFailureIsWrittenOnce(t *testing.T) {
	// A push that keeps failing is retried every pass. The row must not be
	// rewritten every pass with the same error.
	r, agent, zones, _ := dnsModuleFixture(t, true, true)
	ctx := context.Background()
	agent.errByMethod = map[string]error{"dns.zone.upsert": errors.New(pdnsDenied)}

	for i := 0; i < 3; i++ {
		if err := r.ReconcileAll(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := countMethod(agent, "dns.zone.upsert"); got != 3 {
		t.Fatalf("dns.zone.upsert calls = %d, want 3", got)
	}
	if _, _, writes := zones.pushState("domain-1"); writes != 1 {
		t.Errorf("push-result writes = %d, want 1 for one unchanged error", writes)
	}
}

func TestReconcileDNSZone_LongErrorIsCut(t *testing.T) {
	r, agent, zones, _ := dnsModuleFixture(t, true, true)
	agent.errByMethod = map[string]error{"dns.zone.upsert": errors.New(strings.Repeat("é", 5000))}

	if err := r.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, pushErr, _ := zones.pushState("domain-1")
	if pushErr == nil {
		t.Fatal("expected the failure to be recorded")
	}
	if n := len(*pushErr); n > maxPushErrorBytes {
		t.Errorf("recorded error is %d bytes, want at most %d", n, maxPushErrorBytes)
	}
	if !strings.HasSuffix(*pushErr, "…") {
		t.Errorf("a cut error must end with an ellipsis")
	}
	if !utf8.ValidString(*pushErr) {
		t.Fatal("the cut split a UTF-8 sequence")
	}
}

func TestReconcileDNSZone_ModuleOffRecordsNothing(t *testing.T) {
	// With the DNS module off nothing is pushed, so nothing is recorded: the
	// page shows the zone as pending, not as on the server or as failed.
	r, _, zones, _ := dnsModuleFixture(t, false, true)
	if err := r.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	pushedAt, pushErr, writes := zones.pushState("domain-1")
	if pushedAt != nil || pushErr != nil || writes != 0 {
		t.Errorf("DNS module off: pushed_at=%v err=%v writes=%d, want nothing recorded", pushedAt, derefPushErr(pushErr), writes)
	}
}

func derefPushErr(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
