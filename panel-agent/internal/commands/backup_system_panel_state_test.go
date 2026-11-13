package commands

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// GH #2502: the branding pictures travel in panel_config. A host captures the
// dirs it has (no warning for one it lacks), and a restore puts back the
// ones the backup holds; a backup from before they were captured has none.
func TestPanelStateDirs_CaptureAndRestore(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not installed")
	}
	tmp := t.TempDir()
	branding := filepath.Join(tmp, "live", "branding")
	webmail := filepath.Join(tmp, "live", "webmail-branding")
	orig, origExec := panelStateDirs, execCommandContext
	t.Cleanup(func() { panelStateDirs, execCommandContext = orig, origExec })
	panelStateDirs = []string{branding, webmail}
	// TestMain stubs every command; run the real rsync, only between this
	// test's own dirs.
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		for _, a := range args {
			if name != "rsync" || (strings.HasPrefix(a, "/") && !strings.HasPrefix(a, tmp)) {
				t.Fatalf("unexpected command %s %v", name, args)
			}
		}
		return exec.CommandContext(ctx, name, args...)
	}

	if err := os.MkdirAll(webmail, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := panelConfigPaths(); !slices.Equal(got, []string{"/etc/jabali-panel", webmail}) {
		t.Fatalf("panelConfigPaths = %v, want /etc/jabali-panel and only the dir this host has", got)
	}

	// A staged panel_config that holds the webmail pictures but, like a
	// backup made before, not the server-wide logos.
	stagingRoot := filepath.Join(tmp, "staging")
	stageDir := filepath.Join(stagingRoot, "panel_config")
	pic := filepath.Join("01HZZZZZZZZZZZZZZZZZZZZZZZ", "logo-light.png")
	if err := os.MkdirAll(filepath.Join(stageDir, webmail, filepath.Dir(pic)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, webmail, pic), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webmail, "stale"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	applied, warnings := applyPanelStateDirs(context.Background(), stagingRoot, stageDir)
	if b, err := os.ReadFile(filepath.Join(webmail, pic)); err != nil || string(b) != "png" {
		t.Fatalf("picture not restored: %q, %v (applied %v, warnings %v)", b, err, applied, warnings)
	}
	if _, err := os.Stat(filepath.Join(webmail, "stale")); !os.IsNotExist(err) {
		t.Fatal("a file the backup doesn't have stayed: the dir is not what the backup holds")
	}
	if _, err := os.Stat(branding); !os.IsNotExist(err) {
		t.Fatal("a dir the backup doesn't hold was created")
	}
	if !slices.Contains(applied, "panel_config → "+webmail) {
		t.Fatalf("applied = %v", applied)
	}
	// The staging has no os_users.json, so ownership can't be normalized;
	// that is the only warning.
	for _, w := range warnings {
		if !strings.HasPrefix(w, "ownership normalize:") {
			t.Fatalf("unexpected warning %q", w)
		}
	}

	// The panel's user owns the dirs' parent, so it can put a link where a
	// dir was. The restore, run as root, replaces the link with the backup's
	// dir; it neither writes nor deletes where the link points, nor touches
	// the parent's other entries.
	elsewhere := filepath.Join(tmp, "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(elsewhere, "keep"), []byte("theirs"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(webmail); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, webmail); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(filepath.Dir(webmail), "last-built-sha")
	if err := os.WriteFile(sibling, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, warnings = applyPanelStateDirs(context.Background(), stagingRoot, stageDir)
	if fi, err := os.Lstat(webmail); err != nil || !fi.IsDir() {
		t.Fatalf("webmail-branding is %v (%v), want the backup's folder in place of the link (warnings %v)", fi.Mode(), err, warnings)
	}
	if b, err := os.ReadFile(filepath.Join(webmail, pic)); err != nil || string(b) != "png" {
		t.Fatalf("picture not restored in place of the link: %q, %v", b, err)
	}
	entries, _ := os.ReadDir(elsewhere)
	if len(entries) != 1 || entries[0].Name() != "keep" {
		t.Fatalf("the linked folder holds %v, want only its own file", entries)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Fatalf("the parent's other file went: %v", err)
	}
}
