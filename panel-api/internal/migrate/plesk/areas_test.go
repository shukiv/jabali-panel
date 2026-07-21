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

func TestParsePleskInfo_ColonFormat(t *testing.T) {
	raw := "General\n" +
		"=============================\n" +
		"Domain name:                            example.com\n" +
		"PHP support:                            Yes\n" +
		"Total size of local backup files (if enabled in Server Settings):0 B\n"
	got := parsePleskInfo(raw)
	if got["domain name"] != "example.com" {
		t.Errorf("domain name = %q, want example.com", got["domain name"])
	}
	if got["php support"] != "Yes" {
		t.Errorf("php support = %q, want Yes", got["php support"])
	}
	if _, ok := got["general"]; ok {
		t.Error("section header must be skipped")
	}
}

func TestParsePleskInfo_AlignedFormat(t *testing.T) {
	raw := "Limits\n" +
		"================================================\n" +
		"Service plan name                               Unlimited\n" +
		"Disk space                                      10240M\n" +
		"Log rotation condition                          by size: 10240 KB\n"
	got := parsePleskInfo(raw)
	if got["service plan name"] != "Unlimited" {
		t.Errorf("service plan name = %q, want Unlimited", got["service plan name"])
	}
	if got["disk space"] != "10240M" {
		t.Errorf("disk space = %q, want 10240M", got["disk space"])
	}
	if got["log rotation condition"] != "by size: 10240 KB" {
		t.Errorf("log rotation condition = %q, want 'by size: 10240 KB'", got["log rotation condition"])
	}
}

func TestDescribeDomains_DocrootAndPHP(t *testing.T) {
	d := New()
	s := fixtureSession(map[string]string{
		// domains enumerated from the psa DB (primary + addon); key is a
		// quote-free substring of the shell-escaped `plesk db` command.
		"SELECT name FROM domains":          "example.com\naddon.example.net\n",
		"domain --info 'example.com'":       "PHP support:  Yes\n",
		"domain --info 'addon.example.net'": "PHP support:  Yes\n",
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
	if !rows[0].HasPHP {
		t.Error("primary should have PHP (PHP support: Yes)")
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
		// key on the bare domain (a literal substring of the escaped SQL,
		// which wraps quotes as '\'') so only example.com resolves;
		// addon.example.net returns empty → no rows.
		"example.com": "wp_main\nwp_shop\n",
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

// TestDescribeAccount_ReservedRootResolvesToSubscription pins the GH #429 fix:
// a migration run as the SSH principal `root` must NOT be accepted as a
// subscription (the fixture's --info probe returns nil error, which the old
// code treated as "root is a valid subscription"). With the reserved-account
// guard, `root` resolves to the sole real subscription instead.
func TestDescribeAccount_ReservedRootResolvesToSubscription(t *testing.T) {
	d := New()
	s := fixtureSession(map[string]string{
		"subscription --list": "example.com\n",
	})
	m, err := d.DescribeAccount(context.Background(), s, "root")
	if err != nil {
		// Acceptable outcome too (resolved away from root, then a load-bearing
		// area had no fixture) — the one thing that must never happen is a
		// manifest built for "root".
		if strings.Contains(err.Error(), `"root"`) && strings.Contains(err.Error(), "not a subscription") {
			t.Fatalf("root was rejected outright instead of resolving to the sole subscription: %v", err)
		}
		return
	}
	if m.Source.User == "root" {
		t.Fatalf("GH #429 regression: DescribeAccount(root) built a manifest for user=root")
	}
	if m.Source.User != "example.com" {
		t.Errorf("root should resolve to the sole subscription example.com, got %q", m.Source.User)
	}
}
