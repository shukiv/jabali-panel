package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/services"
)

// A new install turns the panel's Let's Encrypt certificate on by itself once
// public DNS shows the panel hostname at this server, and off again if that
// first issue fails (ADR-0066 amendment). Existing servers are never touched.

const leAutoIP = "203.0.113.10"

// leAutoRepo is fakePanelCertRepo with the issue outcomes applied, so a test
// can run the passes of a whole issue.
type leAutoRepo struct{ *fakePanelCertRepo }

func (f leAutoRepo) Upsert(_ context.Context, c *models.PanelCertificate) error {
	cp := *c
	f.rows[c.Kind] = &cp
	return nil
}
func (f leAutoRepo) MarkIssuedKind(_ context.Context, kind string, issuedAt, expiresAt time.Time) error {
	row := f.rows[kind]
	row.Status, row.IssuedAt, row.ExpiresAt, row.LastError, row.NextRetryAt = models.PanelCertStatusIssued, &issuedAt, &expiresAt, "", nil
	return nil
}
func (f leAutoRepo) MarkPendingRetryKind(_ context.Context, kind, errMsg string, retryAfter time.Duration) error {
	row := f.rows[kind]
	next := time.Now().Add(retryAfter)
	row.Status, row.LastError, row.NextRetryAt = models.PanelCertStatusPendingACMERetry, errMsg, &next
	return nil
}

type leAutoResolver struct{ addrs []string }

func (s leAutoResolver) LookupHost(context.Context, string) ([]string, error) { return s.addrs, nil }

type leAutoCase struct {
	host       string
	adminEmail string
	until      time.Time
	noWindow   bool
	public     []string // what public resolvers answer
	unreached  bool     // public DNS couldn't be asked
	issueErr   error
	local      []string // what this server's own resolver answers; default the server's IP
	mail       bool     // the panel hostname serves mail
	mailPublic []string // what public resolvers answer for mail.<host>
}

type leAutoRig struct {
	r       *Reconciler
	agent   *fakeAgent
	repo    leAutoRepo
	lookups *atomic.Int32
}

func newLEAutoRig(t *testing.T, tc leAutoCase) leAutoRig {
	t.Helper()
	if tc.host == "" {
		tc.host = "panel.example.com"
	}
	if tc.until.IsZero() {
		// Opened past the grace: the install is done.
		tc.until = time.Now().Add(models.PanelLEAutoWindow - panelLEAutoGrace - time.Minute)
	}
	if tc.local == nil {
		tc.local = []string{leAutoIP}
	}
	host := &models.PanelCertificate{Kind: models.PanelCertKindHostname, Hostname: tc.host,
		CertPEMPath: "/etc/jabali/tls/panel.crt", Status: models.PanelCertStatusSelfSigned}
	if !tc.noWindow {
		until := tc.until
		host.LEAutoUntil = &until
	}
	repo := leAutoRepo{&fakePanelCertRepo{rows: map[string]*models.PanelCertificate{
		models.PanelCertKindHostname: host,
		models.PanelCertKindMail: {Kind: models.PanelCertKindMail, Hostname: "mail." + tc.host,
			CertPEMPath: "/etc/jabali/tls/panel-mail.crt", Status: models.PanelCertStatusSelfSigned},
	}}}
	fa := &fakeAgent{}
	if tc.issueErr != nil {
		fa.errByMethod = map[string]error{"ssl.panel.issue": tc.issueErr}
	} else {
		raw, _ := json.Marshal(map[string]string{
			"issued_at":  time.Now().UTC().Format(time.RFC3339),
			"expires_at": time.Now().Add(90 * 24 * time.Hour).UTC().Format(time.RFC3339),
		})
		fa.resultByMethod = map[string]json.RawMessage{"ssl.panel.issue": raw}
	}
	lookups := &atomic.Int32{}
	pem := makeSelfSignPEM(t, panelSelfSignOrgMarker, tc.host, []string{tc.host, "mail." + tc.host}, time.Now().AddDate(5, 0, 0))
	r := &Reconciler{
		agent:      fa,
		log:        selfSignTestLogger(),
		panelCerts: repo,
		panelCertRoutability: &services.PanelCertRoutability{
			// The box's own resolver always says yes: it may answer from
			// this server's own zones, so it alone must never be enough.
			Resolver: leAutoResolver{addrs: tc.local},
			PublicLookup: func(_ context.Context, name string) ([]string, bool) {
				if name == "mail."+tc.host {
					return tc.mailPublic, true
				}
				lookups.Add(1)
				return tc.public, !tc.unreached
			},
		},
		serverSettings: &fakeSettingsRepo{srv: &models.ServerSettings{
			Hostname: tc.host, PublicIPv4: leAutoIP, AdminEmail: tc.adminEmail, MailEnabled: tc.mail,
		}},
		domains: &fakeDomainRepo{domains: map[string]*models.Domain{
			"d1": {ID: "d1", Name: tc.host, IsPanelPrimary: true, EmailEnabled: tc.mail},
		}},
		readCertFile: func(path string) ([]byte, error) {
			if path == "/etc/jabali/tls/panel.crt" {
				return pem, nil
			}
			return nil, os.ErrNotExist
		},
	}
	return leAutoRig{r: r, agent: fa, repo: repo, lookups: lookups}
}

