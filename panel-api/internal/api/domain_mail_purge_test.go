package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnscompile"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- partial mocks (embed the interface, override only what purge touches) ---

type mpMailboxRepo struct {
	repository.MailboxRepository
	byDomain map[string][]models.Mailbox
	deleted  []string
}

func (m *mpMailboxRepo) ListByDomainID(_ context.Context, domainID string, opts repository.ListOptions) ([]models.Mailbox, int64, error) {
	var out []models.Mailbox
	for _, mb := range m.byDomain[domainID] {
		if opts.ExcludeSystem && mb.System {
			continue
		}
		out = append(out, mb)
	}
	return out, int64(len(out)), nil
}
func (m *mpMailboxRepo) Delete(_ context.Context, id string) error {
	m.deleted = append(m.deleted, id)
	return nil
}

type mpMailCertRepo struct {
	repository.MailCertificateRepository
	row     *models.MailCertificate
	deleted []string
}

func (m *mpMailCertRepo) GetByDomain(_ context.Context, _ string) (*models.MailCertificate, error) {
	if m.row == nil {
		return nil, repository.ErrNotFound
	}
	return m.row, nil
}
func (m *mpMailCertRepo) Delete(_ context.Context, id string) error {
	m.deleted = append(m.deleted, id)
	return nil
}

// recording agent
type mpAgent struct {
	calls   []string
	failCmd string
}

func (a *mpAgent) Call(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
	a.calls = append(a.calls, cmd)
	if a.failCmd != "" && cmd == a.failCmd {
		return nil, errors.New("agent boom")
	}
	return json.RawMessage(`{"ok":true}`), nil
}
func (a *mpAgent) has(cmd string) bool {
	for _, c := range a.calls {
		if c == cmd {
			return true
		}
	}
	return false
}
func (a *mpAgent) indexOf(cmd string) int {
	for i, c := range a.calls {
		if c == cmd {
			return i
		}
	}
	return -1
}

func purgeRouter(t *testing.T, userID string, isAdmin bool, cfg DomainMailPurgeHandlerConfig) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: userID, IsAdmin: isAdmin})
		c.Next()
	})
	RegisterDomainMailPurgeRoutes(r.Group("/api/v1"), cfg)
	return r
}

