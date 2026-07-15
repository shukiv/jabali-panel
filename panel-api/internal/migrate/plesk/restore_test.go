package plesk

import (
	"strings"
	"testing"
)

func TestParseTSVManifest(t *testing.T) {
	content := "lychee-fruits.co.il\t/var/www/vhosts/lychee-fruits.co.il/httpdocs\n" +
		"\n# comment\n" +
		"addon.example.net\t/var/www/vhosts/addon.example.net/httpdocs\r\n" +
		"broken-no-tab\n"
	got := parseTSVManifest(content)
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2 (blank/comment/no-tab skipped): %+v", len(got), got)
	}
	if got[0].Name != "lychee-fruits.co.il" || got[0].Path != "/var/www/vhosts/lychee-fruits.co.il/httpdocs" {
		t.Errorf("entry0 = %+v", got[0])
	}
	if got[1].Name != "addon.example.net" { // trailing \r stripped
		t.Errorf("entry1 name = %q", got[1].Name)
	}
}

func TestParseLinesManifest(t *testing.T) {
	got := parseLinesManifest("lycheefr_wp2\n\n# x\nother_db\n")
	if len(got) != 2 || got[0] != "lycheefr_wp2" || got[1] != "other_db" {
		t.Errorf("dbs = %v", got)
	}
}

func TestDBDumpCommand_ReadOnlyAndEscaped(t *testing.T) {
	cmd := dbDumpCommand("lycheefr_wp2")
	// read-only snapshot, admin creds from the Plesk shadow file.
	for _, want := range []string{
		"mysqldump", "--single-transaction", "-uadmin",
		"/etc/psa/.psa.shadow", "'lycheefr_wp2'",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("dbDumpCommand missing %q in %q", want, cmd)
		}
	}
	// must NOT be a mutating verb.
	for _, bad := range []string{"mysql -e", "DROP", "DELETE", "--where"} {
		if strings.Contains(cmd, bad) {
			t.Errorf("dbDumpCommand must be read-only, found %q", bad)
		}
	}
	// a hostile db name is single-quote-escaped, not injected.
	evil := dbDumpCommand("a'; DROP DATABASE x; --")
	if !strings.Contains(evil, `'a'\''; DROP DATABASE x; --'`) {
		t.Errorf("db name not escaped: %q", evil)
	}
}

func TestDocrootRemotePath(t *testing.T) {
	e := ManifestEntry{Name: "d", Path: "/var/www/vhosts/d/httpdocs"}
	if docrootRemotePath(e) != "/var/www/vhosts/d/httpdocs" {
		t.Errorf("got %q", docrootRemotePath(e))
	}
}