func (g leAutoRig) pass() {
	g.r.reconcilePanelCertificate(context.Background())
}

func (g leAutoRig) host() *models.PanelCertificate {
	return g.repo.rows[models.PanelCertKindHostname]
}

func (g leAutoRig) issues() int {
	n, _ := countCalls(g.agent, "ssl.panel.issue")
	return n
}

// issuesOf counts the Let's Encrypt requests for one kind.
func (g leAutoRig) issuesOf(kind string) int {
	g.agent.mu.Lock()
	defer g.agent.mu.Unlock()
	n := 0
	for _, c := range g.agent.calls {
		if m, ok := c.params.(map[string]any); ok && c.method == "ssl.panel.issue" && m["kind"] == kind {
			n++
		}
	}
	return n
}

func TestPanelLEAuto_TurnsOnAndIssuesWhenPublicDNSShowsTheHost(t *testing.T) {
	g := newLEAutoRig(t, leAutoCase{adminEmail: "admin@example.com", public: []string{leAutoIP}})

	g.pass()
	assert.True(t, g.host().UseLE, "Let's Encrypt is turned on")
	require.NotNil(t, g.host().LEAutoUntil, "the window stays: it marks the automatic first issue")
	assert.Zero(t, g.issues(), "the pass that turned it on ends there")
	n, _ := countCalls(g.agent, "ssl.panel.selfsign")
	assert.Zero(t, n, "no self-signed regenerate on the way")

	g.pass()
	assert.Equal(t, 1, g.issues(), "the next pass issues")
	assert.Equal(t, models.PanelCertStatusIssued, g.host().Status)

	g.pass()
	assert.True(t, g.host().UseLE)
	assert.Nil(t, g.host().LEAutoUntil, "issued ends the window")
	assert.Equal(t, 1, g.issues())
}

func TestPanelLEAuto_FailedFirstIssueTurnsItBackOff(t *testing.T) {
	g := newLEAutoRig(t, leAutoCase{adminEmail: "admin@example.com", public: []string{leAutoIP},
		issueErr: errors.New("certbot: Timeout during connect (likely firewall problem)")})

	g.pass() // on
	g.pass() // issue fails
	require.Equal(t, 1, g.issues())
	require.Equal(t, models.PanelCertStatusPendingACMERetry, g.host().Status)

	g.pass() // back off
	h := g.host()
	assert.False(t, h.UseLE, "Let's Encrypt is off again")
	assert.Equal(t, models.PanelCertStatusSelfSigned, h.Status)
	assert.Nil(t, h.NextRetryAt, "no retry the admin didn't ask for")
	assert.Nil(t, h.LEAutoUntil, "one try: the window ends")

	g.pass()
	g.pass()
	assert.Equal(t, 1, g.issues(), "it doesn't come back")
	assert.False(t, g.host().UseLE)
}

func TestPanelLEAuto_StaysOffUntilPublicDNSAgrees(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    leAutoCase
	}{
		{"points elsewhere", leAutoCase{adminEmail: "admin@example.com", public: []string{"198.51.100.7"}}},
		{"no public record", leAutoCase{adminEmail: "admin@example.com"}},
		{"public DNS unreachable", leAutoCase{adminEmail: "admin@example.com", unreached: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newLEAutoRig(t, tc.c)
			g.pass()
			assert.False(t, g.host().UseLE)
			assert.NotNil(t, g.host().LEAutoUntil, "the window stays open")
			assert.Equal(t, int32(1), g.lookups.Load())
			assert.Zero(t, g.issues())
		})
	}
}

func TestPanelLEAuto_AsksPublicDNSAtMostEveryTenMinutes(t *testing.T) {
	g := newLEAutoRig(t, leAutoCase{adminEmail: "admin@example.com", public: []string{"198.51.100.7"}})
	g.pass()
	g.pass()
	assert.Equal(t, int32(1), g.lookups.Load(), "a second pass right away doesn't ask again")

	g.r.panelLEAutoLastCheck = time.Now().Add(-panelLEAutoCheckEvery - time.Second)
	g.pass()
	assert.Equal(t, int32(2), g.lookups.Load())
}

