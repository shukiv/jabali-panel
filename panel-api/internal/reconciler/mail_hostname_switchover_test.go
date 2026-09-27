package reconciler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailhostops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/services"
)

// JAB-390 switchover engine. A requested panel mail hostname is applied only
// after it (and mail.<hostname>, which stays served) points at this server
// and a certificate for both is issued and deployed. The engine then applies
// the name, moves the mail certificate row and marks the request done in one
// step, and points Bulwark's JMAP URL at the new name. Any failure leaves the
// applied name alone and records why.

type swFail struct {
	desired string
	msg     string
	retryIn time.Duration
}

type swComplete struct {
	desired string
	applied *string
}

type fakeSwitchoverRepo struct {
	row       *models.MailHostnameSwitchover
	claimOK   bool
	claims    []string
	fails     []swFail
	completes []swComplete
	pins      []string
	pinOK     bool
	pinErr    error
	onPin     func(string)
	onLost    func()
}

func (f *fakeSwitchoverRepo) Get(context.Context) (*models.MailHostnameSwitchover, error) {
	if f.row == nil {
		return nil, repository.ErrNotFound
	}
	cp := *f.row
	return &cp, nil
}
func (f *fakeSwitchoverRepo) Request(context.Context, string, string, time.Time) error { return nil }
func (f *fakeSwitchoverRepo) Cancel(context.Context, time.Time) error                  { return nil }
func (f *fakeSwitchoverRepo) Claim(_ context.Context, desired string, _, _ time.Time) (bool, error) {
	f.claims = append(f.claims, desired)
	return f.claimOK, nil
}
func (f *fakeSwitchoverRepo) Fail(_ context.Context, desired, msg string, retryAt, now time.Time) error {
	f.fails = append(f.fails, swFail{desired: desired, msg: msg, retryIn: retryAt.Sub(now)})
	return nil
}
func (f *fakeSwitchoverRepo) Complete(_ context.Context, desired string, applied *string, _, _, _ time.Time) error {
	f.completes = append(f.completes, swComplete{desired: desired, applied: applied})
	return nil
}
func (f *fakeSwitchoverRepo) PinApplied(_ context.Context, name string) (bool, error) {
	f.pins = append(f.pins, name)
	if f.pinOK && f.onPin != nil {
		f.onPin(name)
	}
	if !f.pinOK && f.pinErr == nil && f.onLost != nil {
		f.onLost()
	}
	return f.pinOK, f.pinErr
}

type fakePanelCertRepo struct {
	rows map[string]*models.PanelCertificate
}

func (f *fakePanelCertRepo) Get(ctx context.Context) (*models.PanelCertificate, error) {
	return f.GetByKind(ctx, models.PanelCertKindHostname)
}
func (f *fakePanelCertRepo) GetByKind(_ context.Context, kind string) (*models.PanelCertificate, error) {
	row, ok := f.rows[kind]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *row
	return &cp, nil
}
func (f *fakePanelCertRepo) ListAll(context.Context) ([]*models.PanelCertificate, error) {
	return nil, nil
}
func (f *fakePanelCertRepo) EnsureDefault(context.Context, string) (*models.PanelCertificate, error) {
	return nil, nil
}
func (f *fakePanelCertRepo) Upsert(context.Context, *models.PanelCertificate) error { return nil }
func (f *fakePanelCertRepo) MarkIssued(context.Context, time.Time, time.Time) error { return nil }
func (f *fakePanelCertRepo) MarkPendingRetry(context.Context, string, time.Duration) error {
	return nil
}
func (f *fakePanelCertRepo) MarkIssuedKind(context.Context, string, time.Time, time.Time) error {
	return nil
}
func (f *fakePanelCertRepo) MarkPendingRetryKind(context.Context, string, string, time.Duration) error {
	return nil
}

// swResolver answers A lookups from a map, both locally and publicly.
type swResolver map[string]string

func (m swResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	if ip, ok := m[host]; ok {
		return []string{ip}, nil
	}
	return nil, errors.New("no such host")
}

