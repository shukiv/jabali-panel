package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// php_pool_runtime_dir_test.go — removing a pool removes its runtime dir
// (/run/php/jabali-<slug>: socket, pid, phpver). fpm-pre-start makes it on each
// start; left behind, it lingers until reboot and, once the account's UID is
// reused, belongs to whoever gets that UID next.

func poolRunFixture(t *testing.T, slugs ...string) (runRoot, outside string) {
	t.Helper()
	tmp := t.TempDir()
	runRoot = filepath.Join(tmp, "run-php")
	t.Setenv("JABALI_PHP_RUN_ROOT", runRoot)
	t.Setenv("JABALI_FPM_CONFIG_ROOT", filepath.Join(tmp, "fpm"))
	t.Setenv("JABALI_PHP_VER_PIN_ROOT", filepath.Join(tmp, "user-phpver"))
	t.Setenv("JABALI_PHP_POOL_SKIP_RELOAD", "1")
	outside = filepath.Join(tmp, "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, s := range slugs {
		dir := filepath.Join(runRoot, "jabali-"+s)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "phpver"), []byte("8.3"), 0o440); err != nil {
			t.Fatal(err)
		}
		// A link the account could plant: removal must not follow it.
		if err := os.Symlink(outside, filepath.Join(dir, "fpm.sock")); err != nil {
			t.Fatal(err)
		}
	}
	return runRoot, outside
}

func TestPHPPoolRemove_RemovesItsRuntimeDir(t *testing.T) {
	runRoot, outside := poolRunFixture(t, "alice", "alice-php8.3")
	params, _ := json.Marshal(phpPoolRemoveParams{Username: "alice", Slug: "alice-php8.3"})
	if _, err := phpPoolRemoveHandler(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(runRoot, "jabali-alice-php8.3")); !os.IsNotExist(err) {
		t.Fatalf("the removed pool's runtime dir is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runRoot, "jabali-alice", "phpver")); err != nil {
		t.Fatalf("the default pool's runtime dir was touched: %v", err)
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != "keep" {
		t.Fatalf("a link inside the dir was followed: %q, %v", b, err)
	}
}

func TestReapFPMPoolArtifacts_RemovesTheRuntimeDir(t *testing.T) {
	runRoot, outside := poolRunFixture(t, "bob", "bobby")
	reapFPMPoolArtifacts(context.Background(), "bob")
	if _, err := os.Lstat(filepath.Join(runRoot, "jabali-bob")); !os.IsNotExist(err) {
		t.Fatalf("the reaped pool's runtime dir is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runRoot, "jabali-bobby", "phpver")); err != nil {
		t.Fatalf("another account's runtime dir was touched: %v", err)
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != "keep" {
		t.Fatalf("a link inside the dir was followed: %q, %v", b, err)
	}
}
