package commands

// JAB-405 Part 2a: count a corrupt repo's key files below restic (which can't
// open it to list them) so the key/config-mismatch message names the exact key
// to move — the concurrent-init race (≥2 keys incl. the named one) vs a corrupt
// config (exactly 1) vs a listing that does not match the error (move nothing).
// Read-only, fail-soft: a listing failure never masks the mismatch itself.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// Real 64-hex restic key ids captured from a reproduced dual-key/one-config repo.
const (
	keyA = "3c0bdcd033d53757bd511b5755967c0ab681e9d103ecc52aab4ba096d89f6f76"
	keyB = "d14707bdd8fc5db506b23abc1253eeefeca7165070341525856db3a9cef2107e"
	// A well-formed key id that is NOT in a two-key listing — the error names a
	// key the listing does not contain.
	keyGhost = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
)

func TestParseKeyIDs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want int
	}{
		{"two keys", keyA + "\n" + keyB + "\n", 2},
		{"stray non-key file and blank line ignored", keyA + "\n" + keyB + "\n.jabali-quarantine-123\n\n", 2},
		{"restic layout names are not keys", "config\ndata\nindex\nkeys\nsnapshots\n", 0},
		{"empty output", "", 0},
		{"trailing spaces trimmed", "  " + keyA + "  \n", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := len(parseKeyIDs([]byte(c.raw))); got != c.want {
				t.Errorf("parseKeyIDs(%q) counted %d, want %d", c.raw, got, c.want)
			}
		})
	}
}

func TestMismatchKeyID(t *testing.T) {
	t.Parallel()
	// Verbatim restic 0.18.0 stderr, lowercased (classifyRepoProbe lowercases
	// before this runs). The id is the full 64-hex key-file name.
	lower := "fatal: config or key " + keyA + " is damaged: ciphertext verification failed"
	if got := mismatchKeyID(lower); got != keyA {
		t.Errorf("mismatchKeyID = %q, want %q", got, keyA)
	}
	// No ciphertext phrase → no id.
	if got := mismatchKeyID("fatal: wrong password or no key found"); got != "" {
		t.Errorf("mismatchKeyID on wrong-password stderr = %q, want empty", got)
	}
}

