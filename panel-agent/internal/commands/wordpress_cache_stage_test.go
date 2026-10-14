package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// wordpress_cache_stage_test.go — staging the bundled jabali-cache plugin stays
// inside the account's home: a wp-content, plugins or jabali-cache symlink that
// leads elsewhere is refused or replaced, never followed.

// stageEnv points the account homes at a temp dir and the plugin owner at the
// test's own uid/gid. It returns the root that holds the homes and a WordPress
// site inside bob's home with an old copy of the plugin.
func stageEnv(t *testing.T) (homes, site string) {
	t.Helper()
	homes = t.TempDir()
	prevHome, prevOwner := tenantHomeDir, cachePluginOwnerIDs
	tenantHomeDir = func(osUser string) string { return filepath.Join(homes, osUser) }
	cachePluginOwnerIDs = func(string) (int, int, error) { return os.Getuid(), os.Getgid(), nil }
	t.Cleanup(func() { tenantHomeDir, cachePluginOwnerIDs = prevHome, prevOwner })

	fakeCacheBundle(t, bundleHeader121)
	if err := os.MkdirAll(filepath.Join(bundledCachePluginSrc, "includes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundledCachePluginSrc, "includes", "object-cache.php"), []byte("<?php\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	site = filepath.Join(homes, "bob", "domains", "example.com", "public_html")
	writeFile(t, filepath.Join(site, "wp-content", "plugins", "jabali-cache", "old.php"))
	return homes, site
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("<?php // keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// otherTree is a directory outside bob's home holding another copy of the plugin.
func otherTree(t *testing.T) (plugins string) {
	t.Helper()
	plugins = filepath.Join(t.TempDir(), "alice-plugins")
	writeFile(t, filepath.Join(plugins, "jabali-cache", "keep.php"))
	return plugins
}

func assertOtherTreeUntouched(t *testing.T, otherPlugin string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(otherPlugin, "keep.php")); err != nil {
		t.Errorf("the other tree's plugin file is gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(otherPlugin, "jabali-cache.php")); err == nil {
		t.Error("the bundle was copied into the other tree")
	}
}

func TestStageBundledCachePlugin_ReplacesThePluginWithTheBundle(t *testing.T) {
	_, site := stageEnv(t)

	if err := stageBundledCachePlugin(context.Background(), site, "bob"); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(site, "wp-content", "plugins", "jabali-cache")
	if _, err := os.Stat(filepath.Join(dest, "old.php")); !os.IsNotExist(err) {
		t.Errorf("old plugin file still there (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "jabali-cache.php")); err != nil {
		t.Errorf("bundle main file not staged: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dest, "includes", "object-cache.php"))
	if err != nil {
		t.Fatalf("bundle subdirectory not staged: %v", err)
	}
	if fi.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want the bundle's 0640", fi.Mode().Perm())
	}
}

func TestStageBundledCachePlugin_RefusesAPluginsDirLinkedOutOfTheHome(t *testing.T) {
	_, site := stageEnv(t)
	other := otherTree(t)
	plugins := filepath.Join(site, "wp-content", "plugins")
	if err := os.RemoveAll(plugins); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, plugins); err != nil {
		t.Fatal(err)
	}

	err := stageBundledCachePlugin(context.Background(), site, "bob")
	if err == nil || !strings.Contains(err.Error(), "has a symlink or a non-directory") {
		t.Errorf("err = %v, want a symlink refusal", err)
	}
	assertOtherTreeUntouched(t, filepath.Join(other, "jabali-cache"))
}

func TestStageBundledCachePlugin_RefusesAWpContentLinkedOutOfTheHome(t *testing.T) {
	_, site := stageEnv(t)
	other := otherTree(t)
	wpContent := filepath.Join(site, "wp-content")
	if err := os.RemoveAll(wpContent); err != nil {
		t.Fatal(err)
	}
	// wp-content -> <other parent>, so wp-content/plugins is the other's tree.
	if err := os.Rename(other, filepath.Join(filepath.Dir(other), "plugins")); err != nil {
		t.Fatal(err)
	}
	other = filepath.Join(filepath.Dir(other), "plugins")
	if err := os.Symlink(filepath.Dir(other), wpContent); err != nil {
		t.Fatal(err)
	}

	if err := stageBundledCachePlugin(context.Background(), site, "bob"); err == nil {
		t.Error("staging through a wp-content symlink that leaves the home succeeded")
	}
	assertOtherTreeUntouched(t, filepath.Join(other, "jabali-cache"))
}

func TestStageBundledCachePlugin_ReplacesALinkedPluginDirWithoutFollowingIt(t *testing.T) {
	_, site := stageEnv(t)
	other := otherTree(t)
	dest := filepath.Join(site, "wp-content", "plugins", "jabali-cache")
	if err := os.RemoveAll(dest); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(other, "jabali-cache"), dest); err != nil {
		t.Fatal(err)
	}

	if err := stageBundledCachePlugin(context.Background(), site, "bob"); err != nil {
		t.Fatal(err)
	}
	assertOtherTreeUntouched(t, filepath.Join(other, "jabali-cache"))
	fi, err := os.Lstat(dest)
	if err != nil || !fi.IsDir() {
		t.Fatalf("dest is not a real directory now (mode %v, err %v)", fi.Mode(), err)
	}
	if _, err := os.Stat(filepath.Join(dest, "jabali-cache.php")); err != nil {
		t.Errorf("bundle not staged: %v", err)
	}
}

func TestStageBundledCachePlugin_RefusesAnInstallOutsideTheUsersHome(t *testing.T) {
	homes, _ := stageEnv(t)
	alice := filepath.Join(homes, "alice", "domains", "a.example", "public_html")
	alicePlugin := filepath.Join(alice, "wp-content", "plugins", "jabali-cache")
	writeFile(t, filepath.Join(alicePlugin, "keep.php"))

	if err := stageBundledCachePlugin(context.Background(), alice, "bob"); err == nil {
		t.Error("staged into another account's home")
	}
	assertOtherTreeUntouched(t, alicePlugin)
}

func TestStageBundledCachePlugin_RejectsAnOSUserThatIsAPath(t *testing.T) {
	homes, _ := stageEnv(t)
	alice := filepath.Join(homes, "alice", "public_html")
	alicePlugin := filepath.Join(alice, "wp-content", "plugins", "jabali-cache")
	writeFile(t, filepath.Join(alicePlugin, "keep.php"))

	for _, u := range []string{"bob/../alice", "..", "", "Bob", "bob/"} {
		if err := stageBundledCachePlugin(context.Background(), alice, u); err == nil {
			t.Errorf("os_user %q accepted", u)
		}
	}
	assertOtherTreeUntouched(t, alicePlugin)
}

// The WordPress.org channel (opt-in) lets wp-cli install the plugin as the
// account; the agent then only sets the group, by file descriptor from the
// home, not with a path-based chown -R.
func TestCacheSet_WordPressOrgChownRefusesALinkedPluginsDir(t *testing.T) {
	_, site := stageEnv(t)
	t.Setenv("JABALI_WP_CACHE_SOURCE", "wordpress-org")
	calls := captureExec(t)
	other := otherTree(t)
	plugins := filepath.Join(site, "wp-content", "plugins")
	if err := os.RemoveAll(plugins); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, plugins); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(site, "wp-config.php"), []byte("<?php\n/* That's all, stop editing! */\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	raw, _ := json.Marshal(wordpressCacheSetParams{InstallPath: site, OSUser: "bob", Enable: true, Prefix: "bob:1"})
	if _, err := wordpressCacheSetHandler(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "has a symlink or a non-directory") {
		t.Errorf("err = %v, want a symlink refusal", err)
	}
	for _, c := range *calls {
		if c[0] == "chown" {
			t.Errorf("ran a path-based chown: %v", c)
		}
	}
	assertOtherTreeUntouched(t, filepath.Join(other, "jabali-cache"))
}

// The plugin files belong to the account, never to root.
func TestCachePluginOwnerIDs_RefusesRoot(t *testing.T) {
	if _, _, err := cachePluginOwnerIDs("root"); err == nil {
		t.Error("root accepted as the plugin owner")
	}
}
