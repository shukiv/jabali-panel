package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #2380: AppSec skips the webmail hosts, because Bulwark sends a message's
// HTML in its JMAP request and CRS scores that as XSS. Two holes let it
// inspect webmail anyway, so an HTML message sent from it banned the sender's
// IP: the applied custom mail hostname (JAB-390) was never on the list, and a
// list that changed between updates never reached the AppSec config.

const appsecRefreshVerb = "security.crowdsec.appsec.refresh"

// refreshAgent records every verb and can fail the AppSec refresh.
type refreshAgent struct {
	challengeAgent
	failRefresh bool
}

func (a *refreshAgent) Call(ctx context.Context, cmd string, params any) (json.RawMessage, error) {
	raw, err := a.challengeAgent.Call(ctx, cmd, params)
	if cmd == appsecRefreshVerb && a.failRefresh {
		return nil, errors.New("agent busy")
	}
	return raw, err
}

func issuedMailCert(d *models.Domain) *models.SSLCertificate {
	return &models.SSLCertificate{DomainID: d.ID, Status: models.SSLStatusIssued,
		CertPath: wmPtr("/etc/letsencrypt/live/" + d.Name + "/fullchain.pem"),
		KeyPath:  wmPtr("/etc/letsencrypt/live/" + d.Name + "/privkey.pem")}
}

// newRefreshReconciler is a host with a panel-primary domain and one tenant
// domain, both with mail and an issued certificate.
func newRefreshReconciler(t *testing.T, ag *refreshAgent, mailHostname *string) (*Reconciler, *fakeDomainRepo, *fakeSSLCertRepo) {
	t.Helper()
	dr := newFakeDomainRepo()
	certs := newFakeSSLCertRepo()
	for _, d := range []*models.Domain{
		{OwnershipState: verifiedOwnership, ID: "p1", Name: "mx.jabali-panel.com", UserID: "u1", IsPanelPrimary: true, EmailEnabled: true, WebmailEnabled: true},
		{OwnershipState: verifiedOwnership, ID: "d1", Name: "tenant.com", UserID: "u1", EmailEnabled: true, WebmailEnabled: true},
	} {
		dr.domains[d.ID] = d
		certs.byDomain[d.ID] = issuedMailCert(d)
	}
	ur := &fakeUserRepo{users: map[string]*models.User{"u1": {ID: "u1"}}}
	r := New(dr, ur, ag, slog.Default(), Config{}).WithSSLCerts(certs).WithPackages(&webmailGatePkgRepo{})
	r.serverSettings = &fakeServerSettingsRepo{settings: &models.ServerSettings{
		WebmailEnabled: true, Hostname: "mx.jabali-panel.com", MailHostname: mailHostname,
	}}
	return r, dr, certs
}

func TestWebmailHosts_CustomMailHostnameIsListed(t *testing.T) {
	derived := []string{
		"autoconfig.mx.jabali-panel.com", "autoconfig.tenant.com",
		"autodiscover.mx.jabali-panel.com", "autodiscover.tenant.com",
		"mail.mx.jabali-panel.com", "mail.tenant.com",
	}
	cases := []struct {
		name         string
		mailHostname *string
		want         []string
	}{
		{name: "an applied custom name is listed", mailHostname: wmPtr("Webmail.Example.NET"),
			want: append(append([]string{}, derived...), "webmail.example.net")},
		{name: "no custom name adds nothing", mailHostname: nil, want: derived},
		{name: "a custom name equal to the derived one adds nothing", mailHostname: wmPtr("mail.mx.jabali-panel.com"), want: derived},
		{name: "an invalid stored value is not listed", mailHostname: wmPtr("https://evil.example/"), want: derived},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := withWebmailHostsFile(t)
			r, _, _ := newRefreshReconciler(t, &refreshAgent{}, tc.mailHostname)

			r.reconcileWebmailVhosts(context.Background())

			assert.ElementsMatch(t, tc.want, webmailHostsOnFile(t, path))
		})
	}
}

// The name goes with the panel-primary mail vhost: without mail on that
// domain no vhost answers it, and an entry would skip AppSec for whatever
// nginx serves that name instead.
func TestWebmailHosts_CustomMailHostnameLeavesWithThePanelPrimaryVhost(t *testing.T) {
	path := withWebmailHostsFile(t)
	r, dr, _ := newRefreshReconciler(t, &refreshAgent{}, wmPtr("webmail.example.net"))
	dr.domains["p1"].EmailEnabled = false

	r.reconcileWebmailVhosts(context.Background())

	hosts := webmailHostsOnFile(t, path)
	assert.NotContains(t, hosts, "webmail.example.net")
	assert.NotContains(t, hosts, "mail.mx.jabali-panel.com")
	assert.Contains(t, hosts, "mail.tenant.com")
}

// A list the pass writes reaches the AppSec config: the agent is asked to
// re-render it, once per list, and again when the list changes.
func TestWebmailHosts_ChangedListRefreshesTheAppSecConfig(t *testing.T) {
	withWebmailHostsFile(t)
	ag := &refreshAgent{}
	r, dr, certs := newRefreshReconciler(t, ag, nil)
	ctx := context.Background()

	r.reconcileWebmailVhosts(ctx)
	require.Len(t, ag.of(appsecRefreshVerb), 1, "the first pass renders the config with the list")

	r.reconcileWebmailVhosts(ctx)
	assert.Len(t, ag.of(appsecRefreshVerb), 1, "an unchanged list sends nothing")

	d := &models.Domain{OwnershipState: verifiedOwnership, ID: "d2", Name: "new.example", UserID: "u1", EmailEnabled: true, WebmailEnabled: true}
	dr.domains[d.ID] = d
	certs.byDomain[d.ID] = issuedMailCert(d)
	r.reconcileWebmailVhosts(ctx)
	assert.Len(t, ag.of(appsecRefreshVerb), 2, "a new mail domain re-renders the config")
}

// One failed call must not leave the config stale until the list changes
// again: the ledger does not stamp it, so the next pass retries.
func TestWebmailHosts_FailedRefreshIsRetried(t *testing.T) {
	withWebmailHostsFile(t)
	ag := &refreshAgent{failRefresh: true}
	r, _, _ := newRefreshReconciler(t, ag, nil)
	ctx := context.Background()

	r.reconcileWebmailVhosts(ctx)
	ag.failRefresh = false
	r.reconcileWebmailVhosts(ctx)
	r.reconcileWebmailVhosts(ctx)

	assert.Len(t, ag.of(appsecRefreshVerb), 2, "retried once after the failure, then current")
}

// The list could not be written: the config is not re-rendered from the old
// list, and nothing is stamped that would skip the refresh once a write works.
func TestWebmailHosts_UnwrittenListDoesNotRefresh(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	withWebmailHostsFile(t)
	webmailHostsPath = filepath.Join(blocker, "webmail-hosts.list")
	ag := &refreshAgent{}
	r, _, _ := newRefreshReconciler(t, ag, nil)

	r.reconcileWebmailVhosts(context.Background())

	assert.Empty(t, ag.of(appsecRefreshVerb))
}
