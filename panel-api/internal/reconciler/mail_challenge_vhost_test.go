package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #2082 follow-ups: with webmail off for a domain (its toggle, its package,
// or server-wide) the webmail pass removed mail.<domain>'s vhost outright.
// Nothing answered Let's Encrypt's HTTP check there, so the domain's mail
// certificate could never be issued (fixed first with a challenge-only :80
// block), and mail apps lost what they use on mail.<domain>:
// autoconfig/autodiscover, CalDAV/CardDAV and JMAP. The pass now keeps the
// vhost without the webmail app (no_webmail), and falls back to the :80 block
// alone only when the domain definitively has no certificate to serve.

type challengeCall struct {
	cmd    string
	params map[string]any
}

// challengeAgent records each verb with its parameters; ssl.mail.issue
// answers issued.
type challengeAgent struct{ calls []challengeCall }

func (a *challengeAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	m, _ := params.(map[string]any)
	a.calls = append(a.calls, challengeCall{cmd: cmd, params: m})
	if cmd == "ssl.mail.issue" {
		now := time.Now().UTC()
		return json.Marshal(map[string]string{
			"outcome":      "issued",
			"lineage_path": "/etc/letsencrypt/live/mail.client.example",
			"issued_at":    now.Format(time.RFC3339),
			"expires_at":   now.Add(90 * 24 * time.Hour).Format(time.RFC3339),
		})
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func (a *challengeAgent) of(cmd string) []map[string]any {
	var out []map[string]any
	for _, c := range a.calls {
		if c.cmd == cmd {
			out = append(out, c.params)
		}
	}
	return out
}

// withWebmailHostsFile points the AppSec webmail-hosts state file at a temp
// file for the test and returns its path.
func withWebmailHostsFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "webmail-hosts.list")
	old := webmailHostsPath
	webmailHostsPath = p
	t.Cleanup(func() { webmailHostsPath = old })
	return p
}

func webmailHostsOnFile(t *testing.T, path string) []string {
	t.Helper()
	hosts, err := appseccfg.LoadWebmailHosts(path)
	require.NoError(t, err)
	return hosts
}

const placeholderCert = "/etc/ssl/jabali-selfsigned/client.example/fullchain.pem"

func newChallengeReconciler(t *testing.T, ag *challengeAgent, d *models.Domain, pkgs []models.HostingPackage, user *models.User) (*Reconciler, *gateMailCertRepo) {
	t.Helper()
	dr := newFakeDomainRepo()
	dr.domains[d.ID] = d
	ur := &fakeUserRepo{users: map[string]*models.User{user.ID: user}}
	certs := newFakeSSLCertRepo()
	certs.byDomain[d.ID] = &models.SSLCertificate{DomainID: d.ID, Status: models.SSLStatusSelfSigned,
		CertPath: wmPtr("/etc/ssl/jabali-selfsigned/" + d.Name + "/fullchain.pem"),
		KeyPath:  wmPtr("/etc/ssl/jabali-selfsigned/" + d.Name + "/privkey.pem")}
	repo := &gateMailCertRepo{rows: map[string]*models.MailCertificate{}}
	r := New(dr, ur, ag, slog.Default(), Config{}).WithSSLCerts(certs).
		WithPackages(&webmailGatePkgRepo{pkgs: pkgs}).WithMailCertificates(repo)
	r.serverSettings = &fakeServerSettingsRepo{settings: &models.ServerSettings{
		WebmailEnabled: true, Hostname: "mx.jabali-panel.com", AdminEmail: "admin@jabali-panel.com", PublicIPv4: "203.0.113.10"}}
	return r, repo
}

func mailDomain() *models.Domain {
	return &models.Domain{OwnershipState: verifiedOwnership, ID: "d1", Name: "client.example", UserID: "u1",
		EmailEnabled: true, WebmailEnabled: true, DocRoot: "/home/client/domains/client.example/public_html"}
}

// webmailOffCases turns webmail off each way it can be off.
var webmailOffCases = map[string]func(d *models.Domain, u *models.User, r *Reconciler){
	"webmail off for the domain": func(d *models.Domain, _ *models.User, _ *Reconciler) { d.WebmailEnabled = false },
	"webmail off in the package": func(_ *models.Domain, u *models.User, _ *Reconciler) { u.PackageID = wmPtr("p-nowebmail") },
	"webmail off server-wide": func(_ *models.Domain, _ *models.User, r *Reconciler) {
		r.serverSettings.(*fakeServerSettingsRepo).settings.WebmailEnabled = false
	},
}

var noWebmailPackage = []models.HostingPackage{{ID: "p-nowebmail", WebmailEnabled: false}}