func doPurge(r *gin.Engine, domainID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/domains/"+domainID+"/email/purge", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestMailPurge_ConfirmMismatchDoesNothing(t *testing.T) {
	dr := newMockDomainRepo()
	dr.Create(context.Background(), &models.Domain{ID: "d1", UserID: "u1", Name: "foo.test", EmailEnabled: true})
	ag := &mpAgent{}
	r := purgeRouter(t, "u1", false, DomainMailPurgeHandlerConfig{Domains: dr, Agent: ag})

	w := doPurge(r, "d1", `{"confirm_domain":"WRONG.test"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", w.Code, w.Body.String())
	}
	if len(ag.calls) != 0 {
		t.Fatalf("no agent calls expected on confirm mismatch, got %v", ag.calls)
	}
	if dr.domains["d1"].EmailEnabled != true {
		t.Fatalf("email must stay enabled on a rejected purge")
	}
}

func TestMailPurge_CrossTenantForbidden(t *testing.T) {
	dr := newMockDomainRepo()
	dr.Create(context.Background(), &models.Domain{ID: "d1", UserID: "owner", Name: "foo.test", EmailEnabled: true})
	ag := &mpAgent{}
	r := purgeRouter(t, "intruder", false, DomainMailPurgeHandlerConfig{Domains: dr, Agent: ag})

	w := doPurge(r, "d1", `{"confirm_domain":"foo.test"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", w.Code)
	}
	if len(ag.calls) != 0 {
		t.Fatalf("no agent calls expected for a foreign domain, got %v", ag.calls)
	}
}

func TestMailPurge_PurgeAccountsFailureAborts(t *testing.T) {
	dr := newMockDomainRepo()
	dr.Create(context.Background(), &models.Domain{ID: "d1", UserID: "u1", Name: "foo.test", EmailEnabled: true})
	mb := &mpMailboxRepo{byDomain: map[string][]models.Mailbox{"d1": {{ID: "m1", LocalPart: "a"}}}}
	ag := &mpAgent{failCmd: "mail.domain.purge_accounts"}
	r := purgeRouter(t, "u1", false, DomainMailPurgeHandlerConfig{Domains: dr, Mailboxes: mb, Agent: ag})

	w := doPurge(r, "d1", `{"confirm_domain":"foo.test"}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("want 502 when purge_accounts fails, got %d: %s", w.Code, w.Body.String())
	}
	// Hard gate: nothing DB-side touched, so the operator can retry.
	if len(mb.deleted) != 0 {
		t.Fatalf("no mailbox rows should be deleted when the gate fails, got %v", mb.deleted)
	}
	if dr.domains["d1"].EmailEnabled != true {
		t.Fatalf("email must stay enabled when the gate fails")
	}
	if ag.has("domain.email_disable") || ag.has("ssl.mail.delete") {
		t.Fatalf("no later teardown steps should run after the gate fails: %v", ag.calls)
	}
}

func TestMailPurge_FullTeardown(t *testing.T) {
	ctx := context.Background()
	dr := newMockDomainRepo()
	dr.Create(ctx, &models.Domain{ID: "d1", UserID: "u1", Name: "foo.test", EmailEnabled: true, MTASTSEnabled: true, MTASTSAppliedId: 3})
	mb := &mpMailboxRepo{byDomain: map[string][]models.Mailbox{"d1": {
		{ID: "m1", LocalPart: "a"}, {ID: "m2", LocalPart: "b"},
		{ID: "relay", LocalPart: "jabali-relay", System: true}, // infra — must survive
	}}}
	mc := &mpMailCertRepo{row: &models.MailCertificate{ID: "c1", LineagePath: "/etc/letsencrypt/live/mail.foo.test"}}

	zr := newMockDNSZoneRepo()
	zr.Create(ctx, &models.DNSZone{ID: "z1", DomainID: "d1", Name: "foo.test"})
	rr := newMockDNSRecordRepo()
	srv := &models.ServerSettings{PublicIPv4: "1.2.3.4"}
	m6 := dnscompile.EmailRecordsManagedBy
	// Mail-specific bootstrap rows (pristine) — must be removed.
	rr.Create(ctx, &models.DNSRecord{ID: "r-mailA", ZoneID: "z1", Name: "mail", Type: "A", Content: "1.2.3.4", Managed: true})
	rr.Create(ctx, &models.DNSRecord{ID: "r-mx", ZoneID: "z1", Name: "@", Type: "MX", Content: "mail.foo.test", Priority: 10, Managed: true})
	rr.Create(ctx, &models.DNSRecord{ID: "r-spf", ZoneID: "z1", Name: "@", Type: "TXT", Content: dnscompile.BuildSPFString(srv), Managed: true})
	rr.Create(ctx, &models.DNSRecord{ID: "r-dmarc", ZoneID: "z1", Name: "_dmarc", Type: "TXT", Content: `"v=DMARC1; p=quarantine; sp=quarantine; adkim=r; aspf=r"`, Managed: true})
	// M6-managed autoconfig — removed by marker.
	rr.Create(ctx, &models.DNSRecord{ID: "r-ac", ZoneID: "z1", Name: "autoconfig", Type: "CNAME", Content: "mail.foo.test", Managed: true, ManagedBy: &m6})
	// Survivors: apex A (website IP) + a user-edited TXT at apex.
	rr.Create(ctx, &models.DNSRecord{ID: "r-apexA", ZoneID: "z1", Name: "@", Type: "A", Content: "1.2.3.4", Managed: true})
	rr.Create(ctx, &models.DNSRecord{ID: "r-userTXT", ZoneID: "z1", Name: "@", Type: "TXT", Content: `"google-site-verification=abc"`, Managed: false})

	ag := &mpAgent{}
	r := purgeRouter(t, "u1", false, DomainMailPurgeHandlerConfig{
		Domains: dr, Mailboxes: mb, MailCerts: mc, Agent: ag,
		DNSZones: zr, DNSRecords: rr, ServerSettings: &mockServerSettingsRepo{getResult: srv},
	})

	w := doPurge(r, "d1", `{"confirm_domain":"foo.test"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp domainMailPurgeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.MailboxesDeleted != 2 {
		t.Errorf("mailboxes_deleted = %d, want 2", resp.MailboxesDeleted)
	}

	// Agent orchestration + ordering.
	for _, cmd := range []string{"mail.domain.purge_accounts", "domain.email_disable", "webmail.vhost_remove", "ssl.mail.delete"} {
		if !ag.has(cmd) {
			t.Errorf("expected agent call %s, got %v", cmd, ag.calls)
		}
	}
	if ag.indexOf("mail.domain.purge_accounts") >= ag.indexOf("domain.email_disable") {
		t.Errorf("purge_accounts must precede email_disable: %v", ag.calls)
	}
	if ag.indexOf("webmail.vhost_remove") >= ag.indexOf("ssl.mail.delete") {
		t.Errorf("vhost_remove must precede ssl.mail.delete (cert/vhost parity): %v", ag.calls)
	}

	// DB side: only the two USER mailboxes deleted; the system relay survives
	// (the sendmail-cred reconciler owns it).
	if len(mb.deleted) != 2 {
		t.Errorf("mailbox rows deleted = %v, want m1+m2 (relay excluded)", mb.deleted)
	}
	for _, id := range mb.deleted {
		if id == "relay" {
			t.Errorf("the system relay row must NOT be deleted by a mail purge")
		}
	}
	if dr.domains["d1"].EmailEnabled {
		t.Errorf("email_enabled must be cleared")
	}
	if dr.domains["d1"].MTASTSEnabled {
		t.Errorf("mta_sts_enabled must be cleared so the reconciler tears the policy down")
	}
	if len(mc.deleted) != 1 || mc.deleted[0] != "c1" {
		t.Errorf("mail cert row deleted = %v, want [c1]", mc.deleted)
	}

	// DNS: mail rows gone, web/user rows survive.
	remaining := map[string]bool{}
	recs, _ := rr.ListByZoneID(ctx, "z1")
	for _, rec := range recs {
		remaining[rec.ID] = true
	}
	for _, gone := range []string{"r-mailA", "r-mx", "r-spf", "r-dmarc", "r-ac"} {
		if remaining[gone] {
			t.Errorf("mail DNS record %s should have been removed", gone)
		}
	}
	for _, kept := range []string{"r-apexA", "r-userTXT"} {
		if !remaining[kept] {
			t.Errorf("record %s (web/user) must survive the mail purge", kept)
		}
	}
}

func TestIsPristineMailBootstrapRecord(t *testing.T) {
	srv := &models.ServerSettings{PublicIPv4: "1.2.3.4", PublicIPv6: "2001:db8::1"}
	m6 := dnscompile.EmailRecordsManagedBy
	mk := func(name, typ, content string, pri int, managed bool, mgr *string) *models.DNSRecord {
		return &models.DNSRecord{Name: name, Type: typ, Content: content, Priority: pri, Managed: managed, ManagedBy: mgr}
	}
	cases := []struct {
		name string
		rec  *models.DNSRecord
		want bool
	}{
		{"mail A pristine", mk("mail", "A", "1.2.3.4", 0, true, nil), true},
		{"mail A drifted IP", mk("mail", "A", "9.9.9.9", 0, true, nil), false},
		{"mail AAAA pristine", mk("mail", "AAAA", "2001:db8::1", 0, true, nil), true},
		{"apex MX pristine", mk("@", "MX", "mail.foo.test", 10, true, nil), true},
		{"apex MX wrong prio", mk("@", "MX", "mail.foo.test", 5, true, nil), false},
		{"apex SPF pristine", mk("@", "TXT", dnscompile.BuildSPFString(srv), 0, true, nil), true},
		{"apex user TXT", mk("@", "TXT", `"google-site-verification=x"`, 0, false, nil), false},
		{"dmarc canonical", mk("_dmarc", "TXT", `"v=DMARC1; p=quarantine; sp=quarantine; adkim=r; aspf=r"`, 0, true, nil), true},
		{"apex A (website) kept", mk("@", "A", "1.2.3.4", 0, true, nil), false},
		{"m6-managed skipped", mk("autoconfig", "CNAME", "mail.foo.test", 0, true, &m6), false},
		{"user-edited mail A kept", mk("mail", "A", "1.2.3.4", 0, false, nil), false},
	}
	for _, tc := range cases {
		if got := isPristineMailBootstrapRecord(tc.rec, "foo.test", srv); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
