package reconciler

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/services"
)

func selfSignTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// makeSelfSignPEM builds a PEM cert with the given issuer/subject Org, CN and
// DNS SANs — a stand-in for a cert on disk. org=="" → empty issuer Org.
func makeSelfSignPEM(t *testing.T, org, cn string, dnsSANs []string, notAfter time.Time) []byte {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	subj := pkix.Name{CommonName: cn}
	if org != "" {
		subj.Organization = []string{org}
	}
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               subj,
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              dnsSANs,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func newSelfSignReconciler(agent *fakeAgent, certPEM []byte, readErr error) *Reconciler {
	return &Reconciler{
		agent: agent,
		log:   selfSignTestLogger(),
		readCertFile: func(string) ([]byte, error) {
			if readErr != nil {
				return nil, readErr
			}
			return certPEM, nil
		},
	}
}

func countCalls(fa *fakeAgent, method string) (int, map[string]any) {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	var n int
	var last map[string]any
	for _, c := range fa.calls {
		if c.method == method {
			n++
			if m, ok := c.params.(map[string]any); ok {
				last = m
			}
		}
	}
	return n, last
}

func TestPanelSelfSignedDrift_CNMismatchDispatches(t *testing.T) {
	pem := makeSelfSignPEM(t, panelSelfSignOrgMarker, "old.example.com",
		[]string{"old.example.com", "mail.old.example.com", "localhost"}, time.Now().AddDate(5, 0, 0))
	fa := &fakeAgent{}
	r := newSelfSignReconciler(fa, pem, nil)
	row := &models.PanelCertificate{CertPEMPath: "/etc/jabali/tls/panel.crt"}

	if drift, _ := r.panelSelfSignedCertDrifted(row.CertPEMPath, "new.example.com"); !drift {
		t.Fatalf("expected drift for CN mismatch")
	}
	r.reconcilePanelSelfSignedCert(context.Background(), row, "new.example.com", "203.0.113.9")

	n, params := countCalls(fa, "ssl.panel.selfsign")
	if n != 1 {
		t.Fatalf("expected 1 ssl.panel.selfsign dispatch, got %d", n)
	}
	if params["hostname"] != "new.example.com" || params["ip"] != "203.0.113.9" {
		t.Fatalf("dispatch params wrong: %v", params)
	}
}

func TestPanelSelfSignedDrift_MissingSANDispatches(t *testing.T) {
	// CN matches but the mail.<hostname> SAN is absent (pre-M6.4 style cert).
	pem := makeSelfSignPEM(t, panelSelfSignOrgMarker, "host.example.com",
		[]string{"host.example.com", "localhost"}, time.Now().AddDate(5, 0, 0))
	fa := &fakeAgent{}
	r := newSelfSignReconciler(fa, pem, nil)
	if drift, reason := r.panelSelfSignedCertDrifted("/x", "host.example.com"); !drift {
		t.Fatalf("expected drift for missing mail SAN, reason=%q", reason)
	}
}

func TestPanelSelfSignedDrift_CoveredNoDispatch(t *testing.T) {
	pem := makeSelfSignPEM(t, panelSelfSignOrgMarker, "host.example.com",
		[]string{"host.example.com", "mail.host.example.com", "localhost"}, time.Now().AddDate(5, 0, 0))
	fa := &fakeAgent{}
	r := newSelfSignReconciler(fa, pem, nil)
	row := &models.PanelCertificate{CertPEMPath: "/etc/jabali/tls/panel.crt"}

	if drift, _ := r.panelSelfSignedCertDrifted(row.CertPEMPath, "host.example.com"); drift {
		t.Fatalf("did not expect drift for a covering cert")
	}
	r.reconcilePanelSelfSignedCert(context.Background(), row, "host.example.com", "")
	if n, _ := countCalls(fa, "ssl.panel.selfsign"); n != 0 {
		t.Fatalf("expected no dispatch, got %d", n)
	}
}

// LE cert (issuer O="Let's Encrypt") must be preserved even in self-signed
// mode: never reported as drift, never dispatched over.
func TestPanelSelfSignedDrift_PreservesLECert(t *testing.T) {
	pem := makeSelfSignPEM(t, "Let's Encrypt", "old.example.com",
		[]string{"old.example.com"}, time.Now().AddDate(0, 3, 0))
	fa := &fakeAgent{}
	r := newSelfSignReconciler(fa, pem, nil)
	row := &models.PanelCertificate{CertPEMPath: "/etc/jabali/tls/panel.crt"}

	if drift, _ := r.panelSelfSignedCertDrifted(row.CertPEMPath, "new.example.com"); drift {
		t.Fatalf("LE cert reported as drift — must be preserved")
	}
	r.reconcilePanelSelfSignedCert(context.Background(), row, "new.example.com", "")
	if n, _ := countCalls(fa, "ssl.panel.selfsign"); n != 0 {
		t.Fatalf("dispatched over an LE cert (%d calls) — must be preserved", n)
	}
}

// GH #2166: with Let's Encrypt off, an EXPIRED Let's Encrypt cert is not a
// live cert to preserve; the panel replaces it with a self-signed one (the
// toggle keeps an LE cert "until expiry", not forever).
func TestPanelSelfSignedDrift_ExpiredLECertDispatches(t *testing.T) {
	pem := makeSelfSignPEM(t, "Let's Encrypt", "host.example.com",
		[]string{"host.example.com"}, time.Now().Add(-time.Hour))
	fa := &fakeAgent{}
	r := newSelfSignReconciler(fa, pem, nil)
	row := &models.PanelCertificate{CertPEMPath: "/etc/jabali/tls/panel.crt"}
	if drift, reason := r.panelSelfSignedCertDrifted(row.CertPEMPath, "host.example.com"); !drift {
		t.Fatalf("expired LE cert not reported as drift, reason=%q", reason)
	}
	r.reconcilePanelSelfSignedCert(context.Background(), row, "host.example.com", "")
	if n, _ := countCalls(fa, "ssl.panel.selfsign"); n != 1 {
		t.Fatalf("expected a dispatch over the expired LE cert, got %d", n)
	}
}

// GH #2166: in self-signed mode the reconciler keeps the hostname row in line
// with the cert on disk. The reporter's row kept a Let's Encrypt retry and
// "not routable" from manual attempts made while Let's Encrypt was off.
func TestPanelSelfSigned_RowFollowsTheCert(t *testing.T) {
	future := time.Now().AddDate(0, 2, 0)
	for _, tc := range []struct {
		name       string
		pem        []byte
		wantStatus string
		wantExpiry bool
	}{
		{name: "current self-signed cert", wantStatus: models.PanelCertStatusSelfSigned,
			pem: makeSelfSignPEM(t, panelSelfSignOrgMarker, "host.example.com",
				[]string{"host.example.com", "mail.host.example.com"}, time.Now().AddDate(5, 0, 0))},
		{name: "live Let's Encrypt cert", wantStatus: models.PanelCertStatusIssued, wantExpiry: true,
			pem: makeSelfSignPEM(t, "Let's Encrypt", "host.example.com", []string{"host.example.com"}, future)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			retry := time.Now().Add(time.Hour)
			repo := &fakePanelCertRepo{rows: map[string]*models.PanelCertificate{
				models.PanelCertKindHostname: {
					Kind: models.PanelCertKindHostname, Status: models.PanelCertStatusPendingACMERetry,
					LastError: "not routable: dns points elsewhere", AttemptCount: 4, NextRetryAt: &retry,
				},
			}}
			fa := &fakeAgent{}
			r := newSelfSignReconciler(fa, tc.pem, nil)
			r.panelCerts = repo
			row, _ := repo.GetByKind(context.Background(), models.PanelCertKindHostname)

			r.reconcilePanelSelfSignedCert(context.Background(), row, "host.example.com", "")

			got := repo.rows[models.PanelCertKindHostname]
			if got.Status != tc.wantStatus || got.LastError != "" || got.NextRetryAt != nil || got.AttemptCount != 0 {
				t.Fatalf("row = %+v, want %s with no Let's Encrypt retry state", got, tc.wantStatus)
			}
			if tc.wantExpiry && (got.ExpiresAt == nil || got.ExpiresAt.Sub(future).Abs() > 2*time.Second) {
				t.Errorf("expires_at = %v, want the kept cert's %v", got.ExpiresAt, future)
			}
			if n, _ := countCalls(fa, "ssl.panel.selfsign"); n != 0 {
				t.Errorf("dispatched %d times for a cert that needs no regenerate", n)
			}
		})
	}
}