func TestMailVhost_WebmailOffKeepsTheMailAppEndpoints(t *testing.T) {
	for name, turnOff := range webmailOffCases {
		t.Run(name, func(t *testing.T) {
			hostsFile := withWebmailHostsFile(t)
			ag := &challengeAgent{}
			d, u := mailDomain(), &models.User{ID: "u1"}
			r, repo := newChallengeReconciler(t, ag, d, noWebmailPackage, u)
			turnOff(d, u, r)

			r.reconcileWebmailVhosts(context.Background())
			assert.Empty(t, ag.of("webmail.vhost_remove"))
			assert.Empty(t, ag.of("service.start"), "no webmail domain: the webmail daemon stays down")
			applies := ag.of("webmail.vhost_apply")
			require.Len(t, applies, 1)
			p := applies[0]
			assert.Equal(t, true, p["no_webmail"])
			assert.NotContains(t, p, "challenge_only")
			assert.Equal(t, placeholderCert, p["ssl_cert_path"], "the :443 server keeps the domain's certificate")
			assert.Equal(t, d.DocRoot, p["doc_root"])
			// DAV methods (PROPFIND, REPORT) would otherwise trip CRS 911100.
			assert.ElementsMatch(t, []string{"mail.client.example", "autoconfig.client.example", "autodiscover.client.example"},
				webmailHostsOnFile(t, hostsFile), "the mail-app hosts stay exempt from AppSec like webmail's")

			r.reconcileMailCertificates(context.Background())
			require.Len(t, ag.of("ssl.mail.issue"), 1, "the mail certificate goes ahead")
			assert.Equal(t, models.MailCertStatusIssued, repo.rows["d1"].Status)
		})
	}
}

// Without a certificate to serve on :443, the :80 block alone still answers
// Let's Encrypt, so the mail certificate can be issued.
func TestMailVhost_WebmailOffWithoutACertificateKeepsTheChallenge(t *testing.T) {
	cases := map[string]func(certs *fakeSSLCertRepo){
		"no certificate row": func(certs *fakeSSLCertRepo) { delete(certs.byDomain, "d1") },
		"certificate revoked": func(certs *fakeSSLCertRepo) {
			certs.byDomain["d1"].Status = models.SSLStatusRevoked
		},
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			hostsFile := withWebmailHostsFile(t)
			ag := &challengeAgent{}
			d := mailDomain()
			d.WebmailEnabled = false
			r, repo := newChallengeReconciler(t, ag, d, nil, &models.User{ID: "u1"})
			set(r.sslCerts.(*fakeSSLCertRepo))

			r.reconcileWebmailVhosts(context.Background())
			applies := ag.of("webmail.vhost_apply")
			require.Len(t, applies, 1)
			assert.Equal(t, true, applies[0]["challenge_only"])
			assert.NotContains(t, applies[0], "ssl_cert_path")
			assert.Empty(t, webmailHostsOnFile(t, hostsFile), ":80 alone has nothing to exempt")

			r.reconcileMailCertificates(context.Background())
			require.Len(t, ag.of("ssl.mail.issue"), 1)
			assert.Equal(t, models.MailCertStatusIssued, repo.rows["d1"].Status)
		})
	}
}

// errSSLCertRepo fails every certificate lookup, like a database blip.
type errSSLCertRepo struct{ *fakeSSLCertRepo }

func (errSSLCertRepo) FindByDomainID(context.Context, string) (*models.SSLCertificate, error) {
	return nil, errors.New("db down")
}

// A failed certificate lookup is not "no certificate": the vhost the domain
// has stays as it is for this tick rather than dropping to the :80 block.
func TestMailVhost_CertificateLookupErrorLeavesTheVhost(t *testing.T) {
	for _, webmail := range []bool{false, true} {
		t.Run(map[bool]string{false: "webmail off", true: "webmail on"}[webmail], func(t *testing.T) {
			hostsFile := withWebmailHostsFile(t)
			ag := &challengeAgent{}
			d := mailDomain()
			d.WebmailEnabled = webmail
			r, _ := newChallengeReconciler(t, ag, d, nil, &models.User{ID: "u1"})
			r = r.WithSSLCerts(errSSLCertRepo{r.sslCerts.(*fakeSSLCertRepo)})

			r.reconcileWebmailVhosts(context.Background())
			assert.Empty(t, ag.of("webmail.vhost_apply"))
			assert.Empty(t, ag.of("webmail.vhost_remove"))
			assert.False(t, r.mailVhostIsLive("client.example"))
			assert.Contains(t, webmailHostsOnFile(t, hostsFile), "mail.client.example", "the exemption the vhost had stays")
		})
	}
}

// Webmail back on: the webmail app returns to mail.<domain>.
func TestMailVhost_WebmailBackOnRestoresWebmail(t *testing.T) {
	ag := &challengeAgent{}
	d := mailDomain()
	d.WebmailEnabled = false
	r, _ := newChallengeReconciler(t, ag, d, nil, &models.User{ID: "u1"})

	r.reconcileWebmailVhosts(context.Background())
	d.WebmailEnabled = true
	r.reconcileWebmailVhosts(context.Background())
	applies := ag.of("webmail.vhost_apply")
	require.Len(t, applies, 2)
	assert.Equal(t, true, applies[0]["no_webmail"])
	assert.NotContains(t, applies[1], "no_webmail")
	assert.Contains(t, applies[1], "ssl_cert_path")
	assert.Len(t, ag.of("service.start"), 1)
}