func TestPanelLEAuto_NeverTurnsOn(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    leAutoCase
	}{
		{"an existing server, no window", leAutoCase{adminEmail: "admin@example.com", public: []string{leAutoIP}, noWindow: true}},
		{"a free jabalihosted.com name", leAutoCase{host: "203-0-113-10.jabalihosted.com", adminEmail: "admin@example.com", public: []string{leAutoIP}}},
		{"no admin email", leAutoCase{public: []string{leAutoIP}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newLEAutoRig(t, tc.c)
			g.pass()
			g.pass()
			assert.False(t, g.host().UseLE)
			assert.Zero(t, g.lookups.Load(), "public DNS isn't even asked")
			assert.Zero(t, g.issues())
		})
	}
}

func TestPanelLEAuto_WindowPassedLeavesItToTheAdmin(t *testing.T) {
	g := newLEAutoRig(t, leAutoCase{adminEmail: "admin@example.com", public: []string{leAutoIP},
		until: time.Now().Add(-time.Minute)})
	g.pass()
	assert.False(t, g.host().UseLE)
	assert.Nil(t, g.host().LEAutoUntil, "the window is closed")
	assert.Zero(t, g.lookups.Load())

	g.pass()
	assert.False(t, g.host().UseLE)
}

func TestPanelLEAuto_WaitsForTheInstallToFinish(t *testing.T) {
	// The row was created on the panel's first pass, moments ago: install.sh
	// may still be setting up nginx, certbot and the agent.
	g := newLEAutoRig(t, leAutoCase{adminEmail: "admin@example.com", public: []string{leAutoIP},
		until: time.Now().Add(models.PanelLEAutoWindow)})
	g.pass()
	assert.False(t, g.host().UseLE)
	assert.Zero(t, g.lookups.Load(), "public DNS isn't asked yet")
	require.NotNil(t, g.host().LEAutoUntil)

	// Past the grace it goes ahead.
	opened := time.Now().Add(-panelLEAutoGrace - time.Second)
	until := opened.Add(models.PanelLEAutoWindow)
	g.host().LEAutoUntil = &until
	g.pass()
	assert.True(t, g.host().UseLE)
}

func TestPanelLEAuto_WaitsWhileThisServersResolverDisagrees(t *testing.T) {
	// Public DNS shows the server, but its own resolver answers elsewhere
	// (say, a stale record in a zone it hosts): the issue's routability
	// check would park it as not routable, and that would turn it back off.
	g := newLEAutoRig(t, leAutoCase{adminEmail: "admin@example.com", public: []string{leAutoIP},
		local: []string{"198.51.100.9"}})
	g.pass()
	g.pass()
	assert.False(t, g.host().UseLE)
	assert.NotNil(t, g.host().LEAutoUntil, "the window stays open")
	assert.Zero(t, g.issues())
}

// With the panel hostname serving mail, the mail certificate follows Let's
// Encrypt as it does when the admin turns it on: requested once mail.<host>
// resolves publicly, parked with the reason (no Let's Encrypt request) until
// then. It never decides the hostname's automatic attempt.
func TestPanelLEAuto_PanelHostnameServesMail(t *testing.T) {
	t.Run("mail name resolves too", func(t *testing.T) {
		g := newLEAutoRig(t, leAutoCase{adminEmail: "admin@example.com", public: []string{leAutoIP},
			mail: true, mailPublic: []string{leAutoIP}})
		g.pass()
		g.pass()
		g.pass()
		assert.True(t, g.host().UseLE)
		assert.Nil(t, g.host().LEAutoUntil)
		assert.Equal(t, 1, g.issuesOf(models.PanelCertKindHostname))
		assert.Equal(t, 1, g.issuesOf(models.PanelCertKindMail))
		assert.Equal(t, models.PanelCertStatusIssued, g.repo.rows[models.PanelCertKindMail].Status)
	})
	t.Run("mail name doesn't resolve", func(t *testing.T) {
		g := newLEAutoRig(t, leAutoCase{adminEmail: "admin@example.com", public: []string{leAutoIP}, mail: true})
		g.pass()
		g.pass()
		g.pass()
		assert.True(t, g.host().UseLE, "the hostname certificate keeps Let's Encrypt on")
		assert.Nil(t, g.host().LEAutoUntil)
		assert.Equal(t, models.PanelCertStatusIssued, g.host().Status)
		assert.Equal(t, 1, g.issuesOf(models.PanelCertKindHostname))
		assert.Zero(t, g.issuesOf(models.PanelCertKindMail), "no Let's Encrypt request for a name that doesn't resolve")
		mail := g.repo.rows[models.PanelCertKindMail]
		assert.Equal(t, models.PanelCertStatusPendingACMERetry, mail.Status)
		assert.Contains(t, mail.LastError, "mail DNS not pointed at this server")
	})
}
