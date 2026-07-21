package cloudpanel

import (
	"context"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// fakeSession builds a *session whose run() returns canned sqlite output,
// so the SQLite-parsing logic is exercised without a live CloudPanel host.
func fakeSession(out string) *session {
	s := &session{dbPath: "/test/db.sq3", commandTimeout: time.Second}
	s.run = func(_ context.Context, _ time.Duration, _ string) ([]byte, error) {
		return []byte(out), nil
	}
	return s
}

func TestListAccounts_ParsesSiteUsersAndFiltersClp(t *testing.T) {
	// user|comma-joined-domains, one row per site user; clp is CloudPanel's own
	// service account and must be filtered.
	d := New()
	s := fakeSession("alice|a.example.com,b.example.com\nbob|c.example.com\nclp|panel.example.com\n")
	accts, err := d.ListAccounts(context.Background(), s)
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(accts) != 2 {
		t.Fatalf("want 2 accounts (clp filtered), got %d: %+v", len(accts), accts)
	}
	if accts[0].ID != "alice" || accts[0].Login != "alice" || accts[0].Domain != "a.example.com" {
		t.Errorf("account[0] = %+v, want alice / a.example.com primary", accts[0])
	}
	if accts[1].ID != "bob" || accts[1].Domain != "c.example.com" {
		t.Errorf("account[1] = %+v, want bob / c.example.com", accts[1])
	}
}

func TestDescribeAccount_EmptyManifestForKnownUser(t *testing.T) {
	d := New()
	s := fakeSession("alice|a.example.com\nbob|c.example.com\n")
	m, err := d.DescribeAccount(context.Background(), s, "alice")
	if err != nil {
		t.Fatalf("DescribeAccount: %v", err)
	}
	if m.SchemaVersion != migrate.ManifestSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", m.SchemaVersion, migrate.ManifestSchemaVersion)
	}
	if m.Source.Kind != models.MigrationSourceCloudPanel || m.Source.User != "alice" {
		t.Errorf("Source = %+v, want kind=cloudpanel user=alice", m.Source)
	}
	if len(m.Mailboxes) != 0 {
		t.Errorf("CloudPanel has no mail — Mailboxes must be empty, got %d", len(m.Mailboxes))
	}
}

func TestDescribeAccount_SingleTenantAutodetect(t *testing.T) {
	// Operator typed the SSH principal (root) but there is exactly one site
	// user — pivot to it, mirroring the DirectAdmin convenience.
	d := New()
	s := fakeSession("alice|a.example.com\n")
	m, err := d.DescribeAccount(context.Background(), s, "root")
	if err != nil {
		t.Fatalf("DescribeAccount autodetect: %v", err)
	}
	if m.Source.User != "alice" {
		t.Errorf("autodetect user = %q, want alice", m.Source.User)
	}
}

func TestDescribeAccount_MultiTenantUnknownErrors(t *testing.T) {
	d := New()
	s := fakeSession("alice|a\nbob|b\n")
	_, err := d.DescribeAccount(context.Background(), s, "carol")
	if err == nil || !strings.Contains(err.Error(), "pick one of") {
		t.Fatalf("want multi-tenant 'pick one of' error, got %v", err)
	}
}

func TestDescribeAccount_RejectsInjectionyID(t *testing.T) {
	d := New()
	s := fakeSession("alice|a\n")
	for _, bad := range []string{"a'; DROP TABLE site;--", "a b", "a$(whoami)", "../etc", "A_UPPER"} {
		if _, err := d.DescribeAccount(context.Background(), s, bad); err == nil {
			t.Errorf("accountID %q should be rejected by siteUserRe", bad)
		}
	}
}

func TestShellSingleQuote_EscapesQuotes(t *testing.T) {
	got := shellSingleQuote("a'b")
	want := `'a'\''b'`
	if got != want {
		t.Errorf("shellSingleQuote(a'b) = %q, want %q", got, want)
	}
}

func TestSqliteQuery_IsReadonlyAndQuoted(t *testing.T) {
	cmd := sqliteQuery("/home/clp/htdocs/app/data/db.sq3", "SELECT count(*) FROM site")
	if !strings.Contains(cmd, "-readonly") {
		t.Errorf("query must use -readonly to guarantee no source mutation: %q", cmd)
	}
	if !strings.Contains(cmd, `'/home/clp/htdocs/app/data/db.sq3'`) {
		t.Errorf("db path must be single-quoted: %q", cmd)
	}
}

func TestSiteUserRe(t *testing.T) {
	ok := []string{"alice", "web-user", "a_b.c", "_svc", "u123"}
	bad := []string{"", "Alice", "a b", "a'b", "a;b", "1abc", strings.Repeat("x", 65)}
	for _, s := range ok {
		if !siteUserRe.MatchString(s) {
			t.Errorf("siteUserRe should accept %q", s)
		}
	}
	for _, s := range bad {
		if siteUserRe.MatchString(s) {
			t.Errorf("siteUserRe should reject %q", s)
		}
	}
}

func TestRegistered(t *testing.T) {
	d, err := migrate.Get(models.MigrationSourceCloudPanel)
	if err != nil {
		t.Fatalf("migrate.Get(cloudpanel): %v — init() registration missing?", err)
	}
	if _, ok := d.(migrate.AllowPrivateSetter); !ok {
		t.Error("cloudpanel Discoverer must implement AllowPrivateSetter")
	}
	if _, ok := d.(migrate.PortSetter); !ok {
		t.Error("cloudpanel Discoverer must implement PortSetter")
	}
}
