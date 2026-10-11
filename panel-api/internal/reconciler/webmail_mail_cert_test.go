package reconciler

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #2082: a domain whose website lives elsewhere (Wix, a CDN, another
// host) never gets a Let's Encrypt certificate of its own here — the apex
// does not point at this server, so it is parked on a self-signed
// placeholder. Its mail.<domain> name does point here, and the per-domain
// mail certificate (M6.6) covers it. The webmail vhost on mail.<domain>
// used the domain's certificate regardless, so webmail kept answering
// with the self-signed placeholder even after the mail certificate was
// issued. It now serves the domain's certificate only when that is a real
// certificate naming mail.<domain>, and otherwise the mail certificate while
// it is valid on disk.

// fakeMailCertRepo answers GetByDomain; the reconciler's webmail pass calls
// nothing else on it.
type fakeMailCertRepo struct {
	repository.MailCertificateRepository
	rows map[string]*models.MailCertificate
}

func (f *fakeMailCertRepo) GetByDomain(_ context.Context, domainID string) (*models.MailCertificate, error) {
	if row, ok := f.rows[domainID]; ok {
		return row, nil
	}
	return nil, repository.ErrNotFound
}

// writeNamedCert writes a self-signed fullchain.pem whose leaf names names.
func writeNamedCert(t *testing.T, names ...string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	p := filepath.Join(t.TempDir(), "fullchain.pem")
	require.NoError(t, os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644))
	return p
}

const mailLineage = "/etc/letsencrypt/live/mail.client.example"

func tenantWebmailVhostApply(t *testing.T, domainCert *models.SSLCertificate, mailCert *models.MailCertificate, wireMailCerts bool) map[string]any {
	t.Helper()
	ag := &paramsWebmailAgent{}
	dr := newFakeDomainRepo()
	dr.domains["d1"] = &models.Domain{OwnershipState: verifiedOwnership, ID: "d1", Name: "client.example", UserID: "u1",
		EmailEnabled: true, WebmailEnabled: true, WebDisabled: true}
	ur := &fakeUserRepo{users: map[string]*models.User{"u1": {ID: "u1"}}}
	certs := newFakeSSLCertRepo()
	if domainCert != nil {
		domainCert.DomainID = "d1"
		certs.byDomain["d1"] = domainCert
	}
	r := New(dr, ur, ag, slog.Default(), Config{}).WithSSLCerts(certs).WithPackages(&webmailGatePkgRepo{})
	if wireMailCerts {
		mc := &fakeMailCertRepo{rows: map[string]*models.MailCertificate{}}
		if mailCert != nil {
			mailCert.DomainID = "d1"
			mc.rows["d1"] = mailCert
		}
		r = r.WithMailCertificates(mc)
	}
	r.serverSettings = &fakeServerSettingsRepo{settings: &models.ServerSettings{WebmailEnabled: true, Hostname: "mx.jabali-panel.com"}}
	r.reconcileWebmailVhosts(context.Background())
	require.Len(t, ag.applies, 1, "one webmail.vhost_apply for the tenant domain")
	return ag.applies[0]
}

