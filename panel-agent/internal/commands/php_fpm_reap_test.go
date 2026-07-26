package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestUsernameForSlug(t *testing.T) {
	cases := map[string]string{
		"alice":         "alice",
		"alice-php8.2":  "alice",
		"bob_1-php8.15": "bob_1",
		"pma":           "pma",
	}
	for slug, want := range cases {
		if got := usernameForSlug(slug); got != want {
			t.Errorf("usernameForSlug(%q) = %q, want %q", slug, got, want)
		}
	}
}

func TestPhpPoolReapOrphans_ReapsOnlyOrphans(t *testing.T) {
	dir := t.TempDir()
	fpmDir := t.TempDir()
	sysDir := t.TempDir()
	t.Setenv("JABALI_PHP_POOL_SKIP_RELOAD", "1")
	t.Setenv("JABALI_FPM_CONFIG_ROOT", fpmDir)
	t.Setenv("JABALI_SYSTEMD_ROOT", sysDir)

	oldDir := userPhpverDir
	userPhpverDir = dir
	defer func() { userPhpverDir = oldDir }()

	// Only "dave" has a surviving OS account (tests the OS-user safety belt).
	oldLookup := osUserExists
	osUserExists = func(u string) bool { return u == "dave" }
	defer func() { osUserExists = oldLookup }()

	for _, slug := range []string{"alice", "bob", "carol-php8.2", "dave", "pma"} {
		if err := os.WriteFile(filepath.Join(dir, slug), []byte("8.4"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fpmDir, slug+".conf"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	params, _ := json.Marshal(map[string]any{"keep_usernames": []string{"alice"}})
	out, err := phpPoolReapOrphansHandler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	resp := out.(phpPoolReapOrphansResponse)
	got := append([]string(nil), resp.Reaped...)
	sort.Strings(got)
	want := []string{"bob", "carol-php8.2"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("reaped = %v, want %v", got, want)
	}

	// alice (keep-list), dave (OS guard), pma (system-reserved) must survive.
	for _, keep := range []string{"alice", "dave", "pma"} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Errorf("expected pin file %q to survive: %v", keep, err)
		}
	}
	// Reaped slugs' pin files + confs are gone.
	for _, gone := range []string{"bob", "carol-php8.2"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Errorf("expected pin %q removed, err=%v", gone, err)
		}
		if _, err := os.Stat(filepath.Join(fpmDir, gone+".conf")); !os.IsNotExist(err) {
			t.Errorf("expected conf %q removed, err=%v", gone, err)
		}
	}
}

func TestPhpPoolReapOrphans_NilKeepRefused(t *testing.T) {
	if out, err := phpPoolReapOrphansHandler(context.Background(), []byte(`{}`)); err == nil {
		t.Fatalf("expected error for missing keep_usernames, got %v", out)
	}
}

func TestReapUserFPMPools_RemovesDefaultAndVersionedOnly(t *testing.T) {
	dir := t.TempDir()
	fpmDir := t.TempDir()
	sysDir := t.TempDir()
	t.Setenv("JABALI_PHP_POOL_SKIP_RELOAD", "1")
	t.Setenv("JABALI_FPM_CONFIG_ROOT", fpmDir)
	t.Setenv("JABALI_SYSTEMD_ROOT", sysDir)
	oldDir := userPhpverDir
	userPhpverDir = dir
	defer func() { userPhpverDir = oldDir }()

	for _, slug := range []string{"alice", "alice-php8.2", "bob"} {
		os.WriteFile(filepath.Join(dir, slug), []byte("8.4"), 0o644)
		os.WriteFile(filepath.Join(fpmDir, slug+".conf"), []byte("x"), 0o644)
	}

	reapUserFPMPools(context.Background(), "alice")

	for _, gone := range []string{"alice", "alice-php8.2"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Errorf("expected %q pin removed, err=%v", gone, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "bob")); err != nil {
		t.Errorf("expected bob pin (other user) to survive: %v", err)
	}
}
