package reconciler

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #2082 follow-up: with webmail off for a domain (its toggle, its package,
// or server-wide) the webmail pass removed mail.<domain>'s vhost outright, so
// nothing answered Let's Encrypt's HTTP check there and the domain's mail
// certificate could never be issued. Mail clients on IMAP and SMTP kept a
// certificate that did not name mail.<domain>. The pass now keeps the :80
// block alone (challenge_only) for a domain that has mail, and the mail
// certificate goes ahead.

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

func TestMailChallengeVhost_WebmailOffKeepsTheChallenge(t *testing.T) {
	cases := map[string]func(d *models.Domain, u *models.User, r *Reconciler){
		"webmail off for the domain": func(d *models.Domain, _ *models.User, _ *Reconciler) { d.WebmailEnabled = false },
		"webmail off in the package": func(_ *models.Domain, u *models.User, _ *Reconciler) { u.PackageID = wmPtr("p-nowebmail") },
		"webmail off server-wide": func(_ *models.Domain, _ *models.User, r *Reconciler) {
			r.serverSettings.(*fakeServerSettingsRepo).settings.WebmailEnabled = false
		},
	}
	for name, turnOff := range cases {
		t.Run(name, func(t *testing.T) {
			ag := &challengeAgent{}
			d, u := mailDomain(), &models.User{ID: "u1"}
			r, repo := newChallengeReconciler(t, ag, d, []models.HostingPackage{{ID: "p-nowebmail", WebmailEnabled: false}}, u)
			turnOff(d, u, r)

			r.reconcileWebmailVhosts(context.Background())
			assert.Empty(t, ag.of("webmail.vhost_remove"), "mail.<domain>:80 stays up for the challenge")
			applies := ag.of("webmail.vhost_apply")
			require.Len(t, applies, 1)
			p := applies[0]
			assert.Equal(t, true, p["challenge_only"])
			assert.Equal(t, "client.example", p["domain_name"])
			assert.Equal(t, d.DocRoot, p["doc_root"], "the docroot fallback for the domain certificate's challenge")
			assert.NotContains(t, p, "ssl_cert_path", "no :443 server, so no certificate")
			assert.NotContains(t, p, "is_panel_primary")

			r.reconcileMailCertificates(context.Background())
			require.Len(t, ag.of("ssl.mail.issue"), 1, "the mail certificate goes ahead")
			assert.Equal(t, models.MailCertStatusIssued, repo.rows["d1"].Status)
		})
	}
}

// Webmail back on: the full vhost replaces the challenge-only block.
func TestMailChallengeVhost_WebmailBackOnRestoresTheVhost(t *testing.T) {
	ag := &challengeAgent{}
	d := mailDomain()
	d.WebmailEnabled = false
	r, _ := newChallengeReconciler(t, ag, d, nil, &models.User{ID: "u1"})

	r.reconcileWebmailVhosts(context.Background())
	d.WebmailEnabled = true
	r.reconcileWebmailVhosts(context.Background())
	applies := ag.of("webmail.vhost_apply")
	require.Len(t, applies, 2)
	assert.Equal(t, true, applies[0]["challenge_only"])
	assert.NotContains(t, applies[1], "challenge_only")
	assert.Contains(t, applies[1], "ssl_cert_path")
}

func TestMailChallengeVhost_NoChallengeWithoutMail(t *testing.T) {
	cases := map[string]func(d *models.Domain, r *Reconciler){
		"mail off": func(d *models.Domain, _ *Reconciler) { d.EmailEnabled = false; d.WebmailEnabled = false },
		"ownership not proven": func(d *models.Domain, _ *Reconciler) {
			d.WebmailEnabled = false
			d.OwnershipState = pendingOwnership()
		},
		// The panel's own domain: its mail certificate is the panel's, and the
		// server's default block already answers that challenge.
		"panel's own domain, webmail off server-wide": func(d *models.Domain, r *Reconciler) {
			d.IsPanelPrimary = true
			r.serverSettings.(*fakeServerSettingsRepo).settings.WebmailEnabled = false
		},
		"panel's own domain, webmail off for the domain": func(d *models.Domain, _ *Reconciler) {
			d.IsPanelPrimary = true
			d.WebmailEnabled = false
		},
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			ag := &challengeAgent{}
			d := mailDomain()
			r, _ := newChallengeReconciler(t, ag, d, nil, &models.User{ID: "u1"})
			set(d, r)

			r.reconcileWebmailVhosts(context.Background())
			assert.Empty(t, ag.of("webmail.vhost_apply"))
			assert.Len(t, ag.of("webmail.vhost_remove"), 1)
		})
	}
}

// The :80 block binds the domain's IP like the full vhost does (M24), or it
// lands in nginx's wildcard pool, which is never consulted for that IP.
func TestMailChallengeVhost_KeepsTheDomainsListenIP(t *testing.T) {
	ag := &challengeAgent{}
	d := mailDomain()
	d.WebmailEnabled = false
	ipID := uint64(7)
	d.ListenIPv4ID = &ipID
	r, _ := newChallengeReconciler(t, ag, d, nil, &models.User{ID: "u1"})
	r = r.WithManagedIPs(&fakeManagedIPs{rows: []models.ManagedIP{{ID: 7, Address: "198.51.100.7", Family: "ipv4"}}})

	r.reconcileWebmailVhosts(context.Background())
	applies := ag.of("webmail.vhost_apply")
	require.Len(t, applies, 1)
	assert.Equal(t, "198.51.100.7", applies[0]["listen_ipv4"])
}
