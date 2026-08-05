package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Verbatim from a production host, so a change to the pool template that breaks
// parsing fails here rather than on a customer's cache.
const (
	livePoolOpenBasedir = `php_admin_value[open_basedir] = /home/arizote:/run/mysqld/mysqld.sock:/tmp:/var/tmp:/run/jabali-wp-purge`
	// The pre-JAB-199 form: identical but for the missing spool entry. Two pools
	// on a live box still carried this shape, so this is the state the probe
	// exists to catch, not a hypothetical.
	stalePoolOpenBasedir = `php_admin_value[open_basedir] = /home/arizote:/run/mysqld/mysqld.sock:/tmp:/var/tmp`
	// The comment sitting directly above the directive in the rendered pool. It
	// contains the words "open_basedir" and "hard-coded", and a matcher keying
	// on the substring reads it as the value.
	poolComment = `; Jailbreak defense (ADR-0023 decision 12). open_basedir is hard-coded`
)

func TestPoolOpenBasedir(t *testing.T) {
	t.Run("reads the directive, not the comment above it", func(t *testing.T) {
		body := poolComment + "\n" + livePoolOpenBasedir + "\n"
		got, present := poolOpenBasedir(body)
		if !present {
			t.Fatal("directive present in body but reported absent")
		}
		if strings.Contains(got, "Jailbreak") {
			t.Fatalf("parsed the comment instead of the value: %q", got)
		}
		if !strings.HasPrefix(got, "/home/arizote:") {
			t.Fatalf("value = %q", got)
		}
	})

	t.Run("absent is distinguishable from empty", func(t *testing.T) {
		if _, present := poolOpenBasedir("[pool]\nuser = arizote\n"); present {
			t.Fatal("no directive, but reported present")
		}
		v, present := poolOpenBasedir("php_admin_value[open_basedir] =\n")
		if !present {
			t.Fatal("empty directive is still a directive")
		}
		if v != "" {
			t.Fatalf("value = %q, want empty", v)
		}
	})
}

func TestOpenBasedirAllows(t *testing.T) {
	const spool = "/run/jabali-wp-purge"
	cases := []struct {
		name string
		ob   string
		want bool
	}{
		{"live pool permits the spool", livePoolOpenBasedir[strings.Index(livePoolOpenBasedir, "=")+1:], true},
		{"pre-fix pool does not", stalePoolOpenBasedir[strings.Index(stalePoolOpenBasedir, "=")+1:], false},
		{"exact entry", "/run/jabali-wp-purge", true},
		{"parent directory covers it", "/run", true},
		{"trailing slash is the same boundary", "/run/jabali-wp-purge/", true},
		// The reason this is not a substring test: a sibling path that merely
		// starts with the spool's name grants nothing.
		{"sibling with a longer name is not the spool", "/run/jabali-wp-purge-old", false},
		{"unrelated entries", "/home/bob:/tmp", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := openBasedirAllows(tc.ob, spool); got != tc.want {
				t.Fatalf("openBasedirAllows(%q) = %v, want %v", tc.ob, got, tc.want)
			}
		})
	}
}

func TestUserPoolConfs(t *testing.T) {
	// Mirror the on-disk layout under a temp root and point the real function at
	// it, so this exercises the selection logic rather than a copy of it.
	root := t.TempDir()
	dir := filepath.Join(root, "etc", "php", "8.1", "fpm", "pool.d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	orig := poolConfDirGlob
	poolConfDirGlob = filepath.Join(root, "etc", "php", "*", "fpm", "pool.d")
	t.Cleanup(func() { poolConfDirGlob = orig })

	names := []string{
		"jabali-arizote.conf",        // default pool
		"jabali-arizote-php8.1.conf", // per-version pool (GH #329)
		"jabali-arizotebackup.conf",  // different user, shares a prefix
		"jabali-brownonline.conf",    // unrelated user
		"jabali-pma.conf",            // service pool
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("[pool]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	matched := map[string]bool{}
	for _, p := range userPoolConfs("arizote") {
		matched[filepath.Base(p)] = true
	}

	// The per-version pool is the whole point: globbing only the default form
	// checked one pool and silently passed the other.
	if !matched["jabali-arizote-php8.1.conf"] {
		t.Error("per-version pool not selected — a stale one would pass silently")
	}
	if !matched["jabali-arizote.conf"] {
		t.Error("default pool not selected")
	}
	// A username that is a prefix of another must not pull in the other's pool,
	// or one user's jail state gets reported against a different user.
	if matched["jabali-arizotebackup.conf"] {
		t.Error("matched a different user whose name shares the prefix")
	}
	if matched["jabali-brownonline.conf"] || matched["jabali-pma.conf"] {
		t.Error("matched an unrelated pool")
	}
}

func TestUserPoolConfsEmptyUser(t *testing.T) {
	// An empty username would glob jabali-*.conf and report every pool on the
	// box against one tenant.
	if got := userPoolConfs(""); got != nil {
		t.Fatalf("userPoolConfs(\"\") = %v, want nil", got)
	}
}
