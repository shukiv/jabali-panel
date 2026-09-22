package reconciler

// JAB-407 — a panel→agent read timeout on ssl.issue_dns01 is NOT proof of
// failure: certbot runs detached and may have finished issuing agent-side. The
// reconciler re-checks the lineage via ssl.cert_info and records success for a
// FRESH cert (no self-sign fallback, no 5-minute backoff, no acmeMaxRetries
// tick), while every other outcome — stale cert, no cert, unknown command, a
// non-timeout error — falls back exactly as before so a genuine failure still
// counts toward the LE rate-limit cap.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
)

// deadlineErr mimics the agent client's wrapped socket read timeout so
// isAgentDeadlineError (errors.Is against os.ErrDeadlineExceeded) matches.
func deadlineErr() error { return fmt.Errorf("agent: read: %w", os.ErrDeadlineExceeded) }

// shrinkDNS01Grace makes the post-timeout confirmation poll run instantly.
func shrinkDNS01Grace(t *testing.T) {
	t.Helper()
	og, op := dns01PostTimeoutGrace, dns01PostTimeoutPoll
	dns01PostTimeoutGrace = 60 * time.Millisecond
	dns01PostTimeoutPoll = 2 * time.Millisecond
	t.Cleanup(func() { dns01PostTimeoutGrace = og; dns01PostTimeoutPoll = op })
}

func certInfoJSON(exists, covers bool, notBefore, notAfter time.Time) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"exists":      exists,
		"covers_sans": covers,
		"not_before":  notBefore.UTC().Format(time.RFC3339),
		"not_after":   notAfter.UTC().Format(time.RFC3339),
		"cert_path":   "/etc/letsencrypt/live/sub.example.com/fullchain.pem",
		"key_path":    "/etc/letsencrypt/live/sub.example.com/privkey.pem",
	})
	return b
}

func TestDNS01_Timeout_FreshCert_RecordsSuccessNoBackoff(t *testing.T) {
	shrinkDNS01Grace(t)
	r, ag, sc, dom := dns01Fixture(t, []string{"kip.ns.cloudflare.com"}, "zone123")
	ag.errByMethod = map[string]error{"ssl.issue_dns01": deadlineErr()}
	ag.resultByMethod["ssl.cert_info"] = certInfoJSON(true, true, time.Now().Add(-time.Minute), time.Now().Add(90*24*time.Hour))

	r.reconcileSSLForDomain(context.Background(), dom)

	if sc.acmeFailures != 0 {
		t.Fatalf("a confirmed post-timeout issuance must NOT record an ACME failure/backoff; got %d", sc.acmeFailures)
	}
	if sc.issueMethods["c1"] != issueMethodDNS01 {
		t.Errorf("issue_method = %q, want %q — the success path must record the confirmed cert", sc.issueMethods["c1"], issueMethodDNS01)
	}
	if _, ok := findAgentCall(ag, "ssl.cert_info"); !ok {
		t.Error("expected a ssl.cert_info re-check after the read timeout")
	}
}

func TestDNS01_Timeout_StaleCert_FallsBack(t *testing.T) {
	shrinkDNS01Grace(t)
	r, ag, sc, dom := dns01Fixture(t, []string{"kip.ns.cloudflare.com"}, "zone123")
	ag.errByMethod = map[string]error{"ssl.issue_dns01": deadlineErr()}
	// A valid cert exists but its NotBefore is 60 days ago — a failed RENEWAL
	// (tryDNS01OrPark is the renewal path too). It must NOT count as this
	// attempt's success. Dropping the freshness check makes this go RED.
	ag.resultByMethod["ssl.cert_info"] = certInfoJSON(true, true, time.Now().Add(-60*24*time.Hour), time.Now().Add(30*24*time.Hour))

	r.reconcileSSLForDomain(context.Background(), dom)

	if sc.acmeFailures != 1 {
		t.Fatalf("a stale (non-fresh) cert must fall back and record a failure; got %d", sc.acmeFailures)
	}
	if sc.issueMethods["c1"] == issueMethodDNS01 {
		t.Error("a stale cert must NOT be recorded as a fresh issuance")
	}
}

func TestDNS01_Timeout_NoCert_FallsBack(t *testing.T) {
	shrinkDNS01Grace(t)
	r, ag, sc, dom := dns01Fixture(t, []string{"kip.ns.cloudflare.com"}, "zone123")
	ag.errByMethod = map[string]error{"ssl.issue_dns01": deadlineErr()}
	ag.resultByMethod["ssl.cert_info"] = certInfoJSON(false, false, time.Time{}, time.Time{})

	r.reconcileSSLForDomain(context.Background(), dom)

	if sc.acmeFailures != 1 {
		t.Fatalf("no cert on the lineage must fall back; got %d ACME failures", sc.acmeFailures)
	}
}

func TestDNS01_Timeout_OldAgentUnknownCommand_FallsBack(t *testing.T) {
	shrinkDNS01Grace(t)
	r, ag, sc, dom := dns01Fixture(t, []string{"kip.ns.cloudflare.com"}, "zone123")
	ag.errByMethod = map[string]error{
		"ssl.issue_dns01": deadlineErr(),
		"ssl.cert_info":   &agent.AgentError{Code: agent.CodeUnknownCommand, Message: "no handler for command \"ssl.cert_info\""},
	}

	r.reconcileSSLForDomain(context.Background(), dom)

	if sc.acmeFailures != 1 {
		t.Fatalf("an agent that does not know ssl.cert_info must fall back (version skew); got %d", sc.acmeFailures)
	}
	if _, ok := findAgentCall(ag, "ssl.cert_info"); !ok {
		t.Error("expected one ssl.cert_info probe before bailing to fallback")
	}
}

func TestDNS01_RealFailureNotTimeout_NoRecheck(t *testing.T) {
	shrinkDNS01Grace(t)
	r, ag, sc, dom := dns01Fixture(t, []string{"kip.ns.cloudflare.com"}, "zone123")
	ag.errByMethod = map[string]error{"ssl.issue_dns01": errBoom} // a real failure, not a deadline

	r.reconcileSSLForDomain(context.Background(), dom)

	if _, ok := findAgentCall(ag, "ssl.cert_info"); ok {
		t.Error("a non-timeout issuance failure must NOT trigger a ssl.cert_info re-check")
	}
	if sc.acmeFailures != 1 {
		t.Fatalf("a real failure must record a backoff exactly as before; got %d", sc.acmeFailures)
	}
}