func TestTenantWebmailVhost_CertificateChoice(t *testing.T) {
	coversMail := func(t *testing.T) string {
		return writeNamedCert(t, "client.example", "www.client.example", "mail.client.example")
	}
	apexOnly := func(t *testing.T) string { return writeNamedCert(t, "client.example", "www.client.example") }
	future := time.Now().Add(60 * 24 * time.Hour)
	past := time.Now().Add(-time.Hour)
	mailCert := func(status string, expires *time.Time) *models.MailCertificate {
		return &models.MailCertificate{Status: status, LineagePath: mailLineage, ExpiresAt: expires}
	}
	issuedMail := func() *models.MailCertificate { return mailCert(models.MailCertStatusIssued, &future) }

	cases := []struct {
		name        string
		status      string
		certFile    func(t *testing.T) string // nil: a path that does not exist
		mailCert    *models.MailCertificate
		wire        bool
		wantLineage bool
	}{
		{"issued domain cert naming mail.<d> keeps serving", models.SSLStatusIssued, coversMail, issuedMail(), true, false},
		{"renewing domain cert naming mail.<d> keeps serving", models.SSLStatusRenewing, coversMail, issuedMail(), true, false},
		{"custom domain cert naming mail.<d> keeps serving", models.SSLStatusCustom, coversMail, issuedMail(), true, false},
		{"issued domain cert without mail.<d> yields to the mail cert", models.SSLStatusIssued, apexOnly, issuedMail(), true, true},
		{"self-signed placeholder naming mail.<d> yields to the mail cert", models.SSLStatusSelfSigned, coversMail, issuedMail(), true, true},
		{"parked domain (apex elsewhere) yields to the mail cert", models.SSLStatusPendingACMERetry, coversMail, issuedMail(), true, true},
		{"placeholder stays while the mail cert waits for DNS", models.SSLStatusSelfSigned, coversMail,
			&models.MailCertificate{Status: models.MailCertStatusDNSMissing}, true, false},
		{"an issued mail cert with no lineage is not used", models.SSLStatusSelfSigned, coversMail,
			&models.MailCertificate{Status: models.MailCertStatusIssued, ExpiresAt: &future}, true, false},
		// A renewal flips the row to issuing before the agent call, and a failed
		// one leaves it failed; the lineage on disk is still valid either way, so
		// webmail must not drop to the placeholder and back on every renewal.
		{"a mail cert being renewed keeps serving", models.SSLStatusSelfSigned, coversMail,
			mailCert(models.MailCertStatusIssuing, &future), true, true},
		{"a mail cert whose renewal failed keeps serving while valid", models.SSLStatusSelfSigned, coversMail,
			mailCert(models.MailCertStatusFailed, &future), true, true},
		{"an expired mail cert is not used", models.SSLStatusSelfSigned, coversMail,
			mailCert(models.MailCertStatusFailed, &past), true, false},
		{"a mail cert with no recorded expiry is not used", models.SSLStatusSelfSigned, coversMail,
			mailCert(models.MailCertStatusIssued, nil), true, false},
		{"a mail cert the operator disabled is not used", models.SSLStatusSelfSigned, coversMail,
			mailCert(models.MailCertStatusDisabled, &future), true, false},
		{"an unreadable issued domain cert is kept, as before", models.SSLStatusIssued, nil, issuedMail(), true, false},
		{"no mail certificate row", models.SSLStatusSelfSigned, coversMail, nil, true, false},
		{"mail certificates not wired", models.SSLStatusSelfSigned, coversMail, nil, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			certPath := filepath.Join(t.TempDir(), "missing", "fullchain.pem")
			if tc.certFile != nil {
				certPath = tc.certFile(t)
			}
			keyPath := filepath.Join(filepath.Dir(certPath), "privkey.pem")
			got := tenantWebmailVhostApply(t, &models.SSLCertificate{Status: tc.status, CertPath: &certPath, KeyPath: &keyPath},
				tc.mailCert, tc.wire)
			if tc.wantLineage {
				assert.Equal(t, mailLineage+"/fullchain.pem", got["ssl_cert_path"])
				assert.Equal(t, mailLineage+"/privkey.pem", got["ssl_key_path"])
			} else {
				assert.Equal(t, certPath, got["ssl_cert_path"])
				assert.Equal(t, keyPath, got["ssl_key_path"])
			}
		})
	}
}

// A domain with no certificate row yet still gets its webmail vhost once the
// mail certificate is issued: that certificate is the one mail.<domain> needs.
func TestTenantWebmailVhost_NoDomainCertUsesIssuedMailCert(t *testing.T) {
	future := time.Now().Add(60 * 24 * time.Hour)
	got := tenantWebmailVhostApply(t, nil, &models.MailCertificate{Status: models.MailCertStatusIssued, LineagePath: mailLineage, ExpiresAt: &future}, true)
	assert.Equal(t, mailLineage+"/fullchain.pem", got["ssl_cert_path"])
}
