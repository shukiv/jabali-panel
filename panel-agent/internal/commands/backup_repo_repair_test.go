package commands

// JAB-405 Part 2b: automated move-aside repair for the concurrent-init key/config
// mismatch. When the listing PROVES the race (mismatch class, ≥2 key files, the
// key restic named present), move that ONE key OUT of keys/ into the repository
// root, re-probe, and proceed if the surviving key opens config. No revert; one
// move per call; only the backup run repairs. keyA/keyB/keyGhost come from
// backup_repo_keys_test.go (same package).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

func TestRemoveID(t *testing.T) {
	t.Parallel()
	got := removeID([]string{keyA, keyB, keyGhost}, keyB)
	if len(got) != 2 || got[0] != keyA || got[1] != keyGhost {
		t.Errorf("removeID dropped the wrong element: %v", got)
	}
	// Removing an absent id is a no-op (keeps every element).
	if got := removeID([]string{keyA}, keyB); len(got) != 1 || got[0] != keyA {
		t.Errorf("removeID of an absent id must be a no-op: %v", got)
	}
}

// TestMoveKeyAside_Local renames the mismatched key OUT of keys/ into the repo
// ROOT, keeps the surviving key, and never deletes. The destination landing in the
// root (not a keys/ sub-name) is load-bearing: restic enumerates key candidates
// only from keys/, so a root file is never re-tried as a key.
func TestMoveKeyAside_Local(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	keysDir := filepath.Join(repo, "keys")
	if err := os.MkdirAll(keysDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{keyA, keyB} {
		if err := os.WriteFile(filepath.Join(keysDir, name), []byte("k"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	dst, err := moveKeyAside(context.Background(), backup.KindLocal, repo, nil, nil, keyA)
	if err != nil {
		t.Fatalf("moveKeyAside: %v", err)
	}

	// The moved key is gone from keys/, the surviving key stays.
	if _, err := os.Stat(filepath.Join(keysDir, keyA)); !os.IsNotExist(err) {
		t.Errorf("moved key must be gone from keys/, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(keysDir, keyB)); err != nil {
		t.Errorf("surviving key must stay in keys/: %v", err)
	}
	// The moved-aside file lands in the repo ROOT, not keys/, and still exists
	// (rename, never delete).
	if filepath.Dir(dst) != repo {
		t.Errorf("moved-aside file must land in the repo root, got dir %q (repo %q)", filepath.Dir(dst), repo)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("moved-aside file must exist (rename, not delete): %v", err)
	}
	// Its name is NOT a valid 64-hex restic key id (so restic never re-tries it as
	// a key) and carries the .jabali-race- marker.
	base := filepath.Base(dst)
	if resticKeyIDRE.MatchString(base) {
		t.Errorf("moved-aside name must not be a 64-hex key id: %q", base)
	}
	if !strings.Contains(base, ".jabali-race-") {
		t.Errorf("moved-aside name must carry the .jabali-race- marker: %q", base)
	}
}

func TestMoveKeyAside_UnsupportedKind(t *testing.T) {
	t.Parallel()
	_, err := moveKeyAside(context.Background(), backup.KindS3, "s3:s3.amazonaws.com/bucket", nil, nil, keyA)
	if err == nil || !strings.Contains(err.Error(), "backend") {
		t.Fatalf("expected an unsupported-backend error, got: %v", err)
	}
}

// TestMoveKeyAside_RefusesNonKeyID is the hard clamp independent of the caller's
// gate: a named that is not a bare 64-hex key id must be refused BEFORE any path is
// built and BEFORE any rename runs. An empty named would otherwise collapse src to
// keys/ itself (renaming the whole key directory); a traversing named would escape
// the repository. Nothing on disk may change.
func TestMoveKeyAside_RefusesNonKeyID(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"", "../config", "not-a-key", keyA + "/../config", keyA + "x"} {
		repo := t.TempDir()
		keysDir := filepath.Join(repo, "keys")
		if err := os.MkdirAll(keysDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(keysDir, keyA), []byte("k"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := moveKeyAside(context.Background(), backup.KindLocal, repo, nil, nil, bad)
		if err == nil || !strings.Contains(err.Error(), "not a restic key id") {
			t.Errorf("moveKeyAside(named=%q) must refuse with a key-id error, got: %v", bad, err)
		}
		// keys/ is untouched, and no stray move-aside file appeared in the root.
		if _, statErr := os.Stat(filepath.Join(keysDir, keyA)); statErr != nil {
			t.Errorf("named=%q: keys/ was disturbed by a refused move: %v", bad, statErr)
		}
		entries, _ := os.ReadDir(repo)
		for _, e := range entries {
			if strings.Contains(e.Name(), ".jabali-race-") {
				t.Errorf("named=%q: a refused move still created a root file %q", bad, e.Name())
			}
		}
	}
}

// TestRepairGateOpen falsifies each clause of the Part 2b safety invariant: the
// repair may run ONLY for the mismatch class, with a listing present, holding ≥2
// key files, whose named key is among them. Deleting any one clause of
// repairGateOpen turns one of these false cases true.
func TestRepairGateOpen(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cls  repoProbeClass
		keys *repoKeyListing
		want bool
	}{
		{"race proven: mismatch + 2 keys + named present", repoProbeKeyConfigMismatch, &repoKeyListing{ids: []string{keyA, keyB}, named: keyA}, true},
		{"wrong class: unopenable never repairs", repoProbeUnopenable, &repoKeyListing{ids: []string{keyA, keyB}, named: keyA}, false},
		{"no listing: jailed/unlistable target", repoProbeKeyConfigMismatch, nil, false},
		{"one key: corrupt config, nothing to move", repoProbeKeyConfigMismatch, &repoKeyListing{ids: []string{keyA}, named: keyA}, false},
		{"named absent: listing does not match the error", repoProbeKeyConfigMismatch, &repoKeyListing{ids: []string{keyA, keyB}, named: keyGhost}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := repairGateOpen(c.cls, c.keys); got != c.want {
				t.Errorf("repairGateOpen = %v, want %v", got, c.want)
			}
		})
	}
}

// TestRepairKeyMismatch exercises the decision table with injected move/reprobe
// fakes — no real backend. Each outcome is falsifiable independently.
func TestRepairKeyMismatch(t *testing.T) {
	t.Parallel()

	const (
		url  = "sftp:u@h:/p"
		pw   = "/etc/jabali-panel/dest-1.password"
		orig = "config or key " + keyA + " is damaged: ciphertext verification failed"
	)
	mismatchOn := func(id string) string {
		return "config or key " + id + " is damaged: ciphertext verification failed"
	}

	t.Run("move fixes it: repo opens -> nil, exactly one move, re-probed", func(t *testing.T) {
		var moved []string
		reprobed := false
		move := func(_ context.Context, named string) (string, error) {
			moved = append(moved, named)
			return "/p/" + named + ".jabali-race-1", nil
		}
		reprobe := func(_ context.Context) (string, error) {
			reprobed = true
			return "", nil // opens
		}
		err := repairKeyMismatch(context.Background(),
			&repoKeyListing{ids: []string{keyA, keyB}, named: keyA},
			url, pw, orig, move, reprobe)
		if err != nil {
			t.Fatalf("expected nil (repo opened after the move), got: %v", err)
		}
		if len(moved) != 1 || moved[0] != keyA {
			t.Errorf("expected exactly one move of keyA, got: %v", moved)
		}
		if !reprobed {
			t.Error("expected a re-probe after the move")
		}
	})

	t.Run("three keys: still mismatches on a different key -> sharpened race message, one move", func(t *testing.T) {
		moves := 0
		move := func(_ context.Context, named string) (string, error) {
			moves++
			if named != keyA {
				t.Errorf("must move the key restic named (keyA), got %s", named)
			}
			return "/p/" + named + ".jabali-race-1", nil
		}
		reprobe := func(_ context.Context) (string, error) {
			return mismatchOn(keyB), errors.New("exit status 1")
		}
		// Three key files: after moving keyA, two remain (keyB, keyGhost) and the
		// re-probe now names keyB — the sharpened race message must target keyB.
		err := repairKeyMismatch(context.Background(),
			&repoKeyListing{ids: []string{keyA, keyB, keyGhost}, named: keyA},
			url, pw, orig, move, reprobe)
		if err == nil {
			t.Fatal("expected a run failure (repo still not open this run)")
		}
		msg := err.Error()
		if !strings.Contains(msg, "holds 2 key files") || !strings.Contains(msg, "move keys/"+keyB) {
			t.Errorf("expected sharpened race message naming keyB from the 2 remaining keys\ngot: %s", msg)
		}
		if moves != 1 {
			t.Errorf("exactly one move per call, got %d", moves)
		}
	})

	t.Run("two keys, config matches neither: one key left, still mismatched -> corrupt-config guidance", func(t *testing.T) {
		move := func(_ context.Context, named string) (string, error) {
			return "/p/" + named + ".jabali-race-1", nil
		}
		reprobe := func(_ context.Context) (string, error) {
			return mismatchOn(keyB), errors.New("exit status 1")
		}
		// Only two key files: after moving keyA, one remains (keyB) and it still
		// mismatches -> the config itself is corrupt, so the message must say ONE key
		// left, fresh dir, and NOT tell the operator to move anything.
		err := repairKeyMismatch(context.Background(),
			&repoKeyListing{ids: []string{keyA, keyB}, named: keyA},
			url, pw, orig, move, reprobe)
		if err == nil {
			t.Fatal("expected a run failure")
		}
		msg := err.Error()
		if !strings.Contains(msg, "exactly ONE") || !strings.Contains(msg, "FRESH empty") {
			t.Errorf("expected corrupt-config guidance (one key left, still mismatched)\ngot: %s", msg)
		}
	})

	t.Run("move itself fails: repo untouched -> manual 2a guidance, NO re-probe", func(t *testing.T) {
		reprobed := false
		move := func(_ context.Context, named string) (string, error) {
			return "", errors.New("permission denied")
		}
		reprobe := func(_ context.Context) (string, error) {
			reprobed = true
			return "", nil
		}
		err := repairKeyMismatch(context.Background(),
			&repoKeyListing{ids: []string{keyA, keyB}, named: keyA},
			url, pw, orig, move, reprobe)
		if err == nil {
			t.Fatal("expected an error when the move fails")
		}
		if reprobed {
			t.Error("must NOT re-probe when the move failed (repository untouched)")
		}
		msg := err.Error()
		if !strings.Contains(msg, "could not move keys/"+keyA) || !strings.Contains(msg, "permission denied") {
			t.Errorf("expected the move-failure reason\ngot: %s", msg)
		}
		// Falls back to the exact 2a manual race guidance (move keyA by hand).
		if !strings.Contains(msg, "move keys/"+keyA) {
			t.Errorf("expected manual 2a race guidance after a move failure\ngot: %s", msg)
		}
	})

	t.Run("still mismatched on the SAME key -> fail loud with the moved-aside path", func(t *testing.T) {
		move := func(_ context.Context, named string) (string, error) {
			return "/p/" + named + ".jabali-race-1", nil
		}
		reprobe := func(_ context.Context) (string, error) {
			return mismatchOn(keyA), errors.New("exit status 1") // same key still named
		}
		err := repairKeyMismatch(context.Background(),
			&repoKeyListing{ids: []string{keyA, keyB}, named: keyA},
			url, pw, orig, move, reprobe)
		if err == nil {
			t.Fatal("expected fail-loud when the same key is still named after the move")
		}
		msg := err.Error()
		if !strings.Contains(msg, "still does not open") || !strings.Contains(msg, ".jabali-race-1") {
			t.Errorf("expected fail-loud naming where the key went\ngot: %s", msg)
		}
	})

	t.Run("non-mismatch failure after move -> fail loud, treat as corrupt", func(t *testing.T) {
		move := func(_ context.Context, named string) (string, error) {
			return "/p/" + named + ".jabali-race-1", nil
		}
		reprobe := func(_ context.Context) (string, error) {
			return "wrong password or no key found", errors.New("exit status 1")
		}
		err := repairKeyMismatch(context.Background(),
			&repoKeyListing{ids: []string{keyA, keyB}, named: keyA},
			url, pw, orig, move, reprobe)
		if err == nil {
			t.Fatal("expected fail-loud on a non-mismatch failure after the move")
		}
		if !strings.Contains(err.Error(), "still does not open") {
			t.Errorf("expected fail-loud\ngot: %s", err.Error())
		}
	})
}