const swIP = "203.0.113.10"

type swFixture struct {
	r        *Reconciler
	domains  *fakeDomainRepo
	agent    *fakeAgent
	sw       *fakeSwitchoverRepo
	certs    *fakePanelCertRepo
	dns      swResolver
	settings *models.ServerSettings
	primary  *models.Domain
}

func newSwitchoverFixture(t *testing.T, desired string, status string) *swFixture {
	t.Helper()
	f := &swFixture{
		agent: &fakeAgent{resultByMethod: map[string]json.RawMessage{
			"ssl.panel.issue": json.RawMessage(`{"issued_at":"2026-09-27T10:00:00Z","expires_at":"2026-12-26T10:00:00Z"}`),
		}},
		sw: &fakeSwitchoverRepo{claimOK: true},
		certs: &fakePanelCertRepo{rows: map[string]*models.PanelCertificate{
			models.PanelCertKindHostname: {Kind: models.PanelCertKindHostname, Hostname: "mx.example.com", UseLE: true, Status: models.PanelCertStatusIssued},
			models.PanelCertKindMail: {Kind: models.PanelCertKindMail, Hostname: "mail.mx.example.com", Status: models.PanelCertStatusIssued,
				CertPEMPath: "/etc/jabali/tls/panel-mail.crt", UpdatedAt: time.Now().Add(-time.Hour)},
		}},
		dns: swResolver{"mail.mx.example.com": swIP, "mx.example.net": swIP},
		settings: &models.ServerSettings{Hostname: "mx.example.com", AdminEmail: "admin@example.com",
			PublicIPv4: swIP, WebmailEnabled: true},
		primary: &models.Domain{ID: "p1", Name: "mx.example.com", UserID: "u1", IsPanelPrimary: true, EmailEnabled: true, WebmailEnabled: true},
	}
	if desired != "" {
		f.sw.row = &models.MailHostnameSwitchover{ID: 1, Desired: &desired, Status: status, UpdatedAt: time.Now().Add(-time.Hour)}
	}
	dr := newFakeDomainRepo()
	dr.domains["p1"] = f.primary
	f.domains = dr
	ur := &fakeUserRepo{users: map[string]*models.User{"u1": {ID: "u1"}}}
	rout := &services.PanelCertRoutability{
		Resolver: f.dns,
		PublicLookup: func(_ context.Context, host string) ([]string, bool) {
			if ip, ok := f.dns[host]; ok {
				return []string{ip}, true
			}
			return nil, true
		},
	}
	f.r = New(dr, ur, f.agent, slog.Default(), Config{}).
		WithPanelCertificate(f.certs, rout).
		WithMailHostnameSwitchover(f.sw)
	f.r.serverSettings = &fakeServerSettingsRepo{settings: f.settings}
	return f
}

// callsTo returns the params of every agent call to method, in order.
func (f *swFixture) callsTo(method string) []map[string]any {
	var out []map[string]any
	for _, c := range f.agent.calls {
		if c.method == method {
			m, _ := c.params.(map[string]any)
			out = append(out, m)
		}
	}
	return out
}

func (f *swFixture) methods() []string {
	var out []string
	for _, c := range f.agent.calls {
		out = append(out, c.method)
	}
	return out
}

