package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// GH #946: deleting an app left files behind. Reported against ITFlow, but
// thirteen deleters share the same shape — a hardcoded list of top-level entries
// that cannot know about anything the app created while running, or anything
// upstream added since the list was written.
//
// The manifest records what an install actually laid down. These tests cover the
// diff, and the path checks that matter because the sweep runs `rm -rf` as the
// tenant against whatever the manifest holds.

func TestInstallRoot(t *testing.T) {
	cases := []struct {
		name string
		p    appDispatchPaths
		want string
	}{
		{"docroot root", appDispatchPaths{Docroot: "/home/bob/public_html"}, "/home/bob/public_html"},
		{"subdirectory", appDispatchPaths{Docroot: "/home/bob/public_html", Subdirectory: "crm"}, "/home/bob/public_html/crm"},
		{"subdirectory with slashes", appDispatchPaths{Docroot: "/home/bob/public_html", Subdirectory: "/crm/"}, "/home/bob/public_html/crm"},
		{"trailing slash on docroot", appDispatchPaths{Docroot: "/home/bob/public_html/"}, "/home/bob/public_html"},
		// No docroot means the manifest opts out entirely rather than keying
		// every app on the same empty root.
		{"no docroot", appDispatchPaths{Subdirectory: "crm"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.installRoot(); got != tc.want {
				t.Fatalf("installRoot() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSafeManifestEntry(t *testing.T) {
	const root = "/home/bob/public_html"

	// The sweep deletes as the tenant. A stored entry that escapes the install
	// root would be a privileged delete of somewhere else on the box.
	for _, bad := range []string{
		"", ".", "..",
		"../../etc/passwd",
		"sub/dir",
		"/etc/passwd",
		"a\x00b",
	} {
		if _, ok := safeManifestEntry(root, bad); ok {
			t.Errorf("entry %q was accepted; it must be rejected", bad)
		}
	}

	for _, good := range []string{"admin", ".git", "config.php", "uploads"} {
		got, ok := safeManifestEntry(root, good)
		if !ok {
			t.Errorf("entry %q rejected; it is a plain name inside the root", good)
			continue
		}
		if want := filepath.Join(root, good); got != want {
			t.Errorf("entry %q resolved to %q, want %q", good, got, want)
		}
	}

	// Never operate on / even if a manifest somehow names it.
	if _, ok := safeManifestEntry("/", "etc"); ok {
		t.Error("root filesystem must never be a valid install root")
	}
}

func TestManifestRoundTripRecordsOnlyWhatInstallCreated(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "public_html")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// Content that was in the docroot BEFORE the install. This is the whole
	// reason the deleters use a list instead of rm -rf, so it must survive.
	for _, pre := range []string{"index.html", "family-photos"} {
		if err := os.WriteFile(filepath.Join(root, pre), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	before := topLevelEntries(root)

	// What the installer lays down, including a dotfile and a directory — the
	// two shapes a hand-written list most often misses.
	for _, made := range []string{"index.php", ".git", "uploads", "config.php"} {
		if err := os.WriteFile(filepath.Join(root, made), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var created []string
	for name := range topLevelEntries(root) {
		if !before[name] {
			created = append(created, name)
		}
	}
	if len(created) != 4 {
		t.Fatalf("diff found %d entries (%v), want the 4 the install created", len(created), created)
	}
	for _, pre := range []string{"index.html", "family-photos"} {
		for _, c := range created {
			if c == pre {
				t.Errorf("pre-existing %q was recorded as part of the install", pre)
			}
		}
	}

	// The manifest path must not depend on anything but the root, or delete
	// cannot find what install wrote.
	if manifestPathFor(root) != manifestPathFor(root+"/") {
		t.Error("manifest key is sensitive to a trailing slash; install and delete would disagree")
	}
	if manifestPathFor(root) == manifestPathFor(filepath.Join(dir, "other")) {
		t.Error("different roots must not share a manifest")
	}
}

func TestParseAppDispatchPathsToleratesPerAppShapes(t *testing.T) {
	// Every app sends its own struct; the dispatcher reads a few common fields
	// and must ignore the rest rather than failing the install.
	raw := json.RawMessage(`{"app_type":"itflow","os_user":"bob","docroot":"/home/bob/public_html",
	    "subdirectory":"","db_name":"x","admin_email":"a@b.c","version":"latest"}`)
	p := parseAppDispatchPaths(raw)
	if p.AppType != "itflow" || p.OSUser != "bob" || p.Docroot != "/home/bob/public_html" {
		t.Fatalf("parsed %+v", p)
	}

	// Malformed params must not panic or produce a bogus root — the installer
	// itself will reject them a moment later with a proper error.
	if got := parseAppDispatchPaths(json.RawMessage(`not json`)).installRoot(); got != "" {
		t.Fatalf("installRoot from bad params = %q, want empty", got)
	}
}

func TestSweepIsANoOpWithoutAManifest(t *testing.T) {
	// Installs made before manifests existed have none. The sweep must do
	// nothing and leave the per-app deleter as the only cleanup, which is
	// exactly today's behaviour.
	if n := sweepAppManifest(t.Context(), "bob", t.TempDir()); n != 0 {
		t.Fatalf("swept %d entries with no manifest present", n)
	}
	// No OS user means no way to run the removal as the tenant.
	if n := sweepAppManifest(t.Context(), "", t.TempDir()); n != 0 {
		t.Fatalf("swept %d entries without an os_user", n)
	}
}
