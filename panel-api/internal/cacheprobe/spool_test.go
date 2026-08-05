package cacheprobe

import (
	"strings"
	"testing"
)

// The real pool line, verbatim from install/php/jabali-php-pool.conf.tmpl after
// the JAB-199 fix.
const poolWithSpool = `; Jailbreak defense (ADR-0023 decision 12).
php_admin_value[open_basedir] = /home/alice:/run/mysqld/mysqld.sock:/tmp:/var/tmp:/run/jabali-wp-purge
php_admin_flag[allow_url_fopen] = off
`

// The same line as it stood BEFORE the fix — this is what a box whose pools have
// not been re-rendered still carries.
const poolWithoutSpool = `php_admin_value[open_basedir] = /home/alice:/run/mysqld/mysqld.sock:/tmp:/var/tmp
`

func TestParseOpenBasedir(t *testing.T) {
	got := parseOpenBasedir(poolWithSpool)
	if !strings.Contains(got, PurgeSpoolDir) {
		t.Fatalf("parsed %q, expected it to contain %s", got, PurgeSpoolDir)
	}
	if parseOpenBasedir("; php_admin_value[open_basedir] = /commented/out\n") != "" {
		t.Error("a commented directive was parsed as active")
	}
	if parseOpenBasedir("no directive here\n") != "" {
		t.Error("absent directive should yield empty, not a guess")
	}
}

func TestOpenBasedirAllows(t *testing.T) {
	ob := "/home/alice:/tmp:/run/jabali-wp-purge"

	if !openBasedirAllows(ob, PurgeSpoolDir) {
		t.Error("exact entry not allowed")
	}
	if !openBasedirAllows(ob, "/home/alice/domains/x/public_html") {
		t.Error("path beneath an entry not allowed")
	}
	if openBasedirAllows(ob, "/var/www") {
		t.Error("unrelated path allowed")
	}
}

// Prefix matching on the raw string would let a sibling directory satisfy the
// check — open_basedir entries are path boundaries, not string prefixes.
func TestOpenBasedirAllows_RespectsPathBoundary(t *testing.T) {
	if openBasedirAllows("/run/jabali-wp-purge", "/run/jabali-wp-purge-evil") {
		t.Error("sibling directory satisfied the check via string prefix")
	}
	if openBasedirAllows("/home/alice", "/home/alice-backup/secret") {
		t.Error("/home/alice-backup matched /home/alice")
	}
}

// The regression this guards: a host whose pools predate the fix. The template
// is right, the box is not, and every purge is silently dropped.
func TestPurgeSpoolMissingFromOpenBasedirIsReported(t *testing.T) {
	ob := parseOpenBasedir(poolWithoutSpool)
	if ob == "" {
		t.Fatal("fixture parse failed")
	}
	if openBasedirAllows(ob, PurgeSpoolDir) {
		t.Fatal("pre-fix pool reported as allowing the spool — the guard would never fire")
	}
	// And the post-fix pool must pass, or the guard would fire on every healthy host.
	if !openBasedirAllows(parseOpenBasedir(poolWithSpool), PurgeSpoolDir) {
		t.Fatal("post-fix pool rejected — false positive on every fixed host")
	}
}

// No pool files, or none for this user, means the check cannot tell — and must
// say so rather than reporting a problem.
func TestCheckPurgeSpool_UnknownWhenNoPools(t *testing.T) {
	problem, checked := CheckPurgeSpool([]string{"/nonexistent/*/pool.d/*.conf"}, "alice")
	if checked {
		t.Error("claimed to have checked with no pool files present")
	}
	if problem != "" {
		t.Errorf("reported a problem it could not observe: %s", problem)
	}
}