func TestMailHostnameSwitchover_IssuesBothNamesAndApplies(t *testing.T) {
	f := newSwitchoverFixture(t, "mx.example.net", models.MailHostnameSwitchoverPending)

	f.r.reconcileMailHostnameSwitchover(context.Background())

	issues := f.callsTo("ssl.panel.issue")
	require.Len(t, issues, 1)
	assert.Equal(t, "mx.example.net", issues[0]["hostname"])
	assert.Equal(t, []string{"mail.mx.example.com"}, issues[0]["extra_hostnames"], "the old derived name stays served")
	assert.Equal(t, "mail", issues[0]["kind"])
	assert.Equal(t, true, issues[0]["force_deploy"], "a kept lineage must still be deployed")
	assert.Equal(t, "admin@example.com", issues[0]["email"])
	assert.Equal(t, "/etc/jabali/tls/panel-mail.crt", issues[0]["cert_pem_path"])

	require.Len(t, f.sw.completes, 1)
	assert.Equal(t, "mx.example.net", f.sw.completes[0].desired)
	require.NotNil(t, f.sw.completes[0].applied)
	assert.Equal(t, "mx.example.net", *f.sw.completes[0].applied)
	assert.Empty(t, f.sw.fails)

	jmap := f.callsTo("webmail.jmap_url.apply")
	require.Len(t, jmap, 1)
	assert.Equal(t, "mx.example.net", jmap[0]["mail_hostname"], "Bulwark follows the applied name in the same tick")
	redirect := f.callsTo("nginx.webmail_redirect.apply")
	require.Len(t, redirect, 1)
	assert.Equal(t, "mx.example.net", redirect[0]["mail_hostname"], "the /webmail redirects follow the applied name in the same tick")
	assert.Equal(t, []string{"ssl.panel.issue", "webmail.jmap_url.apply", "nginx.webmail_redirect.apply"}, f.methods(),
		"webmail moves only after the certificate is deployed")
}

func TestMailHostnameSwitchover_ResetToDerivedClearsApplied(t *testing.T) {
	f := newSwitchoverFixture(t, "mail.mx.example.com", models.MailHostnameSwitchoverPending)
	f.settings.MailHostname = wmPtr("mx.example.net")

	f.r.reconcileMailHostnameSwitchover(context.Background())

	issues := f.callsTo("ssl.panel.issue")
	require.Len(t, issues, 1)
	assert.Equal(t, "mail.mx.example.com", issues[0]["hostname"])
	assert.Equal(t, []string{}, issues[0]["extra_hostnames"])
	assert.Equal(t, true, issues[0]["force_deploy"], "the derived lineage usually still holds a valid cert that must be redeployed")
	require.Len(t, f.sw.completes, 1)
	assert.Nil(t, f.sw.completes[0].applied, "switching back to the derived name clears the applied override")
	jmap := f.callsTo("webmail.jmap_url.apply")
	require.Len(t, jmap, 1)
	assert.Equal(t, "mail.mx.example.com", jmap[0]["mail_hostname"])
}

func TestMailHostnameSwitchover_UnclaimedSendsNothing(t *testing.T) {
	f := newSwitchoverFixture(t, "mx.example.net", models.MailHostnameSwitchoverPending)
	f.sw.claimOK = false

	f.r.reconcileMailHostnameSwitchover(context.Background())

	assert.Equal(t, []string{"mx.example.net"}, f.sw.claims)
	assert.Empty(t, f.callsTo("ssl.panel.issue"))
	assert.Empty(t, f.sw.completes)
	jmap := f.callsTo("webmail.jmap_url.apply")
	require.Len(t, jmap, 1)
	assert.Equal(t, "mail.mx.example.com", jmap[0]["mail_hostname"], "the steady assert keeps the applied name")
}

func TestMailHostnameSwitchover_UnroutableNameFailsBeforeClaim(t *testing.T) {
	for _, name := range []string{"mx.example.net", "mail.mx.example.com"} {
		t.Run(name, func(t *testing.T) {
			f := newSwitchoverFixture(t, "mx.example.net", models.MailHostnameSwitchoverPending)
			delete(f.dns, name)

			f.r.reconcileMailHostnameSwitchover(context.Background())

			require.Len(t, f.sw.fails, 1)
			assert.Equal(t, "mx.example.net", f.sw.fails[0].desired)
			assert.Contains(t, f.sw.fails[0].msg, name, "the reason names the name that does not point here")
			assert.LessOrEqual(t, f.sw.fails[0].retryIn, 15*time.Minute, "a DNS fix is picked up soon")
			assert.Empty(t, f.sw.claims)
			assert.Empty(t, f.callsTo("ssl.panel.issue"))
		})
	}
}

