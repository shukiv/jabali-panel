package commands

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// GH #2502: the branding pictures travel in panel_config. A host captures the
// dirs it has (no warning for one it lacks), and a restore puts back the
// ones the backup holds; a backup from before they were captured has none.
func TestPanelStateDirs_CaptureAndRestore(t *testing.T) {
	tmp := t.TempDir()
	live := filepath.Join(tmp, "live")
	branding := filepath.Join(live, "branding")
	webmail := filepath.Join(live, "webmail-branding")
	orig := panelStateDirs
	t.Cleanup(func() { panelStateDirs = orig })
	panelStateDirs = []string{branding, webmail}

	if err := os.MkdirAll(webmail, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := panelConfigPaths(); !slices.Equal(got, []string{"/etc/jabali-panel", webmail}) {
		t.Fatalf("panelConfigPaths = %v, want /etc/jabali-panel and only the dir this host has", got)
	}

	// stage makes a staged panel_config that holds the webmail pictures but,
	// like a backup made before, not the server-wide logos.
	pic := filepath.Join("01HZZZZZZZZZZZZZZZZZZZZZZZ", "logo-light.png")
	stage := func() string {
		stageDir := filepath.Join(tmp, "staging", "panel_config")
		if err := os.RemoveAll(stageDir); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(stageDir, webmail, filepath.Dir(pic)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stageDir, webmail, pic), []byte("png"), 0o644); err != nil {
			t.Fatal(err)
		}
		return stageDir
	}
	if err := os.WriteFile(filepath.Join(webmail, "stale"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	applied, warnings := applyPanelStateDirs(stage())
	if b, err := os.ReadFile(filepath.Join(webmail, pic)); err != nil || string(b) != "png" {
		t.Fatalf("picture not restored: %q, %v (applied %v, warnings %v)", b, err, applied, warnings)
	}
	if _, err := os.Stat(filepath.Join(webmail, "stale")); !os.IsNotExist(err) {
		t.Fatal("a file the backup doesn't have stayed: the dir is not what the backup holds")
	}
	if _, err := os.Stat(branding); !os.IsNotExist(err) {
		t.Fatal("a dir the backup doesn't hold was created")
	}
	if !slices.Equal(applied, []string{"panel_config → " + webmail}) || len(warnings) != 0 {
		t.Fatalf("applied %v warnings %v", applied, warnings)
	}

	// The panel's user owns the dirs' parent, so it can put a link where a
	// dir was. The restore, run as root, puts the backup's dir in place of
	// the link; it neither writes nor deletes where the link points, nor
	// touches the parent's other entries.
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
	sibling := filepath.Join(live, "last-built-sha")
	if err := os.WriteFile(sibling, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, warnings = applyPanelStateDirs(stage())
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

	// A staged tree holding a link that points outside is not put in place.
	stageDir := stage()
	if err := os.Symlink("/etc/shadow", filepath.Join(stageDir, webmail, "01HZZZZZZZZZZZZZZZZZZZZZZZ", "favicon.png")); err != nil {
		t.Fatal(err)
	}
	_, warnings = applyPanelStateDirs(stageDir)
	if len(warnings) != 1 {
		t.Fatalf("warnings %v, want the staged link refused", warnings)
	}
	if _, err := os.Lstat(filepath.Join(webmail, "01HZZZZZZZZZZZZZZZZZZZZZZZ", "favicon.png")); !os.IsNotExist(err) {
		t.Fatalf("the staged link reached the live folder: %v", err)
	}
}
