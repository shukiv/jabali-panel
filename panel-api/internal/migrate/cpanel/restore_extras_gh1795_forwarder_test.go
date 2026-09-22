package cpanel

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1795 follow-up: an imported (opted-in) forwarder must be pushed to
// Stalwart, and pushed for the MAILBOX's own address — not the alias source
// line. Before the convergence wiring, ImportExtras created the DB row and never
// called forwarder.apply, so the forwarder was inert until a UI edit (RED: 0
// forwarder.apply calls). This asserts one call, with mailbox_email == the
// mailbox address (which is what accountIDByEmail resolves).

type fwdConvergeAgent struct {
	fwApply          int
	lastMailboxEmail string
}

func (a *fwdConvergeAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	if cmd == "forwarder.apply" {
		a.fwApply++
		if m, ok := params.(map[string]any); ok {
			a.lastMailboxEmail, _ = m["mailbox_email"].(string)
		}
	}
	return json.RawMessage(`{}`), nil
}

type fwdMailboxRepo struct {
	repository.MailboxRepository
	mb *models.Mailbox
}

func (r *fwdMailboxRepo) FindByEmail(_ context.Context, email string) (*models.Mailbox, error) {
	if r.mb != nil && r.mb.EmailCached == email {
		return r.mb, nil
	}
	return nil, repository.ErrNotFound
}

type fwdForwarderRepo struct {
	repository.EmailForwarderRepository
	rows []models.EmailForwarder
}

func (r *fwdForwarderRepo) Create(_ context.Context, e *models.EmailForwarder) error {
	r.rows = append(r.rows, *e)
	return nil
}

func (r *fwdForwarderRepo) ListByMailboxID(_ context.Context, mailboxID string, _ repository.ListOptions) ([]models.EmailForwarder, int64, error) {
	var out []models.EmailForwarder
	for _, f := range r.rows {
		if f.MailboxID != nil && *f.MailboxID == mailboxID {
			out = append(out, f)
		}
	}
	return out, int64(len(out)), nil
}

func TestImportExtras_ConvergesImportedForwarder(t *testing.T) {
	parsed := writeUserdata(t, map[string]string{"example.com": "ea-php81"})

	// A cpanel homedir carrying one forwarder: sales@example.com → fwd@out.org.
	home := t.TempDir()
	aliasesDir := filepath.Join(home, "etc", "example.com")
	if err := os.MkdirAll(aliasesDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(aliasesDir, "aliases"), []byte("sales: fwd@out.org\n"), 0o644); err != nil {
		t.Fatalf("write aliases: %v", err)
	}
	parsed.HomeDir = home

	mb := &models.Mailbox{ID: "mb-sales", EmailCached: "sales@example.com", LocalPart: "sales"}
	ag := &fwdConvergeAgent{}
	fwdRepo := &fwdForwarderRepo{}

	_, err := ImportExtras(
		context.Background(),
		&bindDomainRepo{},       // domainsRepo
		&fwdMailboxRepo{mb: mb}, // mailboxesRepo
		fwdRepo,                 // forwardersRepo
		nil,                     // autoRespondersRepo
		nil,                     // filtersRepo
		nil,                     // poolsRepo (nil → no php.pool.apply)
		ag,                      // agentCli
		parsed,
		"user-123", "someuser",
		true, // preserveMailRouting → forwarder enabled → must converge
	)
	if err != nil {
		t.Fatalf("ImportExtras: %v", err)
	}
	if ag.fwApply != 1 {
		t.Fatalf("forwarder.apply calls = %d, want 1 (imported forwarder must be pushed to Stalwart)", ag.fwApply)
	}
	if ag.lastMailboxEmail != "sales@example.com" {
		t.Errorf("forwarder.apply mailbox_email = %q, want sales@example.com (the mailbox, not the alias source line)", ag.lastMailboxEmail)
	}

	// GH #1795 follow-up: a type='external' forwarder must leave local_part NULL
	// (the source is the mailbox). A non-NULL local_part wrongly occupies the
	// uq_alias_local (domain_id, local_part) slot the schema reserves for
	// aliases, so two external forwards off the same source local — or a later
	// same-local alias — collide on the unique key and get silently dropped.
	if len(fwdRepo.rows) != 1 {
		t.Fatalf("forwarder rows created = %d, want 1", len(fwdRepo.rows))
	}
	if got := fwdRepo.rows[0]; got.Type != "external" {
		t.Fatalf("forwarder type = %q, want external", got.Type)
	} else if got.LocalPart != nil {
		t.Errorf("external forwarder local_part = %q, want NULL (nil) — must not consume the uq_alias_local slot", *got.LocalPart)
	}
}