func TestMailHostnameSwitchover_NotReadyFailsWithSharedReason(t *testing.T) {
	cases := map[string]struct {
		mutate func(*swFixture)
		want   error
	}{
		"self-signed panel":       {func(f *swFixture) { f.certs.rows[models.PanelCertKindHostname].UseLE = false }, mailhostops.ErrLetsEncryptOff},
		"webmail off on primary":  {func(f *swFixture) { f.primary.WebmailEnabled = false }, mailhostops.ErrPanelMailOff},
		"no panel-primary domain": {func(f *swFixture) { f.primary.IsPanelPrimary = false }, mailhostops.ErrPanelMailOff},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newSwitchoverFixture(t, "mx.example.net", models.MailHostnameSwitchoverPending)
			tc.mutate(f)

			f.r.reconcileMailHostnameSwitchover(context.Background())

			require.Len(t, f.sw.fails, 1)
			assert.Equal(t, tc.want.Error(), f.sw.fails[0].msg)
			assert.Empty(t, f.sw.claims)
			assert.Empty(t, f.callsTo("ssl.panel.issue"))
		})
	}
}

func TestMailHostnameSwitchover_IssueErrorFailsAndKeepsAppliedName(t *testing.T) {
	f := newSwitchoverFixture(t, "mx.example.net", models.MailHostnameSwitchoverPending)
	f.agent.errByMethod = map[string]error{"ssl.panel.issue": errors.New("certbot: too many certificates already issued")}

	f.r.reconcileMailHostnameSwitchover(context.Background())

	require.Len(t, f.sw.fails, 1)
	assert.GreaterOrEqual(t, f.sw.fails[0].retryIn, time.Hour, "an ACME failure is not retried every tick")
	assert.True(t, strings.Contains(f.sw.fails[0].msg, "too many certificates"), "got %q", f.sw.fails[0].msg)
	assert.Empty(t, f.sw.completes)
	jmap := f.callsTo("webmail.jmap_url.apply")
	require.Len(t, jmap, 1)
	assert.Equal(t, "mail.mx.example.com", jmap[0]["mail_hostname"])
}

func TestMailHostnameSwitchover_NothingDueDoesNothing(t *testing.T) {
	future := time.Now().Add(time.Hour)
	cases := map[string]func(*swFixture){
		"no request": func(f *swFixture) { f.sw.row = nil },
		"cancelled":  func(f *swFixture) { f.sw.row.Desired = nil; f.sw.row.Status = models.MailHostnameSwitchoverIdle },
		"done":       func(f *swFixture) { f.sw.row.Status = models.MailHostnameSwitchoverDone },
		"failed, not due": func(f *swFixture) {
			f.sw.row.Status = models.MailHostnameSwitchoverFailed
			f.sw.row.NextRetryAt = &future
		},
		"issuing, fresh": func(f *swFixture) {
			f.sw.row.Status = models.MailHostnameSwitchoverIssuing
			f.sw.row.UpdatedAt = time.Now()
		},
		"mail cert in flight": func(f *swFixture) {
			f.certs.rows[models.PanelCertKindMail].Status = models.PanelCertStatusPendingACME
			f.certs.rows[models.PanelCertKindMail].UpdatedAt = time.Now()
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newSwitchoverFixture(t, "mx.example.net", models.MailHostnameSwitchoverPending)
			mutate(f)

			f.r.reconcileMailHostnameSwitchover(context.Background())

			assert.Empty(t, f.sw.claims)
			assert.Empty(t, f.sw.fails)
			assert.Empty(t, f.callsTo("ssl.panel.issue"))
		})
	}
}

func TestMailHostnameSwitchover_DueRetryAndStaleAttemptRun(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	cases := map[string]func(*swFixture){
		"failed, due": func(f *swFixture) {
			f.sw.row.Status = models.MailHostnameSwitchoverFailed
			f.sw.row.NextRetryAt = &past
		},
		"issuing, stale": func(f *swFixture) { f.sw.row.Status = models.MailHostnameSwitchoverIssuing },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newSwitchoverFixture(t, "mx.example.net", models.MailHostnameSwitchoverPending)
			mutate(f)

			f.r.reconcileMailHostnameSwitchover(context.Background())

			assert.Len(t, f.callsTo("ssl.panel.issue"), 1)
			assert.Len(t, f.sw.completes, 1)
		})
	}
}

