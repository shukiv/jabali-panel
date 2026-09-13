package commands

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestRestoreVhostAfterFailedTest covers the writeVhost recovery path when
// `nginx -t` rejects a newly-rendered vhost. The bug it guards against: the
// pre-fix code deleted both the config file and its sites-enabled symlink on
// every failure, so a domain whose new (bad) render was rejected went dark and
// stayed dark on the next reboot/reload, recurring every ~60s reconcile tick.
// The fix restores the last-good config instead, tearing down only a brand-new
// vhost that has no prior config to fall back to.
//
// The package TestMain stubs execCommandContext to a no-op `exit 0`, so the
// helper's re-validation `nginx -t` passes by default; the retest-fails case
// overrides it with a stub that exits 1.
func TestRestoreVhostAfterFailedTest(t *testing.T) {
	const goodContent = "server { listen 80; # last-good\n}\n"
	const badContent = "server { listen 80; bogus_directive;\n}\n"

	// setup materializes the on-disk state at the moment writeVhost's failure
	// branch runs: configPath holds the just-written BAD content, and the
	// symlink exists iff linked. It returns the two paths.
	setup := func(t *testing.T, linked bool) (configPath, enabledPath string) {
		t.Helper()
		base := t.TempDir()
		avail := filepath.Join(base, "sites-available")
		enabled := filepath.Join(base, "sites-enabled")
		if err := os.MkdirAll(avail, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(enabled, 0755); err != nil {
			t.Fatal(err)
		}
		configPath = filepath.Join(avail, "example.com.conf")
		enabledPath = filepath.Join(enabled, "example.com.conf")
		// The failed pass has already overwritten configPath with BAD content.
		if err := os.WriteFile(configPath, []byte(badContent), 0644); err != nil {
			t.Fatal(err)
		}
		if linked {
			if err := os.Symlink(configPath, enabledPath); err != nil {
				t.Fatal(err)
			}
		}
		return configPath, enabledPath
	}

	readFile := func(t *testing.T, p string) string {
		t.Helper()
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		return string(b)
	}
	exists := func(p string) bool {
		_, err := os.Lstat(p)
		return err == nil
	}

	t.Run("prior good, linked, retest passes -> last-good restored", func(t *testing.T) {
		configPath, enabledPath := setup(t, true)

		kept := restoreVhostAfterFailedTest(context.Background(), configPath, enabledPath,
			[]byte(goodContent), true /*hadConfig*/, true /*linkedBefore*/)

		if !kept {
			t.Errorf("kept = false, want true (last-good should be restored)")
		}
		if got := readFile(t, configPath); got != goodContent {
			t.Errorf("config not restored to last-good:\n got %q\nwant %q", got, goodContent)
		}
		if !exists(enabledPath) {
			t.Errorf("sites-enabled symlink was removed; domain would go dark")
		}
	})

	t.Run("prior good but retest also fails -> both removed", func(t *testing.T) {
		configPath, enabledPath := setup(t, true)

		// Force the restored config's re-validation to fail (e.g. a referenced
		// cert file has since vanished). No safe state to hold -> remove both.
		prev := execCommandContext
		execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "false")
		}
		t.Cleanup(func() { execCommandContext = prev })

		kept := restoreVhostAfterFailedTest(context.Background(), configPath, enabledPath,
			[]byte(goodContent), true, true)

		if kept {
			t.Errorf("kept = true, want false (retest failed -> nothing safe to keep)")
		}
		if exists(configPath) {
			t.Errorf("config left in place after retest failed")
		}
		if exists(enabledPath) {
			t.Errorf("symlink left in place after retest failed")
		}
	})

	t.Run("no prior config -> brand-new vhost removed", func(t *testing.T) {
		configPath, enabledPath := setup(t, true)

		kept := restoreVhostAfterFailedTest(context.Background(), configPath, enabledPath,
			nil, false /*hadConfig*/, false /*linkedBefore*/)

		if kept {
			t.Errorf("kept = true, want false (no last-good to restore)")
		}
		if exists(configPath) {
			t.Errorf("brand-new rejected vhost left on disk")
		}
		if exists(enabledPath) {
			t.Errorf("brand-new rejected symlink left on disk")
		}
	})

	t.Run("prior good, not linked before -> config restored, link removed", func(t *testing.T) {
		configPath, enabledPath := setup(t, true) // symlink present (writeVhost created it this pass)

		kept := restoreVhostAfterFailedTest(context.Background(), configPath, enabledPath,
			[]byte(goodContent), true /*hadConfig*/, false /*linkedBefore*/)

		if !kept {
			t.Errorf("kept = false, want true (last-good config restored)")
		}
		if got := readFile(t, configPath); got != goodContent {
			t.Errorf("config not restored to last-good:\n got %q\nwant %q", got, goodContent)
		}
		if exists(enabledPath) {
			t.Errorf("symlink kept though it was not linked before this pass")
		}
	})
}