func TestPanelSelfSignedDrift_MissingCertDispatches(t *testing.T) {
	fa := &fakeAgent{}
	r := newSelfSignReconciler(fa, nil, os.ErrNotExist)
	row := &models.PanelCertificate{CertPEMPath: "/etc/jabali/tls/panel.crt"}
	if drift, _ := r.panelSelfSignedCertDrifted(row.CertPEMPath, "host.example.com"); !drift {
		t.Fatalf("expected drift for missing cert")
	}
	r.reconcilePanelSelfSignedCert(context.Background(), row, "host.example.com", "")
	if n, _ := countCalls(fa, "ssl.panel.selfsign"); n != 1 {
		t.Fatalf("expected dispatch for missing cert, got %d", n)
	}
}

func TestPanelSelfSignedDrift_ExpiredCoveringCertDispatches(t *testing.T) {
	// Cert covers the FQDN but is expired → must self-heal (symmetry with the
	// agent's no-churn expiry check).
	pem := makeSelfSignPEM(t, panelSelfSignOrgMarker, "host.example.com",
		[]string{"host.example.com", "mail.host.example.com", "localhost"}, time.Now().Add(-time.Hour))
	fa := &fakeAgent{}
	r := newSelfSignReconciler(fa, pem, nil)
	row := &models.PanelCertificate{CertPEMPath: "/etc/jabali/tls/panel.crt"}
	if drift, reason := r.panelSelfSignedCertDrifted(row.CertPEMPath, "host.example.com"); !drift {
		t.Fatalf("expected drift for expired cert, reason=%q", reason)
	}
	r.reconcilePanelSelfSignedCert(context.Background(), row, "host.example.com", "")
	if n, _ := countCalls(fa, "ssl.panel.selfsign"); n != 1 {
		t.Fatalf("expected dispatch for expired cert, got %d", n)
	}
}

