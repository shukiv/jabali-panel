package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GH #530: canonicalizeRootPrefix must rewrite ONLY the compat-symlink root
// prefix and leave the tenant-extractable remainder byte-for-byte — so
// filesafe's openat2 RESOLVE_BENEATH still governs (and rejects) any symlink
// planted deeper in the staged tree. Resolving the WHOLE path (the original,
// insecure fix) would follow such a symlink and defeat that protection.
func TestCanonicalizeRootPrefix(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")     // the true staging dir
	legacyRoot := filepath.Join(base, "compat") // the compat symlink -> real
	if err := os.Mkdir(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realRoot, legacyRoot); err != nil {
		t.Fatal(err)
	}
	// A symlink planted in the REMAINDER, pointing out of scope.
	if err := os.Symlink("/etc/shadow", filepath.Join(realRoot, "evil")); err != nil {
		t.Fatal(err)
	}

	// Root symlink prefix is rewritten to the real dir; remainder preserved.
	got := canonicalizeRootPrefix(legacyRoot+"/job1/dump.sql", legacyRoot)
	if want := realRoot + "/job1/dump.sql"; got != want {
		t.Errorf("prefix rewrite: got %q, want %q", got, want)
	}

	// The bare root resolves to the real dir.
	if got := canonicalizeRootPrefix(legacyRoot, legacyRoot); got != realRoot {
		t.Errorf("bare root: got %q, want %q", got, realRoot)
	}

	// SECURITY: a symlink in the remainder is NOT resolved — the returned path
	// still contains the literal "evil" component (RESOLVE_BENEATH will reject
	// it at open time), it must NOT become "/etc/shadow".
	got = canonicalizeRootPrefix(legacyRoot+"/evil", legacyRoot)
	if want := realRoot + "/evil"; got != want {
		t.Errorf("remainder symlink must stay verbatim: got %q, want %q", got, want)
	}
	if strings.Contains(got, "shadow") {
		t.Fatalf("remainder symlink was resolved — protection bypass: %q", got)
	}

	// A path not under the legacy root is untouched.
	if got := canonicalizeRootPrefix("/var/lib/jabali/migrations/x", legacyRoot); got != "/var/lib/jabali/migrations/x" {
		t.Errorf("non-legacy path changed: %q", got)
	}
	// A non-symlink legacyRoot is a no-op (returns the path unchanged).
	plain := filepath.Join(base, "plain")
	_ = os.Mkdir(plain, 0o755)
	if got := canonicalizeRootPrefix(plain+"/a", plain); got != plain+"/a" {
		t.Errorf("non-symlink root should be no-op: %q", got)
	}
}
