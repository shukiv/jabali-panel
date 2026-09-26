package reconciler

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// JAB-390: once a custom mail hostname is applied, the panel-primary mail
// vhost also answers it (extra_server_names), and Bulwark's JMAP URL points
// at it. Only the panel mail certificate covers that name — the switchover
// issues it for {custom, mail.<hostname>} — so the vhost must serve it even
// when the panel-primary domain has a certificate of its own. Serving the
// domain's certificate there fails TLS for every JMAP call.

// withPanelMailCertFiles points the panel mail cert seam at files in a temp
// dir; present=false leaves them absent.
func withPanelMailCertFiles(t *testing.T, present bool) (string, string) {
	t.Helper()
	dir := t.TempDir()
	crt, key := filepath.Join(dir, "panel-mail.crt"), filepath.Join(dir, "panel-mail.key")
	if present {
		require.NoError(t, os.WriteFile(crt, []byte("crt"), 0o600))
		require.NoError(t, os.WriteFile(key, []byte("key"), 0o600))
	}
	orig := panelMailCertPair
	panelMailCertPair = [2]string{crt, key}
	t.Cleanup(func() { panelMailCertPair = orig })
	return crt, key
}

func panelPrimaryVhostApply(t *testing.T, mailHostname *string, domainCert bool) map[string]any {
	t.Helper()
	ag := &paramsWebmailAgent{}
	dr := newFakeDomainRepo()
	dr.domains["p1"] = &models.Domain{ID: "p1", Name: "mx.jabali-panel.com", UserID: "u1", EmailEnabled: true, WebmailEnabled: true, IsPanelPrimary: true}
	ur := &fakeUserRepo{users: map[string]*models.User{"u1": {ID: "u1"}}}
	certs := newFakeSSLCertRepo()
	if domainCert {
		certs.byDomain["p1"] = &models.SSLCertificate{DomainID: "p1", Status: models.SSLStatusIssued,
			CertPath: wmPtr("/etc/letsencrypt/live/mx.jabali-panel.com/fullchain.pem"), KeyPath: wmPtr("/etc/letsencrypt/live/mx.jabali-panel.com/privkey.pem")}
	}
	r := New(dr, ur, ag, slog.Default(), Config{}).WithSSLCerts(certs).WithPackages(&webmailGatePkgRepo{})
	r.serverSettings = &fakeServerSettingsRepo{settings: &models.ServerSettings{
		WebmailEnabled: true, Hostname: "mx.jabali-panel.com", MailHostname: mailHostname,
	}}
	r.reconcileWebmailVhosts(context.Background())
	require.Len(t, ag.applies, 1, "one webmail.vhost_apply for the panel-primary domain")
	return ag.applies[0]
}

func TestPanelPrimaryWebmailVhost_CustomNameServesPanelMailCert(t *testing.T) {
	crt, key := withPanelMailCertFiles(t, true)
	got := panelPrimaryVhostApply(t, wmPtr("mailtest.jabali-panel.com"), true)
	assert.Equal(t, []string{"mailtest.jabali-panel.com"}, got["extra_server_names"])
	assert.Equal(t, crt, got["ssl_cert_path"], "the custom name is covered only by the panel mail certificate")
	assert.Equal(t, key, got["ssl_key_path"])
}

func TestPanelPrimaryWebmailVhost_DerivedNameKeepsDomainCert(t *testing.T) {
	withPanelMailCertFiles(t, true)
	got := panelPrimaryVhostApply(t, nil, true)
	assert.NotContains(t, got, "extra_server_names")
	assert.Equal(t, "/etc/letsencrypt/live/mx.jabali-panel.com/fullchain.pem", got["ssl_cert_path"],
		"with no custom name the domain's own certificate is unchanged")
}

func TestPanelPrimaryWebmailVhost_CustomNameWithoutPanelMailCertKeepsDomainCert(t *testing.T) {
	withPanelMailCertFiles(t, false)
	got := panelPrimaryVhostApply(t, wmPtr("mailtest.jabali-panel.com"), true)
	assert.Equal(t, "/etc/letsencrypt/live/mx.jabali-panel.com/fullchain.pem", got["ssl_cert_path"],
		"no panel mail certificate on disk: keep serving the domain certificate rather than none")
}

func TestPanelPrimaryWebmailVhost_NoDomainCertFallsBackToPanelMailCert(t *testing.T) {
	crt, _ := withPanelMailCertFiles(t, true)
	got := panelPrimaryVhostApply(t, nil, false)
	assert.Equal(t, crt, got["ssl_cert_path"], "the existing fallback for a panel-primary domain without a certificate row")
}