// mailSelfSignReconciler: Let's Encrypt off for the panel (the hostname row's
// use_le), the hostname cert current, and the given mail cert on disk. The
// mail row's own use_le is on (stale: the toggle writes only the hostname
// row) and its last Let's Encrypt attempt failed.
func mailSelfSignReconciler(t *testing.T, mailPEM []byte, emailOnPanel bool) (*Reconciler, *fakeAgent, *fakePanelCertRepo) {
	t.Helper()
	hostPEM := makeSelfSignPEM(t, panelSelfSignOrgMarker, "host.example.com",
		[]string{"host.example.com", "mail.host.example.com"}, time.Now().AddDate(5, 0, 0))
	retry := time.Now().Add(time.Hour)
	repo := &fakePanelCertRepo{rows: map[string]*models.PanelCertificate{
		models.PanelCertKindHostname: {Kind: models.PanelCertKindHostname, Hostname: "host.example.com",
			CertPEMPath: "/etc/jabali/tls/panel.crt", Status: models.PanelCertStatusSelfSigned},
		models.PanelCertKindMail: {Kind: models.PanelCertKindMail, Hostname: "mail.host.example.com", UseLE: true,
			CertPEMPath: "/etc/jabali/tls/panel-mail.crt", Status: models.PanelCertStatusPendingACMERetry,
			LastError: "not routable: dns points elsewhere", AttemptCount: 2, NextRetryAt: &retry},
	}}
	fa := &fakeAgent{}
	r := &Reconciler{
		agent:                fa,
		log:                  selfSignTestLogger(),
		panelCerts:           repo,
		panelCertRoutability: &services.PanelCertRoutability{},
		serverSettings:       &fakeSettingsRepo{srv: &models.ServerSettings{Hostname: "host.example.com", PublicIPv4: "203.0.113.10", MailEnabled: true}},
		domains: &fakeDomainRepo{domains: map[string]*models.Domain{
			"d1": {ID: "d1", Name: "host.example.com", IsPanelPrimary: true, EmailEnabled: emailOnPanel},
		}},
		readCertFile: func(path string) ([]byte, error) {
			switch path {
			case "/etc/jabali/tls/panel.crt":
				return hostPEM, nil
			case "/etc/jabali/tls/panel-mail.crt":
				if mailPEM == nil {
					return nil, os.ErrNotExist
				}
				return mailPEM, nil
			}
			return nil, os.ErrNotExist
		},
	}
	return r, fa, repo
}

