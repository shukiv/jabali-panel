package reconciler

// JAB-407 (shared-cert path) — reconcileAcmeSharedCerts makes the same
// synchronous ssl.issue_dns01 call as the per-domain path, so it inherits the
// same read-timeout class: certbot runs detached and may finish issuing after
// the panel gives up. A deadline error now triggers an ssl.cert_info re-check;
// a FRESH cert is recorded as issued (no MarkACMEAttempt, no backoff), every
// other outcome falls through to the unchanged failure path.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
)

// timeoutSharedAgent errors ssl.issue_dns01 and serves a canned ssl.cert_info,
// mirroring the errByMethod/resultByMethod fake the per-domain timeout tests use.
type timeoutSharedAgent struct {
	issueErr    error
	certInfo    json.RawMessage
	certInfoErr error
	calls       []string
}

func (a *timeoutSharedAgent) Call(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
	a.calls = append(a.calls, method)
	switch method {
	case "ssl.issue_dns01":
		return nil, a.issueErr
	case "ssl.cert_info":
		if a.certInfoErr != nil {
			return nil, a.certInfoErr
		}
		return a.certInfo, nil
	}
	return nil, nil
}

func (a *timeoutSharedAgent) called(m string) bool {
	for _, c := range a.calls {
		if c == m {
			return true
		}
	}
	return false
}

func sharedTimeoutReconciler(repo *fakeAcmeSharedRepo, ag *timeoutSharedAgent) *Reconciler {
	r := testReconcilerFor(repo, nil)
	r.agent = ag
	return r
}

func TestAcmeShared_Timeout_FreshCert_RecordsSuccessNoBackoff(t *testing.T) {
	shrinkDNS01Grace(t)
	repo := newFakeAcmeSharedRepo(acmeRow("C1", nil, nil))
	ag := &timeoutSharedAgent{
		issueErr: deadlineErr(),
		certInfo: certInfoJSON(true, true, time.Now().Add(-time.Minute), time.Now().Add(90*24*time.Hour)),
	}
	sharedTimeoutReconciler(repo, ag).reconcileAcmeSharedCerts(context.Background())

	if repo.issued["C1"] == "" {
		t.Fatal("a confirmed post-timeout issuance must be recorded via UpdateACMEIssued with the cert path")
	}
	if _, ok := repo.attempts["C1"]; ok {
		t.Error("a confirmed issuance must NOT record a failed attempt/backoff")
	}
	if !ag.called("ssl.cert_info") {
		t.Error("expected a ssl.cert_info re-check after the timeout")
	}
}

func TestAcmeShared_Timeout_StaleCert_FallsBack(t *testing.T) {
	shrinkDNS01Grace(t)
	repo := newFakeAcmeSharedRepo(acmeRow("C1", nil, nil))
	// Valid cert, but issued 60 days ago (a failed renewal) — not this attempt's.
	// Dropping the freshness check makes this go RED.
	ag := &timeoutSharedAgent{
		issueErr: deadlineErr(),
		certInfo: certInfoJSON(true, true, time.Now().Add(-60*24*time.Hour), time.Now().Add(30*24*time.Hour)),
	}
	sharedTimeoutReconciler(repo, ag).reconcileAcmeSharedCerts(context.Background())

	if _, ok := repo.issued["C1"]; ok {
		t.Error("a stale cert must NOT be recorded as a fresh issuance")
	}
	if _, ok := repo.attempts["C1"]; !ok {
		t.Fatal("a stale cert must fall back to MarkACMEAttempt")
	}
}

func TestAcmeShared_Timeout_NoCert_FallsBack(t *testing.T) {
	shrinkDNS01Grace(t)
	repo := newFakeAcmeSharedRepo(acmeRow("C1", nil, nil))
	ag := &timeoutSharedAgent{
		issueErr: deadlineErr(),
		certInfo: certInfoJSON(false, false, time.Time{}, time.Time{}),
	}
	sharedTimeoutReconciler(repo, ag).reconcileAcmeSharedCerts(context.Background())

	if _, ok := repo.issued["C1"]; ok {
		t.Error("no cert on the lineage must NOT be recorded as issued")
	}
	if _, ok := repo.attempts["C1"]; !ok {
		t.Fatal("no cert on the lineage must fall back to MarkACMEAttempt")
	}
}

func TestAcmeShared_Timeout_OldAgentUnknownCommand_FallsBack(t *testing.T) {
	shrinkDNS01Grace(t)
	repo := newFakeAcmeSharedRepo(acmeRow("C1", nil, nil))
	ag := &timeoutSharedAgent{
		issueErr:    deadlineErr(),
		certInfoErr: &agent.AgentError{Code: agent.CodeUnknownCommand, Message: "no handler for command \"ssl.cert_info\""},
	}
	sharedTimeoutReconciler(repo, ag).reconcileAcmeSharedCerts(context.Background())

	if _, ok := repo.attempts["C1"]; !ok {
		t.Fatal("an agent that does not know ssl.cert_info must fall back (version skew)")
	}
	if !ag.called("ssl.cert_info") {
		t.Error("expected one ssl.cert_info probe before bailing to fallback")
	}
}

func TestAcmeShared_RealFailureNotTimeout_NoRecheck(t *testing.T) {
	shrinkDNS01Grace(t)
	repo := newFakeAcmeSharedRepo(acmeRow("C1", nil, nil))
	ag := &timeoutSharedAgent{issueErr: errBoom} // a real failure, not a deadline
	sharedTimeoutReconciler(repo, ag).reconcileAcmeSharedCerts(context.Background())

	if ag.called("ssl.cert_info") {
		t.Error("a non-timeout failure must NOT trigger a ssl.cert_info re-check")
	}
	if _, ok := repo.attempts["C1"]; !ok {
		t.Fatal("a real failure must record a failed attempt exactly as before")
	}
}