// A tenant can create a domain after the request is made. The engine never
// issues for, or applies, a name a tenant answers or whose zone it controls.
func TestMailHostnameSwitchover_TenantDomainFailsBeforeIssue(t *testing.T) {
	for _, tenant := range []string{"mx.example.net", "example.net"} {
		t.Run(tenant, func(t *testing.T) {
			f := newSwitchoverFixture(t, "mx.example.net", models.MailHostnameSwitchoverPending)
			f.domains.domains["t1"] = &models.Domain{ID: "t1", Name: tenant, UserID: "u2"}

			f.r.reconcileMailHostnameSwitchover(context.Background())

			require.Len(t, f.sw.fails, 1)
			assert.Contains(t, f.sw.fails[0].msg, mailhostops.ErrNameClaimedByDomain.Error())
			assert.Empty(t, f.sw.claims)
			assert.Empty(t, f.callsTo("ssl.panel.issue"))
		})
	}
}

// issueHookAgent runs onIssue when ssl.panel.issue is sent, then answers as
// the wrapped fake does.
type issueHookAgent struct {
	*fakeAgent
	onIssue func()
}

func (a *issueHookAgent) Call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	if method == "ssl.panel.issue" && a.onIssue != nil {
		a.onIssue()
	}
	return a.fakeAgent.Call(ctx, method, params)
}

func TestMailHostnameSwitchover_TenantDomainDuringIssueIsNotApplied(t *testing.T) {
	f := newSwitchoverFixture(t, "mx.example.net", models.MailHostnameSwitchoverPending)
	f.r.agent = &issueHookAgent{fakeAgent: f.agent, onIssue: func() {
		f.domains.domains["t1"] = &models.Domain{ID: "t1", Name: "example.net", UserID: "u2"}
	}}

	f.r.reconcileMailHostnameSwitchover(context.Background())

	assert.Len(t, f.callsTo("ssl.panel.issue"), 1)
	assert.Empty(t, f.sw.completes, "a name a tenant claimed while the certificate was issued is not applied")
	require.Len(t, f.sw.fails, 1)
	assert.Contains(t, f.sw.fails[0].msg, mailhostops.ErrNameClaimedByDomain.Error())
	jmap := f.callsTo("webmail.jmap_url.apply")
	require.Len(t, jmap, 1)
	assert.Equal(t, "mail.mx.example.com", jmap[0]["mail_hostname"])
}

// The steady assert keeps Bulwark's JMAP URL on the effective name, once
// per change — not on every tick.
func TestMailHostnameJMAPAssert(t *testing.T) {
	f := newSwitchoverFixture(t, "", "")
	f.settings.MailHostname = wmPtr("MX.Example.NET")

	f.r.reconcileMailHostnameSwitchover(context.Background())
	f.r.reconcileMailHostnameSwitchover(context.Background())

	jmap := f.callsTo("webmail.jmap_url.apply")
	require.Len(t, jmap, 1, "an unchanged name is not re-sent within the audit interval")
	assert.Equal(t, "mx.example.net", jmap[0]["mail_hostname"])
}

// The /webmail redirects in the default vhost follow the effective name too,
// once per change (JAB-390). Before, they moved only when `jabali update`
// re-rendered the file.
func TestMailHostnameWebmailRedirectAssert(t *testing.T) {
	f := newSwitchoverFixture(t, "", "")
	f.settings.MailHostname = wmPtr("MX.Example.NET")

	f.r.reconcileMailHostnameSwitchover(context.Background())
	f.r.reconcileMailHostnameSwitchover(context.Background())

	redirect := f.callsTo("nginx.webmail_redirect.apply")
	require.Len(t, redirect, 1, "an unchanged name is not re-sent within the audit interval")
	assert.Equal(t, "mx.example.net", redirect[0]["mail_hostname"])
}