func TestListRepoKeys_Local(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	keysDir := filepath.Join(repo, "keys")
	if err := os.MkdirAll(keysDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{keyA, keyB, "not-a-key"} {
		if err := os.WriteFile(filepath.Join(keysDir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := listRepoKeys(context.Background(), backup.KindLocal, repo, nil, nil)
	if err != nil {
		t.Fatalf("listRepoKeys: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("counted %d key files, want 2 (the stray 'not-a-key' must be filtered): %v", len(ids), ids)
	}
}

func TestListRepoKeys_UnsupportedKind(t *testing.T) {
	t.Parallel()
	_, err := listRepoKeys(context.Background(), backup.KindS3, "s3:s3.amazonaws.com/bucket", nil, nil)
	if err == nil {
		t.Fatal("expected an error for an unsupported backend, got nil")
	}
	if !strings.Contains(err.Error(), "backend") {
		t.Errorf("error should name the unsupported backend: %v", err)
	}
}

// The known-count arm turns "count the files yourself" into a concrete
// instruction. Each branch must (a) keep the password-is-CORRECT preamble,
// (b) give the right recovery, and (c) not carry the other branch's advice, and
// stay a single paragraph (renders in a UI toast).
func TestRepoUnopenableMessage_MismatchArm_KnownCount(t *testing.T) {
	t.Parallel()
	const (
		url = "sftp:puzzle@host:/home/puzzle/jabali"
		pw  = "/etc/jabali-panel/dest-7.password"
		lo  = "config or key " + keyA + " is damaged: ciphertext verification failed"
	)

	t.Run("race: two keys incl. the named one -> move that key", func(t *testing.T) {
		msg := repoUnopenableMessage(repoProbeKeyConfigMismatch, url, pw, lo,
			&repoKeyListing{ids: []string{keyA, keyB}, named: keyA}, nil)
		for _, want := range []string{"CORRECT", "do NOT restore", "holds 2 key files", "move keys/" + keyA} {
			if !strings.Contains(msg, want) {
				t.Errorf("race message missing %q\ngot: %s", want, msg)
			}
		}
		// The concrete count REPLACES the count-it-yourself hedge.
		for _, banned := range []string{"MORE THAN ONE", "exactly ONE"} {
			if strings.Contains(msg, banned) {
				t.Errorf("race message still carries the unknown-count hedge %q\ngot: %s", banned, msg)
			}
		}
		if strings.Contains(msg, "\n") {
			t.Errorf("message must be one line\ngot: %s", msg)
		}
	})

	t.Run("corrupt config: exactly one key -> fresh dir, move nothing", func(t *testing.T) {
		msg := repoUnopenableMessage(repoProbeKeyConfigMismatch, url, pw, lo,
			&repoKeyListing{ids: []string{keyA}, named: keyA}, nil)
		for _, want := range []string{"exactly ONE", "FRESH empty"} {
			if !strings.Contains(msg, want) {
				t.Errorf("corrupt-config message missing %q\ngot: %s", want, msg)
			}
		}
		if strings.Contains(msg, "move keys/") {
			t.Errorf("corrupt-config message must not tell the operator to move a key\ngot: %s", msg)
		}
	})

	t.Run("listing does not match: named key absent -> move nothing", func(t *testing.T) {
		msg := repoUnopenableMessage(repoProbeKeyConfigMismatch, url, pw, lo,
			&repoKeyListing{ids: []string{keyA, keyB}, named: keyGhost}, nil)
		for _, want := range []string{"not among them", "do NOT move", "FRESH empty"} {
			if !strings.Contains(msg, want) {
				t.Errorf("listing-mismatch message missing %q\ngot: %s", want, msg)
			}
		}
		// Must NOT name a key to move — the named key is not in the listing.
		if strings.Contains(msg, "move keys/") {
			t.Errorf("listing-mismatch message must not name a key to move\ngot: %s", msg)
		}
	})
}

// Fail-soft: if the listing itself fails, the operator still gets the mismatch
// message (unknown-count guidance) plus a flattened note of why the count is
// missing — a listing failure must never mask the underlying mismatch, and the
// multi-line ssh error must not break the single-paragraph toast.
func TestRepoUnopenableMessage_FailSoftSuffix(t *testing.T) {
	t.Parallel()
	listErr := errors.New("ssh ls -1 /p/keys: exit status 255 (output: Permission denied\nConnection closed by remote host)")
	msg := repoUnopenableMessage(repoProbeKeyConfigMismatch,
		"sftp:u@h:/p", "/etc/jabali-panel/restic-repo.password",
		"config or key "+keyA+" is damaged: ciphertext verification failed", nil, listErr)

	for _, want := range []string{"MORE THAN ONE", "Could not list keys/", "Permission denied"} {
		if !strings.Contains(msg, want) {
			t.Errorf("fail-soft message missing %q\ngot: %s", want, msg)
		}
	}
	if strings.Contains(msg, "\n") {
		t.Errorf("fail-soft message must stay one paragraph — the multi-line ssh error was not flattened\ngot: %s", msg)
	}
}

// The wrong-password (unopenable) arm must ignore a key listing entirely: an
// operator on a foreign/rotated password must never be told about key counts.
// Guards that a listing result cannot bleed across classes.
func TestRepoUnopenableMessage_UnopenableIgnoresKeys(t *testing.T) {
	t.Parallel()
	msg := repoUnopenableMessage(repoProbeUnopenable,
		"/backups", "/etc/jabali-panel/restic-repo.password", "wrong password or no key found",
		&repoKeyListing{ids: []string{keyA, keyB}, named: keyA}, nil)

	if !strings.Contains(msg, "reinstalled or regenerated") {
		t.Errorf("unopenable arm lost its wrong-password advice\ngot: %s", msg)
	}
	for _, banned := range []string{"holds 2", "move keys/", "concurrent-init"} {
		if strings.Contains(msg, banned) {
			t.Errorf("unopenable arm leaked key-count advice %q — a listing bled across classes\ngot: %s", banned, msg)
		}
	}
}
