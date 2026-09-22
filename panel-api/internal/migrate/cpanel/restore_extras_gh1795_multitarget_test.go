package cpanel

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1795 follow-up: a cpanel valias line can list several comma-separated
// targets (`sales: a@x, b@y`), and the "forward + keep a local copy" idiom is
// encoded as the source forwarding to ITSELF plus the external target
// (`sales: sales@dom, fwd@out`). Before the split, parseAliases persisted the
// whole RHS as one external row (target = "a@x, b@y") → a single malformed
// Sieve redirect. This exercises the split, the keep-copy mapping, and the
// exim-directive skip. All external rows must leave local_part NULL (they must
// not consume the uq_alias_local slot — see the single-target test).

// runFwdImport writes one cpanel `<home>/etc/<dom>/aliases` file with the given
// body and runs ImportExtras for a single source mailbox. Returns the created
// forwarder rows and the agent (to assert forwarder.apply fan-out).
func runFwdImport(t *testing.T, dom, aliasesBody string, mb *models.Mailbox) (*fwdForwarderRepo, *fwdConvergeAgent) {
	t.Helper()
	parsed := writeUserdata(t, map[string]string{dom: "ea-php81"})
	home := t.TempDir()
	aliasesDir := filepath.Join(home, "etc", dom)
	if err := os.MkdirAll(aliasesDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(aliasesDir, "aliases"), []byte(aliasesBody), 0o644); err != nil {
		t.Fatalf("write aliases: %v", err)
	}
	parsed.HomeDir = home

	fwdRepo := &fwdForwarderRepo{}
	ag := &fwdConvergeAgent{}
	if _, err := ImportExtras(
		context.Background(),
		&bindDomainRepo{},
		&fwdMailboxRepo{mb: mb},
		fwdRepo,
		nil, nil, nil,
		ag,
		parsed,
		"user-123", "someuser",
		true, // preserveMailRouting
	); err != nil {
		t.Fatalf("ImportExtras: %v", err)
	}
	return fwdRepo, ag
}

func TestImportExtras_MultiTargetForwarderSplits(t *testing.T) {
	mb := &models.Mailbox{ID: "mb-sales", EmailCached: "sales@example.com", LocalPart: "sales"}
	fwdRepo, ag := runFwdImport(t, "example.com", "sales: a@out.org, b@out.org\n", mb)

	if len(fwdRepo.rows) != 2 {
		t.Fatalf("rows created = %d, want 2 (one external row per comma-separated target)", len(fwdRepo.rows))
	}
	got := []string{fwdRepo.rows[0].Target, fwdRepo.rows[1].Target}
	sort.Strings(got)
	if got[0] != "a@out.org" || got[1] != "b@out.org" {
		t.Errorf("targets = %v, want [a@out.org b@out.org] (RHS must split on comma, not one malformed row)", got)
	}
	for _, r := range fwdRepo.rows {
		if r.Type != "external" || r.LocalPart != nil || r.KeepCopy {
			t.Errorf("row %+v: want type=external, local_part=nil, keep_copy=false", r)
		}
	}
	if ag.fwApply != 1 {
		t.Errorf("forwarder.apply calls = %d, want 1 (one convergence for the mailbox)", ag.fwApply)
	}
}

func TestImportExtras_SelfTargetIsKeepCopy(t *testing.T) {
	mb := &models.Mailbox{ID: "mb-sales", EmailCached: "sales@example.com", LocalPart: "sales"}
	// cpanel "forward and keep a local copy": source forwards to itself + external.
	fwdRepo, _ := runFwdImport(t, "example.com", "sales: sales@example.com, fwd@out.org\n", mb)

	if len(fwdRepo.rows) != 1 {
		t.Fatalf("rows created = %d, want 1 (self-target is the keep-copy marker, not a forward row)", len(fwdRepo.rows))
	}
	r := fwdRepo.rows[0]
	if r.Target != "fwd@out.org" {
		t.Errorf("target = %q, want fwd@out.org (self address must not become a self-redirect loop)", r.Target)
	}
	if !r.KeepCopy {
		t.Errorf("keep_copy = false, want true (self-target encodes 'keep a local copy')")
	}
	if r.LocalPart != nil {
		t.Errorf("local_part = %q, want NULL", *r.LocalPart)
	}
}

func TestImportExtras_EximDirectiveTargetSkipped(t *testing.T) {
	mb := &models.Mailbox{ID: "mb-sales", EmailCached: "sales@example.com", LocalPart: "sales"}
	fwdRepo, _ := runFwdImport(t, "example.com", "sales: |/usr/bin/procmail, fwd@out.org\n", mb)

	if len(fwdRepo.rows) != 1 {
		t.Fatalf("rows created = %d, want 1 (pipe directive target must be skipped, address kept)", len(fwdRepo.rows))
	}
	if fwdRepo.rows[0].Target != "fwd@out.org" {
		t.Errorf("target = %q, want fwd@out.org", fwdRepo.rows[0].Target)
	}
}
