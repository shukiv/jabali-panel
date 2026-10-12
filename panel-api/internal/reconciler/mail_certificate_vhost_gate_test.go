package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #2082: Let's Encrypt checks mail.<domain> over HTTP, and only the
// domain's mail vhost answers that check on :80. The mail certificate pass
// ran before the webmail pass, so on a new domain it ordered the certificate
// while mail.<domain> still landed on the server's default block, which
// serves only the panel's own challenges: a failed order and a 3h backoff
// for every new mail domain. It now waits until the webmail pass has the
// domain's mail vhost in place.

// gateMailCertRepo is the slice of the mail certificate repository the mail
// certificate pass drives, backed by a map.
type gateMailCertRepo struct {
	repository.MailCertificateRepository
	rows map[string]*models.MailCertificate // by domain ID
}

func (f *gateMailCertRepo) byID(id string) *models.MailCertificate {
	for _, row := range f.rows {
		if row.ID == id {
			return row
		}
	}
	return nil
}

func (f *gateMailCertRepo) GetByDomain(_ context.Context, domainID string) (*models.MailCertificate, error) {
	if row, ok := f.rows[domainID]; ok {
		return row, nil
	}
	return nil, repository.ErrNotFound
}

func (f *gateMailCertRepo) EnsureForDomain(_ context.Context, domainID string) (*models.MailCertificate, error) {
	if row, ok := f.rows[domainID]; ok {
		return row, nil
	}
	row := &models.MailCertificate{ID: "mc-" + domainID, DomainID: domainID, Status: models.MailCertStatusPending}
	f.rows[domainID] = row
	return row, nil
}

func (f *gateMailCertRepo) List(context.Context) ([]*models.MailCertificate, error) {
	out := make([]*models.MailCertificate, 0, len(f.rows))
	for _, row := range f.rows {
		out = append(out, row)
	}
	return out, nil
}

func (f *gateMailCertRepo) Delete(_ context.Context, id string) error {
	for k, row := range f.rows {
		if row.ID == id {
			delete(f.rows, k)
		}
	}
	return nil
}

func (f *gateMailCertRepo) UpdateStatus(_ context.Context, id, status string, lastError *string) error {
	if row := f.byID(id); row != nil {
		row.Status = status
		row.LastError = lastError
	}
	return nil
}

func (f *gateMailCertRepo) MarkIssued(_ context.Context, id, lineagePath string, issuedAt, expiresAt time.Time) error {
	if row := f.byID(id); row != nil {
		row.Status, row.LineagePath, row.IssuedAt, row.ExpiresAt, row.LastError =
			models.MailCertStatusIssued, lineagePath, &issuedAt, &expiresAt, nil
	}
	return nil
}

func (f *gateMailCertRepo) MarkFailed(_ context.Context, id, errMsg string, retryAfter time.Duration) error {
	if row := f.byID(id); row != nil {
		at := time.Now().Add(retryAfter)
		row.Status, row.LastError, row.NextRetryAt = models.MailCertStatusFailed, &errMsg, &at
	}
	return nil
}

func (f *gateMailCertRepo) MarkDNSMissing(_ context.Context, id, errMsg string) error {
	if row := f.byID(id); row != nil {
		at := time.Now().Add(3 * time.Minute)
		row.Status, row.LastError, row.NextRetryAt = models.MailCertStatusDNSMissing, &errMsg, &at
	}
	return nil
}

func (f *gateMailCertRepo) CountFirstIssueAttemptsLastWeek(context.Context) (int64, error) {
	return 0, nil
}

// mailCertGateAgent records every verb; ssl.mail.issue answers issued, and
// webmail.vhost_apply fails when failApply is set.
type mailCertGateAgent struct {
	cmds      []string
	failApply bool
}

