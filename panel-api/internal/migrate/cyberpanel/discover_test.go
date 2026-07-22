package cyberpanel

import (
	"context"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// route pairs a distinctive SQL substring with the canned TAB-separated output
// `mysql -N -B` would return, so the fake session answers each of
// DescribeAccount's queries independently without a live host.
type route struct{ contains, out string }

func fakeRouted(routes ...route) *session {
	s := &session{dbName: "cyberpanel", commandTimeout: time.Second}
	s.run = func(_ context.Context, _ time.Duration, cmd string) ([]byte, error) {
		for _, r := range routes {
			if strings.Contains(cmd, r.contains) {
				return []byte(r.out), nil
			}
		}
		return nil, nil // unmatched query (e.g. no child/alias rows)
	}
	return s
}

// fakeSession answers only the ListAccounts query (domain \t externalApp).
func fakeSession(accountsOut string) *session {
	return fakeRouted(route{"domain, externalApp", accountsOut})
}

// fullAccount wires every DescribeAccount query for the smoke sample account.
func fullAccount() *session {
	return fakeRouted(
		route{"domain, externalApp", "smoke.jabalitest.com\tsmoke6221\n"},
		route{"id, phpSelection", "1\tPHP 8.1\tsmoke6221\n"},
		route{"databases_databases", "smokedb\tsmokeuser\n"},
		route{"e_users", "info@smoke.jabalitest.com\t0\n"},
		route{"records r", "smoke.jabalitest.com\tA\t192.0.2.10\t3600\t0\n"},
		route{"crontab -u", "30 3 * * * /usr/bin/php /home/smoke.jabalitest.com/public_html/cron.php\n"},
		route{"childdomains", ""},
		route{"aliasdomains", ""},
	)
}

func TestListAccounts_ParsesWebsites(t *testing.T) {
	d := New()
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

func TestDescribeAccount_PopulatesAreas(t *testing.T) {
	d := New()
	m, err := d.DescribeAccount(context.Background(), fullAccount(), "smoke.jabalitest.com")
	if err != nil {
		t.Fatalf("DescribeAccount: %v", err)
	}
	if m.SchemaVersion != migrate.ManifestSchemaVersion || m.Source.Kind != models.MigrationSourceCyberPanel {
		t.Errorf("source/schema wrong: %+v v%d", m.Source, m.SchemaVersion)
	}
	// Primary domain.
	if len(m.Domains) != 1 || !m.Domains[0].IsPrimary || m.Domains[0].Name != "smoke.jabalitest.com" {
		t.Fatalf("domains = %+v, want the primary website", m.Domains)
	}
	if m.Domains[0].DocRoot != "/home/smoke.jabalitest.com/public_html" || m.Domains[0].PHPVer != "8.1" {
		t.Errorf("primary domain docroot/php wrong: %+v", m.Domains[0])
	}
	// Database.
	if len(m.Databases) != 1 || m.Databases[0].Name != "smokedb" || m.Databases[0].GrantUser != "smokeuser" || m.Databases[0].Engine != "mysql" {
		t.Fatalf("databases = %+v, want smokedb/smokeuser", m.Databases)
	}
	// Mailbox.
	if len(m.Mailboxes) != 1 || m.Mailboxes[0].Address != "info@smoke.jabalitest.com" {
		t.Fatalf("mailboxes = %+v, want info@", m.Mailboxes)
	}
	if m.Mailboxes[0].MaildirPath != "/home/vmail/smoke.jabalitest.com/info/" {
		t.Errorf("maildir path wrong: %q", m.Mailboxes[0].MaildirPath)
	}
	// DNS zone (records JOIN domains).
	if len(m.DNSZones) != 1 || m.DNSZones[0].Origin != "smoke.jabalitest.com" || len(m.DNSZones[0].Records) != 1 {
		t.Fatalf("dns zones = %+v, want one zone with one record", m.DNSZones)
	}
	if r := m.DNSZones[0].Records[0]; r.Type != "A" || r.Content != "192.0.2.10" || r.TTL != 3600 {
		t.Errorf("dns record wrong: %+v", r)
	}
	// Cron (crontab of the externalApp user).
	if len(m.Cron) != 1 || m.Cron[0].Schedule != "30 3 * * *" || m.Cron[0].RunAs != "smoke6221" {
		t.Fatalf("cron = %+v, want one job run as smoke6221", m.Cron)
	}
	if m.Cron[0].Command != "/usr/bin/php /home/smoke.jabalitest.com/public_html/cron.php" {
		t.Errorf("cron command wrong: %q", m.Cron[0].Command)
	}
}

func TestSplitCronLine(t *testing.T) {
	sc, cmd := splitCronLine("30 3 * * * /usr/bin/php /app/cron.php")
	if sc != "30 3 * * *" || cmd != "/usr/bin/php /app/cron.php" {
		t.Errorf("5-field split = %q / %q", sc, cmd)
	}
	sc, cmd = splitCronLine("@daily /usr/bin/backup.sh")
	if sc != "@daily" || cmd != "/usr/bin/backup.sh" {
		t.Errorf("@-form split = %q / %q", sc, cmd)
	}
	if _, cmd := splitCronLine("PATH=/usr/bin"); cmd != "" {
		t.Errorf("env assignment must yield no command, got %q", cmd)
	}
}

func TestDescribeAccount_SingleTenantAutodetect(t *testing.T) {
	d := New()
	m, err := d.DescribeAccount(context.Background(), fullAccount(), "root")
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

func TestParsePHP(t *testing.T) {
	for in, want := range map[string]string{"PHP 8.1": "8.1", "PHP 8.3": "8.3", "8.2": "8.2", "PHP": ""} {
		if got := parsePHP(in); got != want {
			t.Errorf("parsePHP(%q) = %q, want %q", in, got, want)
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
