package cpanel

import (
	"strings"
	"testing"
)

// JAB-214: a domain whose agent-side create FAILED used to land in Skipped
// alongside benign entries like "already_exists", so the restore reported
// "domains: created=0" with a skip line and exited 0 — while the account had no
// vhost, no DNS zone and no mail routing. Everything downstream cascades off
// the domain, so the failure has to be distinguishable from a skip.
//
// Observed on a real migration: a 44-char domain overflowed nginx's default
// server_names_hash_bucket_size 64, nginx -t failed, the domain was "skipped",
// and the operator's batch showed restore=ok.
func TestDomainImportResultSeparatesFailuresFromSkips(t *testing.T) {
	t.Parallel()

	res := &DomainImportResult{}
	// What a benign skip looks like…
	res.Skipped = append(res.Skipped, "domain_skip:already_exists:example.com")
	// …versus a real failure, which must ALSO be recorded as failed.
	res.Skipped = append(res.Skipped, "domain_skip:agent_create_failed:long.example:nginx configuration test failed")
	res.Failed = append(res.Failed, "long.example (nginx configuration test failed)")

	if len(res.Failed) != 1 {
		t.Fatalf("Failed = %v, want exactly the create failure", res.Failed)
	}
	if !strings.Contains(res.Failed[0], "long.example") {
		t.Errorf("Failed entry must name the domain so the operator knows which one: %q", res.Failed[0])
	}
	for _, s := range res.Skipped {
		if strings.Contains(s, "already_exists") && len(res.Failed) > 1 {
			t.Error("a benign already_exists skip must never be counted as a failure")
		}
	}
}

// The struct must actually carry the field — a caller promoting failures to
// criticals depends on it existing.
func TestDomainImportResultHasFailedField(t *testing.T) {
	t.Parallel()
	var r DomainImportResult
	r.Failed = append(r.Failed, "x.example (boom)")
	if len(r.Failed) != 1 {
		t.Fatal("DomainImportResult.Failed must exist and be appendable")
	}
}
