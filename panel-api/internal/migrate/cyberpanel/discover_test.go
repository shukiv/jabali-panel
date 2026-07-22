package cyberpanel

import (
	"context"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// fakeSession builds a *session whose run() returns canned mysql output (TAB-
// separated, as `mysql -N -B` produces), exercising the parsing logic without a
// live CyberPanel host.
func fakeSession(out string) *session {
	s := &session{dbName: "cyberpanel", commandTimeout: time.Second}
	s.run = func(_ context.Context, _ time.Duration, _ string) ([]byte, error) {
		return []byte(out), nil
	}
	return s
}

func TestListAccounts_ParsesWebsites(t *testing.T) {
	d := New()
	// domain \t externalApp — one row per website.
	s := fakeSession("smoke.jabalitest.com\tsmoke6221\nshop.example.com\tshop4820\n")
	accts, err := d.ListAccounts(context.Background(), s)
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(accts) != 2 {
		t.Fatalf("want 2 accounts, got %d: %+v", len(accts), accts)
	}
	if accts[0].ID != "smoke.jabalitest.com" || accts[0].Login != "smoke6221" || accts[0].Domain != "smoke.jabalitest.com" {
		t.Errorf("account[0] = %+v, want domain id + externalApp login", accts[0])
	}
	if accts[1].ID != "shop.example.com" || accts[1].Login != "shop4820" {
		t.Errorf("account[1] = %+v, want shop.example.com / shop4820", accts[1])
	}
}

func TestDescribeAccount_EmptyManifestForKnownDomain(t *testing.T) {
	d := New()
	s := fakeSession("smoke.jabalitest.com\tsmoke6221\nshop.example.com\tshop4820\n")
	m, err := d.DescribeAccount(context.Background(), s, "smoke.jabalitest.com")
	if err != nil {
		t.Fatalf("DescribeAccount: %v", err)
	}
	if m.SchemaVersion != migrate.ManifestSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", m.SchemaVersion, migrate.ManifestSchemaVersion)
	}
	if m.Source.Kind != models.MigrationSourceCyberPanel || m.Source.User != "smoke.jabalitest.com" {
		t.Errorf("Source = %+v, want kind=cyberpanel user=smoke.jabalitest.com", m.Source)
	}
}

func TestDescribeAccount_SingleTenantAutodetect(t *testing.T) {
	d := New()
	s := fakeSession("smoke.jabalitest.com\tsmoke6221\n")
	m, err := d.DescribeAccount(context.Background(), s, "root")
	if err != nil {
		t.Fatalf("DescribeAccount autodetect: %v", err)
	}
	if m.Source.User != "smoke.jabalitest.com" {
		t.Errorf("autodetect user = %q, want smoke.jabalitest.com", m.Source.User)
	}
}

func TestDescribeAccount_MultiTenantUnknownErrors(t *testing.T) {
	d := New()
	s := fakeSession("a.example.com\ta1\nb.example.com\tb1\n")
	_, err := d.DescribeAccount(context.Background(), s, "c.example.com")
	if err == nil || !strings.Contains(err.Error(), "pick one of") {
		t.Fatalf("want multi-tenant 'pick one of' error, got %v", err)
	}
}

func TestDescribeAccount_RejectsInjectionyID(t *testing.T) {
	d := New()
	s := fakeSession("smoke.jabalitest.com\tsmoke6221\n")
	for _, bad := range []string{"a'; DROP TABLE websiteFunctions_websites;--", "a b", "a$(whoami)", "../etc/passwd", "UPPER.com"} {
		if _, err := d.DescribeAccount(context.Background(), s, bad); err == nil {
			t.Errorf("accountID %q should be rejected by domainRe", bad)
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

func TestMysqlQuery_BatchAndQuoted(t *testing.T) {
	cmd := mysqlQuery("cyberpanel", "SELECT count(*) FROM websiteFunctions_websites")
	if !strings.Contains(cmd, "-N -B") {
		t.Errorf("query must use -N -B for header-less TAB output: %q", cmd)
	}
	if !strings.Contains(cmd, `'cyberpanel'`) {
		t.Errorf("db name must be single-quoted: %q", cmd)
	}
	if !strings.HasPrefix(strings.TrimSpace(cmd), "mysql ") {
		t.Errorf("must invoke mysql: %q", cmd)
	}
}

func TestDomainRe(t *testing.T) {
	ok := []string{"smoke.jabalitest.com", "a.example.com", "sub.domain.co.uk", "x-y.example.org", "example.com"}
	bad := []string{"", "UPPER.com", "a b", "a'b", "a;b", "../x", "a$(x)", strings.Repeat("x", 255)}
	for _, s := range ok {
		if !domainRe.MatchString(s) {
			t.Errorf("domainRe should accept %q", s)
		}
	}
	for _, s := range bad {
		if domainRe.MatchString(s) {
			t.Errorf("domainRe should reject %q", s)
		}
	}
}

func TestRegistered(t *testing.T) {
	d, err := migrate.Get(models.MigrationSourceCyberPanel)
	if err != nil {
		t.Fatalf("migrate.Get(cyberpanel): %v — init() registration missing?", err)
	}
	if _, ok := d.(migrate.AllowPrivateSetter); !ok {
		t.Error("cyberpanel Discoverer must implement AllowPrivateSetter")
	}
	if _, ok := d.(migrate.PortSetter); !ok {
		t.Error("cyberpanel Discoverer must implement PortSetter")
	}
}
