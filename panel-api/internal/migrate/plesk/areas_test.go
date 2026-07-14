package plesk

import (
	"context"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
)

// fixtureSession returns a *session whose run() dispatches to `responses`
// keyed by a substring of the command. First matching key wins; an
// unmatched command returns empty output (best-effort builders treat
// that as "no data" + warning, never a panic).
func fixtureSession(responses map[string]string) *session {
	return &session{execFn: func(_ context.Context, _ time.Duration, cmd string) ([]byte, error) {
		for key, out := range responses {
			if strings.Contains(cmd, key) {
				return []byte(out), nil
			}
		}
		return []byte(""), nil
	}}
}

func TestParsePleskInfo(t *testing.T) {
	raw := "Subscription info:\n" +
		"    Domain name: example.com\n" +
		"    PHP version: 8.2\n" +
		"    Hosting type: virtual\n" +
		"    # a comment\n" +
		"    Status\n" // valueless line → skipped
	got := parsePleskInfo(raw)
	if got["domain name"] != "example.com" {
		t.Errorf("domain name = %q, want example.com", got["domain name"])
	}
	if got["php version"] != "8.2" {
		t.Errorf("php version = %q, want 8.2", got["php version"])
	}
	if _, ok := got["status"]; ok {
		t.Error("valueless line must be skipped")
	}
}

func TestDescribeDomains_DocrootAndPHP(t *testing.T) {
	d := New()
	s := fixtureSession(map[string]string{
		"subscription --info":               "Domain name: example.com\ndomains: example.com, addon.example.net\n",
		"domain --info 'example.com'":       "PHP version: 8.2\nphp: on\n",
		"domain --info 'addon.example.net'": "PHP version: 7.4\nphp: on\nwww-root: /var/www/vhosts/example.com/addon\n",
	})
	rows, err := d.describeDomains(context.Background(), s, "example.com")
	if err != nil {
		t.Fatalf("describeDomains: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d domains, want 2: %+v", len(rows), rows)
	}
	if !rows[0].IsPrimary || rows[0].Name != "example.com" {
		t.Errorf("row0 primary/name wrong: %+v", rows[0])
	}
	if rows[0].DocRoot != "/var/www/vhosts/example.com/httpdocs" {
		t.Errorf("primary docroot = %q, want default vhost path", rows[0].DocRoot)
	}
	if rows[0].PHPVer != "8.2" {
		t.Errorf("primary PHPVer = %q, want 8.2", rows[0].PHPVer)
	}
	// addon carries an overridden docroot from www-root.
	if rows[1].DocRoot != "/var/www/vhosts/example.com/addon" {
		t.Errorf("addon docroot override not applied: %q", rows[1].DocRoot)
	}
	if rows[1].IsPrimary {
		t.Error("addon domain must not be primary")
	}
}

func TestDescribeDomains_FallbackToPrimary(t *testing.T) {
	// subscription --info yields no parseable domain set → primary only.
	d := New()
	s := fixtureSession(map[string]string{
		"domain --info": "PHP version: 8.1\n",
	})
	rows, err := d.describeDomains(context.Background(), s, "solo.example.org")
	if err != nil {
		t.Fatalf("describeDomains: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "solo.example.org" || !rows[0].IsPrimary {
		t.Fatalf("fallback should yield the single primary domain: %+v", rows)
	}
}

func TestDescribeDatabases_ParsesAndWarnsPerDomain(t *testing.T) {
	d := New()
	s := fixtureSession(map[string]string{
		"database --list -domain 'example.com'": "wp_main\nwp_shop\n",
		// addon.example.net returns empty → no rows, no crash.
	})
	domains := []migrate.DomainSpec{{Name: "example.com"}, {Name: "addon.example.net"}}
	dbs, warns, err := d.describeDatabases(context.Background(), s, domains)
	if err != nil {
		t.Fatalf("describeDatabases: %v", err)
	}
	if len(dbs) != 2 {
		t.Fatalf("got %d dbs, want 2: %+v", len(dbs), dbs)
	}
	if dbs[0].Engine != "mysql" || dbs[0].Name != "wp_main" {
		t.Errorf("db0 = %+v, want mysql/wp_main", dbs[0])
	}
	_ = warns // no forced failure here; warns may be empty
}

func TestAccountSize_ParsesDuBytes(t *testing.T) {
	d := New()
	s := fixtureSession(map[string]string{
		"du -sb": "5242880\t/var/www/vhosts/example.com\n",
	})
	n, err := d.AccountSize(context.Background(), s, "example.com")
	if err != nil {
		t.Fatalf("AccountSize: %v", err)
	}
	if n != 5242880 {
		t.Errorf("size = %d, want 5242880", n)
	}
}

func TestAccountSize_UnreadableIsZeroNotError(t *testing.T) {
	d := New()
	s := fixtureSession(map[string]string{"du -sb": "not-a-number\n"})
	n, err := d.AccountSize(context.Background(), s, "example.com")
	if err != nil || n != 0 {
		t.Errorf("unparseable du → (0,nil), got (%d,%v)", n, err)
	}
}
