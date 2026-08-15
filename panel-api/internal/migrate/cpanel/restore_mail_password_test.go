package cpanel

import (
	"os"
	"path/filepath"
	"testing"
)

// normalizeSourceMailHash strips a {SCHEME} tag and keeps any crypt string
// Stalwart verifies (bcrypt, SHA-512/256/MD5-crypt, Argon2, PBKDF2, scrypt).
// Only unknown/plaintext hashes are dropped (JAB-149).
func TestNormalizeSourceMailHash(t *testing.T) {
	cases := map[string]string{
		"{BLF-CRYPT}$2y$05$abcdefghijklmnopqrstuv": "$2y$05$abcdefghijklmnopqrstuv",
		"$2a$10$abcdefghijklmnopqrstuv":            "$2a$10$abcdefghijklmnopqrstuv",
		"$2b$12$abcdefghijklmnopqrstuv":            "$2b$12$abcdefghijklmnopqrstuv",
		// JAB-149: Stalwart verifies these natively — preserve, don't drop.
		"{SHA512-CRYPT}$6$rounds=5000$salt$hash":       "$6$rounds=5000$salt$hash",
		"{SHA256-CRYPT}$5$rounds=5000$salt$hash":       "$5$rounds=5000$salt$hash",
		"$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA": "$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA",
		"{MD5-CRYPT}$1$salt$hash":                      "$1$salt$hash",
		// No Stalwart-recognised prefix → still dropped.
		"{PLAIN}hunter2": "",
		"plaintextnope":  "",
		"":               "",
	}
	for in, want := range cases {
		if got := normalizeSourceMailHash(in); got != want {
			t.Errorf("normalizeSourceMailHash(%q) = %q, want %q", in, got, want)
		}
	}
}

// loadSourceMailPasswords parses a Hestia dovecot passwd-file into local→hash,
// keeping every Stalwart-verifiable scheme (JAB-149); missing file → empty.
func TestLoadSourceMailPasswords(t *testing.T) {
	dir := t.TempDir()
	confDir := filepath.Join(dir, "conf")
	if err := os.MkdirAll(confDir, 0o750); err != nil {
		t.Fatal(err)
	}
	content := "" +
		"info:{BLF-CRYPT}$2y$05$JH3fabcdefghijklmnopqr:1001:1001::/home\n" +
		"legacy:{SHA512-CRYPT}$6$rounds=5000$s$h:1002:1002::/home\n" +
		"\n" +
		"# comment line\n" +
		"admin:$2b$10$anotheranotheranotherx:1003:1003\n"
	if err := os.WriteFile(filepath.Join(confDir, "passwd"), []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}

	got := loadSourceMailPasswords(dir)
	if got["info"] != "$2y$05$JH3fabcdefghijklmnopqr" {
		t.Errorf("info hash = %q", got["info"])
	}
	if got["admin"] != "$2b$10$anotheranotheranotherx" {
		t.Errorf("admin hash = %q", got["admin"])
	}
	if got["legacy"] != "$6$rounds=5000$s$h" {
		t.Errorf("legacy (SHA512-CRYPT) must be preserved (JAB-149), got %q", got["legacy"])
	}
	if len(got) != 3 {
		t.Errorf("want 3 usable hashes (JAB-149 keeps SHA512), got %d: %v", len(got), got)
	}

	// Missing file → empty map, no error.
	if m := loadSourceMailPasswords(t.TempDir()); len(m) != 0 {
		t.Errorf("missing passwd → want empty, got %v", m)
	}
}