func (a *mailCertGateAgent) Call(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
	a.cmds = append(a.cmds, cmd)
	switch cmd {
	case "ssl.mail.issue":
		now := time.Now().UTC()
		return json.Marshal(map[string]string{
			"outcome":      "issued",
			"lineage_path": "/etc/letsencrypt/live/mail.client.example",
			"issued_at":    now.Format(time.RFC3339),
			"expires_at":   now.Add(90 * 24 * time.Hour).Format(time.RFC3339),
		})
	case "webmail.vhost_apply":
		if a.failApply {
			return nil, errors.New("nginx -t failed")
		}
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func (a *mailCertGateAgent) count(cmd string) int {
	n := 0
	for _, c := range a.cmds {
		if c == cmd {
			n++
		}
	}
	return n
}

func (a *mailCertGateAgent) index(cmd string) int {
	for i, c := range a.cmds {
		if c == cmd {
			return i
		}
	}
	return -1
}

// newMailCertGateReconciler wires a mail-only domain (site elsewhere) with
// email and webmail on and its self-signed placeholder on file, so the
// webmail pass can render its mail vhost.
func newMailCertGateReconciler(t *testing.T, ag *mailCertGateAgent, repo repository.MailCertificateRepository) (*Reconciler, *fakeDomainRepo) {
	t.Helper()
	dr := newFakeDomainRepo()
	dr.domains["d1"] = &models.Domain{OwnershipState: verifiedOwnership, ID: "d1", Name: "client.example", UserID: "u1",
		EmailEnabled: true, WebmailEnabled: true, WebDisabled: true}
	ur := &fakeUserRepo{users: map[string]*models.User{"u1": {ID: "u1"}}}
	certs := newFakeSSLCertRepo()
	certs.byDomain["d1"] = &models.SSLCertificate{DomainID: "d1", Status: models.SSLStatusSelfSigned,
		CertPath: wmPtr("/etc/ssl/jabali/client.example/fullchain.pem"), KeyPath: wmPtr("/etc/ssl/jabali/client.example/privkey.pem")}
	r := New(dr, ur, ag, slog.Default(), Config{}).WithSSLCerts(certs).WithPackages(&webmailGatePkgRepo{}).
		WithMailCertificates(repo)
	r.serverSettings = &fakeServerSettingsRepo{settings: &models.ServerSettings{
		WebmailEnabled: true, Hostname: "mx.jabali-panel.com", AdminEmail: "admin@jabali-panel.com", PublicIPv4: "203.0.113.10"}}
	return r, dr
}

func TestMailCert_NewDomainWaitsForItsMailVhost(t *testing.T) {
	ag := &mailCertGateAgent{}
	repo := &gateMailCertRepo{rows: map[string]*models.MailCertificate{}}
	r, _ := newMailCertGateReconciler(t, ag, repo)
	ctx := context.Background()

	// Before the webmail pass, mail.client.example has no :80 block here.
	r.reconcileMailCertificates(ctx)
	assert.Zero(t, ag.count("ssl.mail.issue"), "no order while mail.<domain> lands on the default block")
	row := repo.rows["d1"]
	require.NotNil(t, row, "the backfill still creates the row")
	assert.Equal(t, models.MailCertStatusPending, row.Status, "not marked failed, so no 3h backoff")
	require.NotNil(t, row.LastError, "the wait is visible on the row")
	assert.Contains(t, *row.LastError, "mail.client.example")

	r.reconcileWebmailVhosts(ctx)
	r.reconcileMailCertificates(ctx)
	require.Equal(t, 1, ag.count("ssl.mail.issue"), "ordered once the mail vhost is in place")
	assert.Less(t, ag.index("webmail.vhost_apply"), ag.index("ssl.mail.issue"))
	assert.Equal(t, models.MailCertStatusIssued, row.Status)
	assert.Nil(t, row.LastError)
}

func TestMailCert_NoOrderWhileTheMailVhostCannotServe(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	issued := time.Now().Add(-80 * 24 * time.Hour)
	expires := time.Now().Add(10 * 24 * time.Hour)
	// Every row that would otherwise reach ssl.mail.issue: a first issue, a
	// retry whose backoff ran out, and a renewal inside the 30-day window.
	dueRows := map[string]func() *models.MailCertificate{
		"pending": func() *models.MailCertificate {
			return &models.MailCertificate{ID: "mc-d1", DomainID: "d1", Status: models.MailCertStatusPending}
		},
		"failed retry due": func() *models.MailCertificate {
			return &models.MailCertificate{ID: "mc-d1", DomainID: "d1", Status: models.MailCertStatusFailed, NextRetryAt: &past}
		},
		"dns_missing retry due": func() *models.MailCertificate {
			return &models.MailCertificate{ID: "mc-d1", DomainID: "d1", Status: models.MailCertStatusDNSMissing, NextRetryAt: &past}
		},
		"renewal due": func() *models.MailCertificate {
			return &models.MailCertificate{ID: "mc-d1", DomainID: "d1", Status: models.MailCertStatusIssued,
				LineagePath: "/etc/letsencrypt/live/mail.client.example", IssuedAt: &issued, ExpiresAt: &expires}
		},
	}
	scenarios := map[string]func(t *testing.T, r *Reconciler, dr *fakeDomainRepo, ag *mailCertGateAgent){
		"vhost apply failed": func(t *testing.T, r *Reconciler, _ *fakeDomainRepo, ag *mailCertGateAgent) {
			ag.failApply = true
			r.reconcileWebmailVhosts(context.Background())
			require.Equal(t, 1, ag.count("webmail.vhost_apply"))
		},
		"no certificate on file to render the vhost": func(t *testing.T, r *Reconciler, _ *fakeDomainRepo, ag *mailCertGateAgent) {
			r.reconcileWebmailVhosts(context.Background())
			require.Equal(t, 1, ag.count("webmail.vhost_apply"))
			delete(r.sslCerts.(*fakeSSLCertRepo).byDomain, "d1")
			r.reconcileWebmailVhosts(context.Background())
		},
	}
	for rowName, mkRow := range dueRows {
		for scName, scenario := range scenarios {
			if rowName == "renewal due" && scName == "no certificate on file to render the vhost" {
				// The issued mail certificate is itself on file, so the vhost
				// renders with it and the renewal goes ahead.
				continue
			}
			t.Run(rowName+"/"+scName, func(t *testing.T) {
				ag := &mailCertGateAgent{}
				row := mkRow()
				repo := &gateMailCertRepo{rows: map[string]*models.MailCertificate{"d1": row}}
				r, dr := newMailCertGateReconciler(t, ag, repo)
				scenario(t, r, dr, ag)
				status := row.Status

				r.reconcileMailCertificates(context.Background())
				assert.Zero(t, ag.count("ssl.mail.issue"))
				assert.Equal(t, status, row.Status, "status left as it was")
				require.NotNil(t, row.LastError)
				assert.Contains(t, *row.LastError, "mail.client.example")
			})
		}
	}
}

// An existing domain's row goes ahead once its mail vhost is in place: the
// reporter's domain sat in failed backoff behind the wrong vhost, and after
// the upgrade its retry must go out.
func TestMailCert_DueRowOrdersOnceTheVhostIsUp(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	issued := time.Now().Add(-80 * 24 * time.Hour)
	expires := time.Now().Add(10 * 24 * time.Hour)
	rows := map[string]*models.MailCertificate{
		"pending": {ID: "mc-d1", DomainID: "d1", Status: models.MailCertStatusPending},
		"failed retry due": {ID: "mc-d1", DomainID: "d1", Status: models.MailCertStatusFailed, NextRetryAt: &past,
			LastError: wmPtr("certbot issue failed (unknown): ... 404")},
		"dns_missing retry due": {ID: "mc-d1", DomainID: "d1", Status: models.MailCertStatusDNSMissing, NextRetryAt: &past},
		"renewal due": {ID: "mc-d1", DomainID: "d1", Status: models.MailCertStatusIssued,
			LineagePath: "/etc/letsencrypt/live/mail.client.example", IssuedAt: &issued, ExpiresAt: &expires},
	}
	for name, row := range rows {
		t.Run(name, func(t *testing.T) {
			ag := &mailCertGateAgent{}
			repo := &gateMailCertRepo{rows: map[string]*models.MailCertificate{"d1": row}}
			r, _ := newMailCertGateReconciler(t, ag, repo)

			r.reconcileWebmailVhosts(context.Background())
			r.reconcileMailCertificates(context.Background())
			assert.Equal(t, 1, ag.count("ssl.mail.issue"))
			assert.Equal(t, models.MailCertStatusIssued, row.Status)
		})
	}
}

// A domain deleted and re-created under the same name between two passes
// lost its mail vhost with the old row. The webmail pass must send it again
// rather than skip it as unchanged, and the new domain's order waits for it.
func TestMailCert_DomainRecreatedUnderSameName(t *testing.T) {
	ag := &mailCertGateAgent{}
	repo := &gateMailCertRepo{rows: map[string]*models.MailCertificate{}}
	r, dr := newMailCertGateReconciler(t, ag, repo)
	ctx := context.Background()

	r.reconcileWebmailVhosts(ctx)
	require.Equal(t, 1, ag.count("webmail.vhost_apply"))

	// Deleted (its vhost and mail certificate row go with it) and re-created.
	old := dr.domains["d1"]
	delete(dr.domains, "d1")
	delete(repo.rows, "d1")
	recreated := *old
	recreated.ID = "d2"
	dr.domains["d2"] = &recreated
	certs := r.sslCerts.(*fakeSSLCertRepo)
	placeholder := *certs.byDomain["d1"]
	placeholder.DomainID = "d2"
	certs.byDomain["d2"] = &placeholder

	r.reconcileWebmailVhosts(ctx)
	assert.Equal(t, 2, ag.count("webmail.vhost_apply"), "the re-created domain's vhost is sent again")
	r.reconcileMailCertificates(ctx)
	assert.Equal(t, 1, ag.count("ssl.mail.issue"))
	assert.Less(t, ag.index("webmail.vhost_apply"), ag.index("ssl.mail.issue"))
}

// A removed mail vhost no longer counts as in place.
func TestMailCert_RemovedVhostIsNotLive(t *testing.T) {
	ag := &mailCertGateAgent{}
	r, dr := newMailCertGateReconciler(t, ag, &gateMailCertRepo{rows: map[string]*models.MailCertificate{}})
	r.reconcileWebmailVhosts(context.Background())
	require.True(t, r.mailVhostIsLive("client.example"))

	dr.domains["d1"].EmailEnabled = false
	r.reconcileWebmailVhosts(context.Background())
	require.Equal(t, 1, ag.count("webmail.vhost_remove"))
	assert.False(t, r.mailVhostIsLive("client.example"))
}

// The wait message is written once, not on every tick.
func TestMailCert_WaitMessageWrittenOnce(t *testing.T) {
	ag := &mailCertGateAgent{}
	repo := &countingMailCertRepo{gateMailCertRepo: gateMailCertRepo{rows: map[string]*models.MailCertificate{}}}
	r, _ := newMailCertGateReconciler(t, ag, repo)

	r.reconcileMailCertificates(context.Background())
	r.reconcileMailCertificates(context.Background())
	assert.Zero(t, ag.count("ssl.mail.issue"))
	assert.Equal(t, 1, repo.updates)
}

type countingMailCertRepo struct {
	gateMailCertRepo
	updates int
}

func (c *countingMailCertRepo) UpdateStatus(ctx context.Context, id, status string, lastError *string) error {
	c.updates++
	return c.gateMailCertRepo.UpdateStatus(ctx, id, status, lastError)
}