// A failed apply is retried every tick and warned about once per distinct
// error, not every tick.
func TestMailHostnameWebmailRedirectAssert_FailureIsRetried(t *testing.T) {
	f := newSwitchoverFixture(t, "", "")
	var logs bytes.Buffer
	f.r.log = slog.New(slog.NewTextHandler(&logs, nil))
	f.agent.errByMethod = map[string]error{"nginx.webmail_redirect.apply": errors.New("unknown method")}

	f.r.reconcileMailHostnameSwitchover(context.Background())
	f.r.reconcileMailHostnameSwitchover(context.Background())
	f.agent.errByMethod = nil
	f.r.reconcileMailHostnameSwitchover(context.Background())

	assert.Len(t, f.callsTo("nginx.webmail_redirect.apply"), 3, "a failed apply is not stamped")
	assert.Len(t, f.callsTo("webmail.jmap_url.apply"), 1, "a failed redirect apply does not hold back the JMAP URL")
	assert.Equal(t, 1, strings.Count(logs.String(), "webmail redirect apply failed"), "one warning per distinct error")
}

func TestMailHostnameJMAPAssert_FailureIsRetried(t *testing.T) {
	f := newSwitchoverFixture(t, "", "")
	f.agent.errByMethod = map[string]error{"webmail.jmap_url.apply": errors.New("unknown method")}

	f.r.reconcileMailHostnameSwitchover(context.Background())
	f.agent.errByMethod = nil
	f.r.reconcileMailHostnameSwitchover(context.Background())

	assert.Len(t, f.callsTo("webmail.jmap_url.apply"), 2, "a failed apply is not stamped")
}

// JAB-389 keeps the panel mail certificate on the name it was issued for when
// the panel is renamed. With no applied mail hostname, the effective name
// would follow the new panel hostname, so Bulwark and the webmail vhosts would
// move to mail.<new-hostname>, a name no certificate covers. The pass records
// the name the issued Let's Encrypt certificate serves as the applied one, so
// nothing moves until an admin requests a switchover.
func renamedPanelFixture(t *testing.T) *swFixture {
	t.Helper()
	f := newSwitchoverFixture(t, "", "")
	row := f.certs.rows[models.PanelCertKindMail]
	row.Hostname = "mail.old.example.com"
	row.UseLE = true
	f.sw.pinOK = true
	f.sw.onPin = func(name string) { f.settings.MailHostname = &name }
	// Each read is a copy, as from the database, so the pass must carry the
	// pinned name itself rather than see it through a shared pointer.
	f.r.serverSettings = copyingSettingsRepo{&fakeServerSettingsRepo{settings: f.settings}}
	return f
}

func TestMailHostnamePin_RenamedPanelKeepsIssuedMailName(t *testing.T) {
	f := renamedPanelFixture(t)

	f.r.reconcileMailHostnameSwitchover(context.Background())

	assert.Equal(t, []string{"mail.old.example.com"}, f.sw.pins)
	jmap := f.callsTo("webmail.jmap_url.apply")
	require.Len(t, jmap, 1)
	assert.Equal(t, "mail.old.example.com", jmap[0]["mail_hostname"], "Bulwark stays on the name the certificate serves")
	assert.Empty(t, f.callsTo("ssl.panel.issue"), "pinning never issues a certificate")

	f.r.reconcileMailHostnameSwitchover(context.Background())
	assert.Len(t, f.sw.pins, 1, "once applied, the pin is not written again")
}