// GH #2166: the mail cert follows the toggle. With Let's Encrypt off, a
// missing mail cert is regenerated self-signed for the mail names, and the
// mail row loses its Let's Encrypt retry state, whatever its own use_le.
func TestPanelMailSelfSigned_LEOffRegenerates(t *testing.T) {
	r, fa, repo := mailSelfSignReconciler(t, nil, true)
	r.reconcilePanelCertificate(context.Background())

	n, params := countCalls(fa, "ssl.panel.mail_selfsign")
	if n != 1 {
		t.Fatalf("ssl.panel.mail_selfsign dispatched %d times, want 1", n)
	}
	if names, _ := params["names"].([]string); len(names) != 1 || names[0] != "mail.host.example.com" {
		t.Errorf("names %v", params["names"])
	}
	if n, _ := countCalls(fa, "ssl.panel.selfsign"); n != 0 {
		t.Errorf("the current hostname cert was regenerated")
	}
	mail := repo.rows[models.PanelCertKindMail]
	if mail.Status != models.PanelCertStatusSelfSigned || mail.LastError != "" || mail.NextRetryAt != nil || mail.AttemptCount != 0 {
		t.Errorf("mail row %+v, want self_signed with no Let's Encrypt retry state", mail)
	}
}

// A live Let's Encrypt mail cert is kept until it expires; the row shows it.
func TestPanelMailSelfSigned_KeepsLiveLECert(t *testing.T) {
	exp := time.Now().AddDate(0, 2, 0)
	r, fa, repo := mailSelfSignReconciler(t,
		makeSelfSignPEM(t, "Let's Encrypt", "mail.host.example.com", []string{"mail.host.example.com"}, exp), true)
	r.reconcilePanelCertificate(context.Background())

	if n, _ := countCalls(fa, "ssl.panel.mail_selfsign"); n != 0 {
		t.Fatalf("dispatched for a live CA cert")
	}
	mail := repo.rows[models.PanelCertKindMail]
	if mail.Status != models.PanelCertStatusIssued || mail.ExpiresAt == nil || mail.ExpiresAt.Sub(exp).Abs() > 2*time.Second || mail.LastError != "" {
		t.Errorf("mail row %+v, want issued with the kept cert's expiry", mail)
	}
}

// No churn: a current self-signed mail cert means no dispatch, so Stalwart
// isn't restarted every tick.
func TestPanelMailSelfSigned_CoveredNoDispatch(t *testing.T) {
	r, fa, _ := mailSelfSignReconciler(t,
		makeSelfSignPEM(t, panelSelfSignOrgMarker, "mail.host.example.com", []string{"mail.host.example.com"}, time.Now().AddDate(5, 0, 0)), true)
	r.reconcilePanelCertificate(context.Background())
	if n, _ := countCalls(fa, "ssl.panel.mail_selfsign"); n != 0 {
		t.Fatalf("dispatched %d times for a current cert", n)
	}
}

// GH #295 gate: a panel hostname without email serves no mail, so its mail
// cert and row are left alone.
func TestPanelMailSelfSigned_SkipsWhenThePanelDoesNoMail(t *testing.T) {
	r, fa, repo := mailSelfSignReconciler(t, nil, false)
	r.reconcilePanelCertificate(context.Background())
	if n, _ := countCalls(fa, "ssl.panel.mail_selfsign"); n != 0 {
		t.Fatalf("dispatched with email off on the panel hostname")
	}
	if repo.rows[models.PanelCertKindMail].Status != models.PanelCertStatusPendingACMERetry {
		t.Errorf("mail row changed: %+v", repo.rows[models.PanelCertKindMail])
	}
}

