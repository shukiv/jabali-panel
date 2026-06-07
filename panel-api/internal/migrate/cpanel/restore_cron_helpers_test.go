package cpanel

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsCronEnvLine(t *testing.T) {
	env := []string{
		`MAILTO=""`,
		`MAILTO="ops@example.com"`,
		`SHELL=/bin/bash`,
		`PATH=/usr/local/bin:/usr/bin:/bin`,
		`HOME=/home/user`,
		`FOO = bar`,
	}
	for _, l := range env {
		if !isCronEnvLine(l) {
			t.Errorf("isCronEnvLine(%q) = false, want true", l)
		}
	}
	jobs := []string{
		`*/5 * * * * php /home/u/public_html/wp-cron.php`,
		`@daily wp cron event run --path=/home/u/public_html`,
		`0 0 * * * curl https://example.com/wp-cron.php`,
		`# a comment`,
	}
	for _, l := range jobs {
		if isCronEnvLine(l) {
			t.Errorf("isCronEnvLine(%q) = true, want false", l)
		}
	}
}

func TestStripOutputRedirect(t *testing.T) {
	cases := map[string]string{
		`php /home/u/public_html/wp-cron.php >/dev/null 2>&1`: `php /home/u/public_html/wp-cron.php`,
		`php x.php >/dev/null`:                                `php x.php`,
		`php x.php > /dev/null 2>&1`:                          `php x.php`,
		`php x.php 2>&1 >/dev/null`:                           `php x.php`,
		`php x.php &>/dev/null`:                               `php x.php`,
		`php x.php`:                                           `php x.php`,
		// redirect to a REAL file is left intact (will fail validation).
		`php x.php >/var/log/cron.log`: `php x.php >/var/log/cron.log`,
	}
	for in, want := range cases {
		if got := stripOutputRedirect(in); got != want {
			t.Errorf("stripOutputRedirect(%q) = %q, want %q", in, got, want)
		}
	}
}

// cronRewriteFixture builds a docroots map for "own.example.com" pointing
// at a temp dir, and drops the named .php files in it.
func cronRewriteFixture(t *testing.T, files ...string) map[string]string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("<?php\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return map[string]string{"own.example.com": dir}
}

func TestRewriteSelfCurlCron(t *testing.T) {
	dr := cronRewriteFixture(t, "cron-pong.php", "wp-cron.php")
	docroot := dr["own.example.com"]

	type tc struct {
		name    string
		cmd     string
		wantOK  bool
		wantCmd string // when wantOK
	}
	cases := []tc{
		{"own-domain-php", "curl https://own.example.com/cron-pong.php", true, "php " + filepath.Join(docroot, "cron-pong.php")},
		{"wget-O-devnull", "wget -q -O /dev/null https://own.example.com/wp-cron.php", true, "php " + filepath.Join(docroot, "wp-cron.php")},
		{"query-string", "curl https://own.example.com/cron-pong.php?tok=1", false, ""},
		{"foreign-host", "curl https://evil.com/x.php", false, ""},
		{"ssrf-metadata", "curl http://169.254.169.254/latest/meta-data", false, ""},
		{"danger-flag-T", "curl -T /home/u/secret https://own.example.com/cron-pong.php", false, ""},
		{"danger-o-realfile", "curl -o /tmp/out https://own.example.com/cron-pong.php", false, ""},
		{"missing-file", "curl https://own.example.com/does-not-exist.php", false, ""},
		{"not-php", "curl https://own.example.com/cron-pong.txt", false, ""},
		{"benign-silent-fail", "curl -s -f https://own.example.com/cron-pong.php", true, "php " + filepath.Join(docroot, "cron-pong.php")},
	}
	for _, c := range cases {
		got, ok, reason := rewriteSelfCurlCron(c.cmd, dr)
		if ok != c.wantOK {
			t.Errorf("%s: rewriteSelfCurlCron(%q) ok=%v, want %v (reason=%q)", c.name, c.cmd, ok, c.wantOK, reason)
			continue
		}
		if c.wantOK && got != c.wantCmd {
			t.Errorf("%s: got %q, want %q", c.name, got, c.wantCmd)
		}
	}
}
