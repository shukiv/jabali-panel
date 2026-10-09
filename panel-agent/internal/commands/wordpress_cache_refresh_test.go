package commands

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// captureExec records every command the handler runs; the commands themselves
// still go to the test binary's no-op stub.
func captureExec(t *testing.T) *[][]string {
	t.Helper()
	var calls [][]string
	prev := execCommandContext
	t.Cleanup(func() { execCommandContext = prev })
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		return prev(ctx, name, args...)
	}
	return &calls
}

func fakeCacheBundle(t *testing.T, header string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "jabali-cache.php"), []byte(header), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := bundledCachePluginSrc
	bundledCachePluginSrc = dir
	t.Cleanup(func() { bundledCachePluginSrc = prev })
}

const bundleHeader121 = "<?php\n/**\n * Plugin Name:       Jabali Cache\n * Version:           1.2.1\n * Requires PHP:      7.4\n */\n"

func ranAsTenant(calls [][]string) bool {
	for _, c := range calls {
		if c[0] == "systemd-run" {
			return true
		}
	}
	return false
}

// The bundled refresh decides nothing from the site's own WordPress or wp-cli
// config: a site without a plugins directory is skipped without running
// anything as the tenant.
func TestCachePluginRefresh_BundledSkipsASiteWithoutPluginsWithoutRunningWP(t *testing.T) {
	t.Setenv("JABALI_WP_CACHE_SOURCE", "")
	fakeCacheBundle(t, bundleHeader121)
	calls := captureExec(t)

	raw, _ := json.Marshal(wordpressCachePluginRefreshParams{InstallPath: "/home/nosuchuser-jc/domains/x/public_html", OSUser: "nosuchuser-jc"})
	out, err := wordpressCachePluginRefreshHandler(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	res := out.(wordpressCachePluginRefreshResult)
	if res.Refreshed {
		t.Errorf("result = %+v, want a skip for a site without wp-content/plugins", res)
	}
	if ranAsTenant(*calls) {
		t.Errorf("ran as the tenant: %v", *calls)
	}
}

// It stages the root-owned bundle and reports the bundle's version, which is
// what the site now runs and what the panel tightens the site's ACL from.
func TestRefreshBundledCachePlugin_StagesAndReportsTheBundleVersion(t *testing.T) {
	fakeCacheBundle(t, bundleHeader121)
	calls := captureExec(t)
	site := t.TempDir()
	if err := os.MkdirAll(filepath.Join(site, "wp-content", "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := refreshBundledCachePlugin(context.Background(), site, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Refreshed || res.Version != "1.2.1" {
		t.Errorf("result = %+v, want refreshed to the bundle's 1.2.1", res)
	}
	if ranAsTenant(*calls) {
		t.Errorf("ran as the tenant: %v", *calls)
	}
	dest := filepath.Join(site, "wp-content", "plugins", "jabali-cache")
	var cp bool
	for _, c := range *calls {
		if c[0] == "cp" && strings.Join(c[1:], " ") == "-a "+bundledCachePluginSrc+" "+dest {
			cp = true
		}
	}
	if !cp {
		t.Errorf("calls = %v, want the bundle copied to %s", *calls, dest)
	}
}

// A site the agent couldn't stage must not report the bundle's version: the
// panel would tighten the ACL of a site still running the old plugin.
func TestRefreshBundledCachePlugin_AFailedStagingIsAnError(t *testing.T) {
	prev := bundledCachePluginSrc
	bundledCachePluginSrc = filepath.Join(t.TempDir(), "missing")
	t.Cleanup(func() { bundledCachePluginSrc = prev })
	site := t.TempDir()
	if err := os.MkdirAll(filepath.Join(site, "wp-content", "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := refreshBundledCachePlugin(context.Background(), site, "bob")
	if err == nil || res.Refreshed || res.Version != "" {
		t.Errorf("res=%+v err=%v, want an error and no version", res, err)
	}
}

func TestRefreshBundledCachePlugin_SkipsASiteWithoutPlugins(t *testing.T) {
	fakeCacheBundle(t, bundleHeader121)
	calls := captureExec(t)

	res, err := refreshBundledCachePlugin(context.Background(), t.TempDir(), "bob")
	if err != nil || res.Refreshed || len(*calls) != 0 {
		t.Errorf("res=%+v err=%v calls=%v, want a skip that runs nothing", res, err, *calls)
	}
}

func TestBundledCachePluginVersion(t *testing.T) {
	for header, want := range map[string]string{
		bundleHeader121: "1.2.1",
		"<?php\n/*\nPlugin Name: Jabali Cache\nVersion: 2.0.0-beta1\n*/\n": "2.0.0-beta1",
		"<?php\n// no header\n":         "",
		"<?php\n * Version:   \n":       "",
		"<?php\n * Stable tag: 9.9.9\n": "",
		"<?php\n * Description: Needs PHP Version: 7.4 or later\n * Version: 1.2.1\n": "1.2.1",
		"<?php\n * Version: dev\n": "",
	} {
		fakeCacheBundle(t, header)
		if got := bundledCachePluginVersion(); got != want {
			t.Errorf("header %q: version %q, want %q", header, got, want)
		}
	}
	prev := bundledCachePluginSrc
	bundledCachePluginSrc = filepath.Join(t.TempDir(), "missing")
	defer func() { bundledCachePluginSrc = prev }()
	if got := bundledCachePluginVersion(); got != "" {
		t.Errorf("missing bundle: version %q, want empty", got)
	}
}