// The mail cert goes first: the hostname's regenerate restarts the panel,
// which cuts the tick short.
func TestPanelMailSelfSigned_BeforeTheHostname(t *testing.T) {
	r, fa, _ := mailSelfSignReconciler(t, nil, true)
	r.readCertFile = func(string) ([]byte, error) { return nil, os.ErrNotExist }
	r.reconcilePanelCertificate(context.Background())

	fa.mu.Lock()
	defer fa.mu.Unlock()
	var order []string
	for _, c := range fa.calls {
		order = append(order, c.method)
	}
	if len(order) != 2 || order[0] != "ssl.panel.mail_selfsign" || order[1] != "ssl.panel.selfsign" {
		t.Fatalf("calls %v, want the mail cert first", order)
	}
}

// With Let's Encrypt on, the self-signed path doesn't run for mail.
func TestPanelMailSelfSigned_LEOnNoSelfSign(t *testing.T) {
	r, fa, repo := mailSelfSignReconciler(t, nil, true)
	repo.rows[models.PanelCertKindHostname].UseLE = true
	repo.rows[models.PanelCertKindHostname].Status = models.PanelCertStatusIssued
	repo.rows[models.PanelCertKindMail].Status = models.PanelCertStatusIssued
	r.serverSettings = &fakeSettingsRepo{srv: &models.ServerSettings{Hostname: "host.example.com", AdminEmail: "a@example.com", MailEnabled: true}}
	r.reconcilePanelCertificate(context.Background())
	if n, _ := countCalls(fa, "ssl.panel.mail_selfsign"); n != 0 {
		t.Fatalf("mail self-signed with Let's Encrypt on")
	}
}

// GH #2166: with the mail module off there is no mail server to give the
// certificate, even when the panel hostname's domain still has email on.
// Pushing one started a Stalwart that wasn't installed, and every failed
// start was a "Stalwart is down" alert.
func TestPanelMailSelfSigned_SkipsWithTheMailModuleOff(t *testing.T) {
	r, fa, repo := mailSelfSignReconciler(t, nil, true)
	r.serverSettings = &fakeSettingsRepo{srv: &models.ServerSettings{Hostname: "host.example.com", PublicIPv4: "203.0.113.10"}}
	r.reconcilePanelCertificate(context.Background())
	if n, _ := countCalls(fa, "ssl.panel.mail_selfsign"); n != 0 {
		t.Fatalf("dispatched with the mail module off")
	}
	if repo.rows[models.PanelCertKindMail].Status != models.PanelCertStatusPendingACMERetry {
		t.Errorf("mail row changed: %+v", repo.rows[models.PanelCertKindMail])
	}
}

// upsertRecordingPanelCerts records Upserts, which fakePanelCertRepo drops.
type upsertRecordingPanelCerts struct {
	*fakePanelCertRepo
	upserted []models.PanelCertificate
}

func (u *upsertRecordingPanelCerts) Upsert(_ context.Context, row *models.PanelCertificate) error {
	u.upserted = append(u.upserted, *row)
	return nil
}

// With Let's Encrypt on and the mail module off, the mail cert isn't
// requested either, and a stale retry row is idled with the reason.
func TestPanelMailCert_LEOnSkipsWithTheMailModuleOff(t *testing.T) {
	r, fa, repo := mailSelfSignReconciler(t, nil, true)
	repo.rows[models.PanelCertKindHostname].UseLE = true
	repo.rows[models.PanelCertKindHostname].Status = models.PanelCertStatusIssued
	rec := &upsertRecordingPanelCerts{fakePanelCertRepo: repo}
	r.panelCerts = rec
	r.serverSettings = &fakeSettingsRepo{srv: &models.ServerSettings{Hostname: "host.example.com", AdminEmail: "a@example.com"}}
	r.reconcilePanelCertificate(context.Background())

	fa.mu.Lock()
	for _, c := range fa.calls {
		if m, ok := c.params.(map[string]any); ok && m["hostname"] == "mail.host.example.com" {
			t.Errorf("%s dispatched for the mail cert with the mail module off", c.method)
		}
	}
	fa.mu.Unlock()
	var mail *models.PanelCertificate
	for i := range rec.upserted {
		if rec.upserted[i].Kind == models.PanelCertKindMail {
			mail = &rec.upserted[i]
		}
	}
	if mail == nil || mail.Status != models.PanelCertStatusSelfSigned || mail.NextRetryAt != nil || !strings.Contains(mail.LastError, "mail module is off") {
		t.Errorf("mail row upsert %+v, want idled with the mail-module reason", mail)
	}
}
