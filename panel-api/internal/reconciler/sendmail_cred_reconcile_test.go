package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

type sendmailAgentCall struct {
	method string
	params map[string]any
}

type fakeSendmailAgent struct {
	calls      []sendmailAgentCall
	failEnsure bool
}

func (f *fakeSendmailAgent) Call(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
	m, _ := params.(map[string]any)
	f.calls = append(f.calls, sendmailAgentCall{method: method, params: m})
	if method == "sendmail.cred.ensure" && f.failEnsure {
		return nil, fmt.Errorf("agent down")
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func (f *fakeSendmailAgent) byMethod(method string) []sendmailAgentCall {
	var out []sendmailAgentCall
	for _, c := range f.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

type fakeSendmailDomainRepo struct {
	repository.DomainRepository
	rows []models.Domain
}

func (f *fakeSendmailDomainRepo) List(context.Context, repository.ListOptions) ([]models.Domain, int64, error) {
	return f.rows, int64(len(f.rows)), nil
}

type fakeSendmailUserRepo struct {
	repository.UserRepository
	users map[string]*models.User
}

func (f *fakeSendmailUserRepo) FindByID(_ context.Context, id string) (*models.User, error) {
	u, ok := f.users[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return u, nil
}

type fakeSendmailMailboxRepo struct {
	repository.MailboxRepository
	byEmail map[string]*models.Mailbox
	// domainNames lets Create key byEmail like the real DB trigger would
	// (local_part@domains.name), so a second tick FINDS the created row.
	domainNames map[string]string
	created     []*models.Mailbox
	rotated     []string
}

func (f *fakeSendmailMailboxRepo) FindByEmail(_ context.Context, email string) (*models.Mailbox, error) {
	mb, ok := f.byEmail[email]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return mb, nil
}

func (f *fakeSendmailMailboxRepo) Create(_ context.Context, mb *models.Mailbox) error {
	f.created = append(f.created, mb)
	f.byEmail[mb.LocalPart+"@"+f.domainNames[mb.DomainID]] = mb
	return nil
}

func (f *fakeSendmailMailboxRepo) UpdatePasswordHashAndEnc(_ context.Context, id, hash string, enc []byte) error {
	f.rotated = append(f.rotated, id)
	for _, mb := range f.byEmail {
		if mb.ID == id {
			mb.PasswordHash = hash
			mb.PasswordEnc = enc
		}
	}
	return nil
}

func sendmailTestReconciler(agent *fakeSendmailAgent, mailboxes *fakeSendmailMailboxRepo, domains []models.Domain) *Reconciler {
	key := ssokey.Key{}
	for i := range key {
		key[i] = byte(i)
	}
	return &Reconciler{
		domains: &fakeSendmailDomainRepo{rows: domains},
		users: &fakeSendmailUserRepo{users: map[string]*models.User{
			"u1": {ID: "u1", Username: strPtr("alice")},
			"u2": {ID: "u2", Username: nil}, // no Linux account
			// Panel-primary shape: admin WITH a synthesized DB username that
			// has no OS counterpart (testserver E2E found the warn-loop).
			"u3": {ID: "u3", Username: strPtr("user_01krhrna2zmp"), IsAdmin: true},
		}},
		serverSettings: &fakeSettingsRepo{srv: &models.ServerSettings{Hostname: "panel.example.tld"}},
		agent:          agent,
		mailboxes:      mailboxes,
		sendmailSSOKey: &key,
		log:            slog.New(slog.DiscardHandler),
	}
}

func TestReconcileSendmailCreds_ProvisionsAndCaches(t *testing.T) {
	agent := &fakeSendmailAgent{}
	mailboxes := &fakeSendmailMailboxRepo{
		byEmail:     map[string]*models.Mailbox{},
		domainNames: map[string]string{"d1": "site.tld", "d2": "mailless.tld"},
	}
	r := sendmailTestReconciler(agent, mailboxes, []models.Domain{
		{ID: "d1", Name: "site.tld", UserID: "u1", EmailEnabled: true},
		{ID: "d2", Name: "mailless.tld", UserID: "u1", EmailEnabled: false},
	})
	ctx := context.Background()

	r.reconcileSendmailCreds(ctx)

	// Both domains — including EmailEnabled=false — get a SendOnly mailbox.
	if len(mailboxes.created) != 2 {
		t.Fatalf("created %d mailboxes, want 2", len(mailboxes.created))
	}
	for _, mb := range mailboxes.created {
		if !mb.SendOnly {
			t.Errorf("relay mailbox %s must be SendOnly", mb.ID)
		}
		if mb.LocalPart != "noreply" {
			t.Errorf("local part = %q", mb.LocalPart)
		}
		if len(mb.PasswordEnc) == 0 {
			t.Errorf("relay mailbox %s missing sealed password", mb.ID)
		}
	}

	ensures := agent.byMethod("sendmail.cred.ensure")
	if len(ensures) != 2 {
		t.Fatalf("cred.ensure calls = %d, want 2", len(ensures))
	}
	first := ensures[0].params
	if first["username"] != "alice" || first["host"] != "mail.panel.example.tld" {
		t.Errorf("unexpected ensure params: %v", first)
	}
	if first["email"] != "noreply@site.tld" || first["domain"] != "site.tld" {
		t.Errorf("unexpected ensure identity: %v", first)
	}
	if pw, _ := first["password"].(string); pw == "" {
		t.Error("ensure carried empty password")
	}
	if len(agent.byMethod("mailbox.create")) != 2 {
		t.Error("Stalwart registry notify missing")
	}

	// Steady state: fingerprint cache suppresses all calls.
	before := len(agent.calls)
	r.reconcileSendmailCreds(ctx)
	if len(agent.calls) != before {
		t.Fatalf("steady-state tick made %d extra calls", len(agent.calls)-before)
	}
}

func TestReconcileSendmailCreds_ExistingSealedPasswordReused(t *testing.T) {
	agent := &fakeSendmailAgent{}
	key := ssokey.Key{}
	for i := range key {
		key[i] = byte(i)
	}
	enc, err := key.Seal([]byte("known-plaintext"))
	if err != nil {
		t.Fatal(err)
	}
	mailboxes := &fakeSendmailMailboxRepo{byEmail: map[string]*models.Mailbox{
		"noreply@site.tld": {ID: "mb1", LocalPart: "noreply", PasswordEnc: enc, SendOnly: true},
	}}
	r := sendmailTestReconciler(agent, mailboxes, []models.Domain{
		{ID: "d1", Name: "site.tld", UserID: "u1"},
	})

	r.reconcileSendmailCreds(context.Background())

	if len(mailboxes.created) != 0 || len(mailboxes.rotated) != 0 {
		t.Fatalf("existing sealed mailbox must be reused (created=%d rotated=%d)", len(mailboxes.created), len(mailboxes.rotated))
	}
	ensures := agent.byMethod("sendmail.cred.ensure")
	if len(ensures) != 1 || ensures[0].params["password"] != "known-plaintext" {
		t.Fatalf("cred.ensure must carry the unsealed password, got %v", ensures)
	}
}

func TestReconcileSendmailCreds_LegacyRowRotates(t *testing.T) {
	agent := &fakeSendmailAgent{}
	mailboxes := &fakeSendmailMailboxRepo{byEmail: map[string]*models.Mailbox{
		"noreply@site.tld": {ID: "mb1", LocalPart: "noreply", PasswordEnc: nil, SendOnly: true},
	}}
	r := sendmailTestReconciler(agent, mailboxes, []models.Domain{
		{ID: "d1", Name: "site.tld", UserID: "u1"},
	})

	r.reconcileSendmailCreds(context.Background())

	if len(mailboxes.rotated) != 1 {
		t.Fatalf("legacy row must rotate, rotated=%v", mailboxes.rotated)
	}
	if len(agent.byMethod("mailbox.set_password")) != 1 {
		t.Error("rotate must invalidate the Stalwart auth cache")
	}
	if len(agent.byMethod("sendmail.cred.ensure")) != 1 {
		t.Error("cred.ensure missing after rotate")
	}
}

func TestReconcileSendmailCreds_SkipsAndRetries(t *testing.T) {
	agent := &fakeSendmailAgent{failEnsure: true}
	mailboxes := &fakeSendmailMailboxRepo{
		byEmail:     map[string]*models.Mailbox{},
		domainNames: map[string]string{"d1": "site.tld", "d-admin": "adminsite.tld"},
	}
	r := sendmailTestReconciler(agent, mailboxes, []models.Domain{
		{ID: "d-nouser", Name: "nouser.tld", UserID: "u2"},      // no Linux user
		{ID: "d-admin", Name: "panelhost.tld", UserID: "u3"},    // admin w/ synthesized username
		{ID: "d1", Name: "site.tld", UserID: "u1"},
	})
	ctx := context.Background()

	r.reconcileSendmailCreds(ctx)
	if n := len(agent.byMethod("sendmail.cred.ensure")); n != 1 {
		t.Fatalf("no-OS-user and admin domains must be skipped (ensure calls=%d)", n)
	}
	if len(mailboxes.created) != 1 {
		t.Fatalf("skipped domains must not get relay mailboxes (created=%d)", len(mailboxes.created))
	}

	// Failed ensure is retried next tick (not cached as done).
	agent.failEnsure = false
	r.reconcileSendmailCreds(ctx)
	if n := len(agent.byMethod("sendmail.cred.ensure")); n != 2 {
		t.Fatalf("failed domain must retry (ensure calls=%d, want 2)", n)
	}
	// And after success, steady state.
	r.reconcileSendmailCreds(ctx)
	if n := len(agent.byMethod("sendmail.cred.ensure")); n != 2 {
		t.Fatalf("converged domain re-called (ensure calls=%d)", n)
	}
}

func TestReconcileSendmailCreds_HumanNoreplyNeverTouched(t *testing.T) {
	agent := &fakeSendmailAgent{}
	// Migrated-account shape: a REAL noreply@ with an imported hash and no
	// PasswordEnc. Rotating it would break the owner's IMAP/webmail login.
	human := &models.Mailbox{ID: "mb-human", LocalPart: "noreply", PasswordHash: "imported", PasswordEnc: nil, SendOnly: false}
	mailboxes := &fakeSendmailMailboxRepo{
		byEmail:     map[string]*models.Mailbox{"noreply@site.tld": human},
		domainNames: map[string]string{"d1": "site.tld"},
	}
	r := sendmailTestReconciler(agent, mailboxes, []models.Domain{
		{ID: "d1", Name: "site.tld", UserID: "u1"},
	})

	r.reconcileSendmailCreds(context.Background())

	if len(mailboxes.rotated) != 0 {
		t.Fatal("human noreply@ was rotated — this breaks the owner's login")
	}
	if human.PasswordHash != "imported" {
		t.Fatal("human noreply@ hash changed")
	}
	// The fallback identity carries the relay instead.
	if len(mailboxes.created) != 1 || mailboxes.created[0].LocalPart != "jabali-noreply" {
		t.Fatalf("expected jabali-noreply fallback, created=%+v", mailboxes.created)
	}
	ensures := agent.byMethod("sendmail.cred.ensure")
	if len(ensures) != 1 || ensures[0].params["email"] != "jabali-noreply@site.tld" {
		t.Fatalf("cred must carry the fallback identity, got %v", ensures)
	}
}

func TestReconcileSendmailCreds_BothNamesHumanSkips(t *testing.T) {
	agent := &fakeSendmailAgent{}
	mailboxes := &fakeSendmailMailboxRepo{
		byEmail: map[string]*models.Mailbox{
			"noreply@site.tld":        {ID: "h1", LocalPart: "noreply", SendOnly: false},
			"jabali-noreply@site.tld": {ID: "h2", LocalPart: "jabali-noreply", SendOnly: false},
		},
		domainNames: map[string]string{"d1": "site.tld"},
	}
	r := sendmailTestReconciler(agent, mailboxes, []models.Domain{
		{ID: "d1", Name: "site.tld", UserID: "u1"},
	})
	ctx := context.Background()

	r.reconcileSendmailCreds(ctx)
	if len(mailboxes.created) != 0 || len(mailboxes.rotated) != 0 || len(agent.byMethod("sendmail.cred.ensure")) != 0 {
		t.Fatal("domain with both names taken must be skipped entirely")
	}
	// And the skip is cached — no per-tick warn/probe churn.
	r.reconcileSendmailCreds(ctx)
	if len(agent.calls) != 0 {
		t.Fatal("skipped domain must stay quiet on later ticks")
	}
}

func TestReconcileSendmailCreds_NoHostnameNoop(t *testing.T) {
	agent := &fakeSendmailAgent{}
	mailboxes := &fakeSendmailMailboxRepo{byEmail: map[string]*models.Mailbox{}}
	r := sendmailTestReconciler(agent, mailboxes, []models.Domain{
		{ID: "d1", Name: "site.tld", UserID: "u1"},
	})
	r.serverSettings = &fakeSettingsRepo{srv: &models.ServerSettings{Hostname: ""}}

	r.reconcileSendmailCreds(context.Background())
	if len(agent.calls) != 0 {
		t.Fatalf("no hostname must be a no-op, calls=%v", agent.calls)
	}
}
