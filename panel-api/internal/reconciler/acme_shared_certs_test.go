package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type fakeAcmeSharedRepo struct {
	repository.SharedCertificateRepository
	due      []models.SharedCertificate
	issued   map[string]string // id -> cert_path recorded via UpdateACMEIssued
	attempts map[string]string // id -> last error message ("" = cleared)
}

func newFakeAcmeSharedRepo(due ...models.SharedCertificate) *fakeAcmeSharedRepo {
	return &fakeAcmeSharedRepo{due: due, issued: map[string]string{}, attempts: map[string]string{}}
}

func (f *fakeAcmeSharedRepo) ListACMEDue(_ context.Context, _ time.Time) ([]models.SharedCertificate, error) {
	return f.due, nil
}
func (f *fakeAcmeSharedRepo) MarkACMEAttempt(_ context.Context, id string, errMsg *string) error {
	if errMsg == nil {
		f.attempts[id] = ""
	} else {
		f.attempts[id] = *errMsg
	}
	return nil
}
func (f *fakeAcmeSharedRepo) UpdateACMEIssued(_ context.Context, id, certPath, _, _ string, _ time.Time, _ bool) error {
	f.issued[id] = certPath
	return nil
}

type fakeSettingsRepo struct {
	repository.ServerSettingsRepository
	srv *models.ServerSettings
}

func (f *fakeSettingsRepo) Get(_ context.Context) (*models.ServerSettings, error) {
	return f.srv, nil
}

type fakeDNS01Agent struct {
	calls []map[string]any
	fail  bool
}

func (f *fakeDNS01Agent) Call(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
	if method != "ssl.issue_dns01" {
		return nil, nil
	}
	m, _ := params.(map[string]any)
	f.calls = append(f.calls, m)
	if f.fail {
		return nil, fmt.Errorf("dns-01 issue failed (dns): no local zone")
	}
	return json.Marshal(map[string]any{
		"cert_path":  "/etc/letsencrypt/live/wildcard.example.com/fullchain.pem",
		"key_path":   "/etc/letsencrypt/live/wildcard.example.com/privkey.pem",
		"expires_at": time.Now().Add(90 * 24 * time.Hour).Format(time.RFC3339),
	})
}

func strPtr(s string) *string { return &s }

func acmeRow(id string, lastAttempt *time.Time, certPath *string) models.SharedCertificate {
	return models.SharedCertificate{
		ID:            id,
		Name:          "*.example.com",
		Source:        models.SharedCertSourceACME,
		SANs:          strPtr(`["example.com","*.example.com"]`),
		LastAttemptAt: lastAttempt,
		CertPath:      certPath,
	}
}

func testReconcilerFor(repo repository.SharedCertificateRepository, ag *fakeDNS01Agent) *Reconciler {
	return &Reconciler{
		sharedCerts:    repo,
		agent:          ag,
		serverSettings: &fakeSettingsRepo{srv: &models.ServerSettings{AdminEmail: "admin@example.com"}},
		log:            slog.New(slog.DiscardHandler),
	}
}

func TestReconcileAcmeSharedCerts_IssuesPendingRow(t *testing.T) {
	repo := newFakeAcmeSharedRepo(acmeRow("C1", nil, nil))
	ag := &fakeDNS01Agent{}
	r := testReconcilerFor(repo, ag)

	r.reconcileAcmeSharedCerts(context.Background())

	if len(ag.calls) != 1 {
		t.Fatalf("expected 1 agent call, got %d", len(ag.calls))
	}
	call := ag.calls[0]
	if call["cert_name"] != "wildcard.example.com" {
		t.Errorf("cert_name = %v — certbot forbids '*' in lineage names", call["cert_name"])
	}
	sans, _ := call["sans"].([]string)
	if len(sans) != 2 || sans[1] != "*.example.com" {
		t.Errorf("sans = %v, want [example.com *.example.com]", call["sans"])
	}
	if repo.issued["C1"] == "" {
		t.Error("successful issue was not recorded via UpdateACMEIssued")
	}
}

func TestReconcileAcmeSharedCerts_FailureRecordsError(t *testing.T) {
	repo := newFakeAcmeSharedRepo(acmeRow("C1", nil, nil))
	ag := &fakeDNS01Agent{fail: true}
	r := testReconcilerFor(repo, ag)

	r.reconcileAcmeSharedCerts(context.Background())

	if repo.attempts["C1"] == "" {
		t.Error("failed issue must record last_error via MarkACMEAttempt")
	}
	if len(repo.issued) != 0 {
		t.Error("nothing should be recorded as issued on failure")
	}
}

func TestReconcileAcmeSharedCerts_BackoffSkipsRecentFailure(t *testing.T) {
	recent := time.Now().Add(-time.Minute)
	repo := newFakeAcmeSharedRepo(acmeRow("C1", &recent, nil))
	ag := &fakeDNS01Agent{}
	r := testReconcilerFor(repo, ag)

	r.reconcileAcmeSharedCerts(context.Background())

	if len(ag.calls) != 0 {
		t.Errorf("row attempted %v ago must wait out the %v backoff", time.Minute, acmeSharedRetryBackoff)
	}
}

func TestReconcileAcmeSharedCerts_RenewalUsesLongerBackoff(t *testing.T) {
	attempt := time.Now().Add(-2 * time.Hour) // past retry backoff, inside renew backoff
	repo := newFakeAcmeSharedRepo(acmeRow("C1", &attempt, strPtr("/etc/letsencrypt/live/wildcard.example.com/fullchain.pem")))
	ag := &fakeDNS01Agent{}
	r := testReconcilerFor(repo, ag)

	r.reconcileAcmeSharedCerts(context.Background())

	if len(ag.calls) != 0 {
		t.Error("issued cert attempted 2h ago must respect the 24h renewal backoff")
	}
}

func TestAcmeCertLineageName(t *testing.T) {
	c := models.SharedCertificate{Name: "*.example.com"}
	if got := c.ACMELineageName(); got != "wildcard.example.com" {
		t.Errorf("got %q", got)
	}
	c.Name = "preview-cert.host.tld"
	if got := c.ACMELineageName(); got != "preview-cert.host.tld" {
		t.Errorf("non-wildcard name must pass through, got %q", got)
	}
}
