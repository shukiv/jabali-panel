package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The agent installs jabali-cache on WordPress sites from the bundle, and the
// refresh sweep at the end of `jabali update` re-copies it to every
// cache-enabled site. A --from-source update skips the release-tarball step
// that refreshes the bundle, so it must sync the bundle from the checkout
// itself, before the sweep; otherwise the sweep puts the old plugin back.
func TestUpdate_SyncsBundledCachePluginBeforeRefresh(t *testing.T) {
	src := stripLineComments(readGoSource(t, "update.go"))
	sync := strings.Index(src, `{"sync bundled jabali-cache plugin", func() error {`)
	refresh := strings.Index(src, `{"refresh jabali-cache plugin on cache-enabled sites (GH #613)", func() error {`)
	tarball := strings.Index(src, `{"install from release tarball (or fall back to source)", func() error {`)
	if sync < 0 || refresh < 0 || tarball < 0 {
		t.Fatalf("steps not found: sync at %d, refresh at %d, tarball at %d", sync, refresh, tarball)
	}
	if sync > refresh {
		t.Fatal("the bundle must be synced before the refresh sweep copies it to sites")
	}
	step := src[sync:]
	if end := strings.Index(step[1:], `{"`); end > 0 {
		step = step[:end+1]
	}
	if strings.Contains(step, "fromSource") {
		t.Fatal("the sync must run on every update, like install.sh, not only --from-source")
	}
	want := `syncBundledCachePlugin(repoDir+"/wp-plugins/jabali-cache", bundledCachePluginDir, "root:root")`
	if !strings.Contains(step, want) {
		t.Fatalf("the step must call %s", want)
	}
}

// The panel and the agent must agree on where the bundle lives.
func TestBundledCachePluginDir_MatchesAgent(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "panel-agent", "internal", "commands", "wordpress_cache.go"))
	if err != nil {
		t.Fatalf("read agent source: %v", err)
	}
	want := `const bundledWPCachePluginDir = "` + bundledCachePluginDir + `"`
	if !strings.Contains(string(raw), want) {
		t.Fatalf("agent bundle path differs; want %s", want)
	}
}

func TestSyncBundledCachePlugin(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not installed")
	}
	owner := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	write := func(p, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(p string) string {
		t.Helper()
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		return string(b)
	}

	root := t.TempDir()
	src := filepath.Join(root, "repo", "wp-plugins", "jabali-cache")
	dst := filepath.Join(root, "share", "wp-plugins", "jabali-cache")
	write(filepath.Join(src, "jabali-cache.php"), "Version: 1.2.0")
	write(filepath.Join(src, "includes", "lib.php"), "new lib")
	write(filepath.Join(src, ".git", "HEAD"), "ref")
	write(filepath.Join(dst, "jabali-cache.php"), "Version: 1.1.0")
	write(filepath.Join(dst, "includes", "removed.php"), "gone in the new version")

	if err := syncBundledCachePlugin(src, dst, owner); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if got := read(filepath.Join(dst, "jabali-cache.php")); got != "Version: 1.2.0" {
		t.Errorf("bundle not updated: %q", got)
	}
	if got := read(filepath.Join(dst, "includes", "lib.php")); got != "new lib" {
		t.Errorf("new file not copied: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dst, "includes", "removed.php")); !os.IsNotExist(err) {
		t.Errorf("a file the new version dropped must be removed from the bundle (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); !os.IsNotExist(err) {
		t.Errorf(".git must not be copied (err=%v)", err)
	}

	// A checkout without the plugin leaves the bundle alone.
	if err := syncBundledCachePlugin(filepath.Join(root, "missing"), dst, owner); err != nil {
		t.Fatalf("missing source: %v", err)
	}
	if got := read(filepath.Join(dst, "jabali-cache.php")); got != "Version: 1.2.0" {
		t.Errorf("bundle changed for a missing source: %q", got)
	}

	// A failing chown fails the step: the bundle must not be left owned by
	// the checkout's owner.
	if err := syncBundledCachePlugin(src, dst, "no-such-user-jc:no-such-group-jc"); err == nil {
		t.Error("a failing chown must fail the sync")
	}
}
