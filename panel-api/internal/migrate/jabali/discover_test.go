package jabali

import (
	"context"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// fakeSession answers `jabali user list --json` with the given JSON body; any
// other command returns empty. Exercises the JSON-parsing logic without SSH.
func fakeSession(userListJSON string) *session {
	s := &session{commandTimeout: time.Second}
	s.run = func(_ context.Context, _ time.Duration, cmd string) ([]byte, error) {
		if strings.Contains(cmd, "user list") {
			return []byte(userListJSON), nil
		}
		return []byte("{}"), nil
	}
	return s
}

const twoTenants = `{"total":4,"users":[
  {"id":"01A","email":"alice@x.com","username":"alice","is_admin":false,"suspended":false},
  {"id":"01B","email":"admin@x.com","username":"root_admin","is_admin":true},
  {"id":"01C","email":"bob@x.com","username":"bob","is_admin":false,"suspended":true},
  {"id":"01D","email":"noname@x.com","username":"","is_admin":false}
]}`

func TestListAccounts_SkipsAdminsAndUsernameless(t *testing.T) {
	d := New()
	accts, err := d.ListAccounts(context.Background(), fakeSession(twoTenants))
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	// Only alice + bob: admin (root_admin) and the username-less row are dropped.
	if len(accts) != 2 {
		t.Fatalf("want 2 accounts, got %d: %+v", len(accts), accts)
	}
	byID := map[string]migrate.AccountSummary{}
	for _, a := range accts {
		byID[a.ID] = a
	}
	if _, ok := byID["alice"]; !ok {
		t.Error("alice must be listed")
	}
	if b, ok := byID["bob"]; !ok || !b.Suspended {
		t.Errorf("bob must be listed and suspended, got %+v", b)
	}
	if _, ok := byID["root_admin"]; ok {
		t.Error("admin must NOT be listed")
	}
}

func TestDescribeAccount_ScaffoldForValid(t *testing.T) {
	d := New()
	m, err := d.DescribeAccount(context.Background(), fakeSession(twoTenants), "alice")
	if err != nil {
		t.Fatalf("DescribeAccount: %v", err)
	}
	if m.SchemaVersion != migrate.ManifestSchemaVersion {
		t.Errorf("schema version = %d, want %d", m.SchemaVersion, migrate.ManifestSchemaVersion)
	}
	if m.Source.Kind != models.MigrationSourceJabali || m.Source.User != "alice" {
		t.Errorf("bad source ref: %+v", m.Source)
	}
	// Phase 1 returns empty (non-nil) area slices for the Phase 2 builders.
	if m.Domains == nil || m.Databases == nil || m.Mailboxes == nil {
		t.Error("area slices must be non-nil scaffolds")
	}
}

func TestDescribeAccount_RejectsBadUsername(t *testing.T) {
	d := New()
	if _, err := d.DescribeAccount(context.Background(), fakeSession(twoTenants), "bad name!"); err == nil {
		t.Error("a non-username id must be rejected before any source call")
	}
}

func TestDescribeAccount_UnknownAmongManyErrors(t *testing.T) {
	d := New()
	_, err := d.DescribeAccount(context.Background(), fakeSession(twoTenants), "carol")
	if err == nil || !strings.Contains(err.Error(), "pick one of") {
		t.Errorf("unknown account among several must list choices, got %v", err)
	}
}

func TestDescribeAccount_SingleTenantPivots(t *testing.T) {
	single := `{"total":1,"users":[{"id":"01A","email":"solo@x.com","username":"solo","is_admin":false}]}`
	d := New()
	m, err := d.DescribeAccount(context.Background(), fakeSession(single), "wrongname")
	if err != nil {
		t.Fatalf("single-tenant source must pivot to the only account, got %v", err)
	}
	if m.Source.User != "solo" {
		t.Errorf("must pivot to 'solo', got %q", m.Source.User)
	}
}

// probeSession routes the two version commands so probeJabali can be exercised
// without SSH: --json returns jsonOut, plain returns plainOut (empty = error).
func probeSession(jsonOut, plainOut string, plainErr bool) *session {
	s := &session{commandTimeout: time.Second}
	s.run = func(_ context.Context, _ time.Duration, cmd string) ([]byte, error) {
		if strings.Contains(cmd, "version --json") {
			if jsonOut == "" {
				return nil, context.Canceled // simulate old CLI: --json unsupported
			}
			return []byte(jsonOut), nil
		}
		if plainErr {
			return nil, context.Canceled
		}
		return []byte(plainOut), nil
	}
	return s
}

func TestProbeJabali(t *testing.T) {
	ctx := context.Background()
	// Modern source: --json parses.
	if err := probeJabali(ctx, probeSession(`{"repo_head":"abc","os":"linux"}`, "", false), time.Second); err != nil {
		t.Errorf("json version must pass: %v", err)
	}
	// Old source: --json unsupported, plain `jabali version` proves it.
	if err := probeJabali(ctx, probeSession("", "version:        42f145db\n", false), time.Second); err != nil {
		t.Errorf("plain-version fallback must pass: %v", err)
	}
	// Not Jabali: --json empty AND plain output has no version marker.
	if err := probeJabali(ctx, probeSession("", "bash: jabali: command not found", false), time.Second); err == nil {
		t.Error("non-Jabali output must be rejected")
	}
	// Both commands error (no CLI at all).
	if err := probeJabali(ctx, probeSession("", "", true), time.Second); err == nil {
		t.Error("a missing CLI must be rejected")
	}
}

func TestJabaliRegisteredInRegistry(t *testing.T) {
	d, err := migrate.Get(models.MigrationSourceJabali)
	if err != nil {
		t.Fatalf("jabali kind must be registered: %v", err)
	}
	if d == nil {
		t.Fatal("registry returned nil discoverer")
	}
}