func TestMailHostnamePin_NotWritten(t *testing.T) {
	applied := "mx.example.net"
	cases := []struct {
		name   string
		mutate func(*swFixture)
		jmap   string
	}{
		{"row is the derived name", func(f *swFixture) {
			f.certs.rows[models.PanelCertKindMail].Hostname = "mail.mx.example.com"
		}, "mail.mx.example.com"},
		{"self-signed row follows the hostname", func(f *swFixture) {
			f.certs.rows[models.PanelCertKindMail].UseLE = false
		}, "mail.mx.example.com"},
		{"Let's Encrypt row not issued yet", func(f *swFixture) {
			f.certs.rows[models.PanelCertKindMail].Status = models.PanelCertStatusPendingACMERetry
		}, "mail.mx.example.com"},
		{"row hostname is not a valid name", func(f *swFixture) {
			f.certs.rows[models.PanelCertKindMail].Hostname = "https://mail.old.example.com/"
		}, "mail.mx.example.com"},
		{"row hostname is the panel hostname", func(f *swFixture) {
			f.certs.rows[models.PanelCertKindMail].Hostname = "mx.example.com"
		}, "mail.mx.example.com"},
		{"a name is already applied", func(f *swFixture) {
			f.settings.MailHostname = &applied
		}, "mx.example.net"},
		{"no mail certificate row", func(f *swFixture) {
			delete(f.certs.rows, models.PanelCertKindMail)
		}, "mail.mx.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := renamedPanelFixture(t)
			tc.mutate(f)

			f.r.reconcileMailHostnameSwitchover(context.Background())

			assert.Empty(t, f.sw.pins)
			jmap := f.callsTo("webmail.jmap_url.apply")
			require.Len(t, jmap, 1)
			assert.Equal(t, tc.jmap, jmap[0]["mail_hostname"])
		})
	}
}

// A pin that did not write must not point Bulwark at the name it tried to
// pin: after a lost race it uses the name the other writer stored, and after
// a failed write the name it already had.
func TestMailHostnamePin_LostOrFailedWrite(t *testing.T) {
	t.Run("lost the race", func(t *testing.T) {
		f := renamedPanelFixture(t)
		f.sw.pinOK = false
		other := "mx.example.net"
		f.sw.onLost = func() { f.settings.MailHostname = &other }

		f.r.reconcileMailHostnameSwitchover(context.Background())

		assert.Len(t, f.sw.pins, 1)
		jmap := f.callsTo("webmail.jmap_url.apply")
		require.Len(t, jmap, 1)
		assert.Equal(t, "mx.example.net", jmap[0]["mail_hostname"], "the stored name wins")
	})
	t.Run("write failed", func(t *testing.T) {
		f := renamedPanelFixture(t)
		f.sw.pinOK, f.sw.pinErr = false, errors.New("db down")

		f.r.reconcileMailHostnameSwitchover(context.Background())

		assert.Len(t, f.sw.pins, 1)
		jmap := f.callsTo("webmail.jmap_url.apply")
		require.Len(t, jmap, 1)
		assert.Equal(t, "mail.mx.example.com", jmap[0]["mail_hostname"])
	})
}

// copyingSettingsRepo returns a fresh copy on every Get, as the database
// does, so a memoized snapshot cannot see a later write through a shared
// pointer.
type copyingSettingsRepo struct {
	*fakeServerSettingsRepo
}

func (c copyingSettingsRepo) Get(ctx context.Context) (*models.ServerSettings, error) {
	s, err := c.fakeServerSettingsRepo.Get(ctx)
	if err != nil || s == nil {
		return s, err
	}
	cp := *s
	return &cp, nil
}

// Later passes in the same tick (webmail vhosts, sendmail credentials) read
// the pinned name, not the snapshot taken before the pin.
func TestMailHostnamePin_LaterPassesInTheTickSeeIt(t *testing.T) {
	f := renamedPanelFixture(t)
	ctx, rr := withRun(context.Background(), RunNormal)
	defer rr.finish()
	if s, _ := f.r.settingsGet(ctx); s.MailHostname != nil {
		t.Fatal("precondition: nothing applied before the pass")
	}

	f.r.reconcileMailHostnameSwitchover(ctx)

	s, err := f.r.settingsGet(ctx)
	require.NoError(t, err)
	require.NotNil(t, s.MailHostname)
	assert.Equal(t, "mail.old.example.com", *s.MailHostname)
}