func TestMailVhost_RemovedWithoutMail(t *testing.T) {
	cases := map[string]func(d *models.Domain){
		"mail off":             func(d *models.Domain) { d.EmailEnabled = false; d.WebmailEnabled = false },
		"ownership not proven": func(d *models.Domain) { d.OwnershipState = pendingOwnership() },
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			ag := &challengeAgent{}
			d := mailDomain()
			r, _ := newChallengeReconciler(t, ag, d, nil, &models.User{ID: "u1"})
			set(d)

			r.reconcileWebmailVhosts(context.Background())
			assert.Empty(t, ag.of("webmail.vhost_apply"))
			assert.Len(t, ag.of("webmail.vhost_remove"), 1)
		})
	}
}

// The panel's own domain keeps its mail-app endpoints too; its vhost keeps
// the panel-primary parameters.
func TestMailVhost_PanelPrimaryWebmailOff(t *testing.T) {
	for name, turnOff := range webmailOffCases {
		t.Run(name, func(t *testing.T) {
			ag := &challengeAgent{}
			d, u := mailDomain(), &models.User{ID: "u1"}
			d.IsPanelPrimary = true
			r, _ := newChallengeReconciler(t, ag, d, noWebmailPackage, u)
			turnOff(d, u, r)

			r.reconcileWebmailVhosts(context.Background())
			assert.Empty(t, ag.of("webmail.vhost_remove"))
			applies := ag.of("webmail.vhost_apply")
			require.Len(t, applies, 1)
			assert.Equal(t, true, applies[0]["no_webmail"])
			assert.Equal(t, true, applies[0]["is_panel_primary"])
		})
	}
}

// Without SSL wired there is no certificate to serve on :443: webmail off
// server-wide still keeps the :80 block for a domain with mail, and stops
// the webmail daemon (#760).
func TestMailVhost_ServerWideOffWithoutSSL(t *testing.T) {
	ag := &challengeAgent{}
	dr := newFakeDomainRepo()
	tenant := mailDomain()
	primary := &models.Domain{OwnershipState: verifiedOwnership, ID: "d0", Name: "mx.jabali-panel.com", UserID: "u0",
		EmailEnabled: true, WebmailEnabled: true, IsPanelPrimary: true}
	dr.domains[tenant.ID], dr.domains[primary.ID] = tenant, primary
	r := New(dr, nil, ag, slog.Default(), Config{})
	r.serverSettings = &fakeServerSettingsRepo{settings: &models.ServerSettings{WebmailEnabled: false}}

	r.reconcileWebmailVhosts(context.Background())
	applies := ag.of("webmail.vhost_apply")
	require.Len(t, applies, 1)
	assert.Equal(t, "client.example", applies[0]["domain_name"])
	assert.Equal(t, true, applies[0]["challenge_only"])
	removes := ag.of("webmail.vhost_remove")
	require.Len(t, removes, 1)
	assert.Equal(t, "mx.jabali-panel.com", removes[0]["domain_name"])
	assert.Len(t, ag.of("service.stop"), 1)
}

// Every form binds the domain's IP (M24), or it lands in nginx's wildcard
// pool, which is never consulted for that IP.
func TestMailVhost_KeepsTheDomainsListenIP(t *testing.T) {
	cases := map[string]struct {
		webmail bool
		noCert  bool
		key     string
	}{
		"full vhost":      {webmail: true},
		"without webmail": {key: "no_webmail"},
		"challenge-only":  {noCert: true, key: "challenge_only"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ag := &challengeAgent{}
			d := mailDomain()
			d.WebmailEnabled = tc.webmail
			ipID := uint64(7)
			d.ListenIPv4ID = &ipID
			r, _ := newChallengeReconciler(t, ag, d, nil, &models.User{ID: "u1"})
			if tc.noCert {
				delete(r.sslCerts.(*fakeSSLCertRepo).byDomain, "d1")
			}
			r = r.WithManagedIPs(&fakeManagedIPs{rows: []models.ManagedIP{{ID: 7, Address: "198.51.100.7", Family: "ipv4"}}})

			r.reconcileWebmailVhosts(context.Background())
			applies := ag.of("webmail.vhost_apply")
			require.Len(t, applies, 1)
			assert.Equal(t, "198.51.100.7", applies[0]["listen_ipv4"])
			for _, k := range []string{"no_webmail", "challenge_only"} {
				assert.Equal(t, k == tc.key, applies[0][k] == true, k)
			}
		})
	}
}
