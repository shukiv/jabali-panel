package cpanel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// wpDocroot builds a docroot that looks like a WordPress install, plus an
// optional index.html body (skipped when body is nil).
func wpDocroot(t *testing.T, indexHTML []byte, files ...string) string {
	t.Helper()
	dir := t.TempDir()
	if files == nil {
		files = []string{"wp-load.php", "index.php"}
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("<?php\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	if indexHTML != nil {
		if err := os.WriteFile(filepath.Join(dir, "index.html"), indexHTML, 0o644); err != nil {
			t.Fatalf("write index.html: %v", err)
		}
	}
	return dir
}

// The bug as observed: a CyberPanel default page in front of a live WordPress
// install. It must be renamed out of the way, not deleted.
func TestQuarantine_CyberPanelDefaultIsQuarantined(t *testing.T) {
	body := []byte(`<html><head><title>CyberPanel</title></head>
<body><h1>CyberPanel Installed</h1><p>please remove this page</p></body></html>`)
	dir := wpDocroot(t, body)

	res := QuarantineShadowingIndexHTML(context.Background(), nil, "uid", "user", []string{dir})

	if len(res.Quarantined) != 1 {
		t.Fatalf("want 1 quarantined, got %d (flagged=%v skipped=%v)",
			len(res.Quarantined), res.Flagged, res.Skipped)
	}
	if _, err := os.Stat(filepath.Join(dir, "index.html")); !os.IsNotExist(err) {
		t.Errorf("index.html should no longer shadow index.php, stat err = %v", err)
	}
	// Renamed, never destroyed — the operator has to be able to get it back.
	preserved := filepath.Join(dir, "index.html"+quarantineSuffix)
	got, err := os.ReadFile(preserved)
	if err != nil {
		t.Fatalf("quarantined file missing: %v", err)
	}
	if string(got) != string(body) {
		t.Error("quarantined file content was altered")
	}
}

// Every marker must actually trigger — a typo in the table is a silent no-op
// that only shows up as another customer serving a parked page.
func TestQuarantine_EveryParkedMarkerMatches(t *testing.T) {
	for _, marker := range parkedPageMarkers {
		t.Run(marker, func(t *testing.T) {
			// Upper-cased to pin the case-insensitive match too.
			body := []byte("<html><body>" + strings.ToUpper(marker) + "</body></html>")
			dir := wpDocroot(t, body)

			res := QuarantineShadowingIndexHTML(context.Background(), nil, "uid", "user", []string{dir})
			if len(res.Quarantined) != 1 {
				t.Fatalf("marker %q did not quarantine (flagged=%v skipped=%v)",
					marker, res.Flagged, res.Skipped)
			}
		})
	}
}

// The conservative half: an unrecognised page is a page somebody may have meant
// to publish. Report it, never move it.
func TestQuarantine_UnknownPageIsFlaggedNotMoved(t *testing.T) {
	body := []byte(`<html><body><h1>Acme Ltd — back soon</h1></body></html>`)
	dir := wpDocroot(t, body)

	res := QuarantineShadowingIndexHTML(context.Background(), nil, "uid", "user", []string{dir})

	if len(res.Quarantined) != 0 {
		t.Fatalf("must not move an unrecognised page, quarantined=%v", res.Quarantined)
	}
	if len(res.Flagged) != 1 {
		t.Fatalf("want 1 flagged, got %d", len(res.Flagged))
	}
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		t.Errorf("unrecognised index.html must stay put: %v", err)
	}
}

// A "coming soon" splash is a deliberate publish, not a leftover. Explicitly
// pinned because it is the most tempting thing to add to the marker table.
func TestQuarantine_ComingSoonIsNotTreatedAsParked(t *testing.T) {
	dir := wpDocroot(t, []byte(`<html><body>Coming Soon! Under construction.</body></html>`))

	res := QuarantineShadowingIndexHTML(context.Background(), nil, "uid", "user", []string{dir})
	if len(res.Quarantined) != 0 {
		t.Fatalf("coming-soon page must not be quarantined, got %v", res.Quarantined)
	}
}

// No WordPress means no shadow to resolve — a static site's index.html is the
// whole point of the site.
func TestQuarantine_NonWordPressDocrootUntouched(t *testing.T) {
	// index.php present but no wp-load.php: some other PHP app.
	dir := wpDocroot(t, []byte("<html>CyberPanel Installed</html>"), "index.php")

	res := QuarantineShadowingIndexHTML(context.Background(), nil, "uid", "user", []string{dir})

	if len(res.Quarantined) != 0 || len(res.Flagged) != 0 {
		t.Fatalf("non-WordPress docroot must be untouched: quarantined=%v flagged=%v",
			res.Quarantined, res.Flagged)
	}
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		t.Errorf("index.html must survive: %v", err)
	}
}

// A WordPress docroot with no index.html at all is the common case and must
// produce no output whatsoever.
func TestQuarantine_NoIndexHTMLIsSilent(t *testing.T) {
	dir := wpDocroot(t, nil)

	res := QuarantineShadowingIndexHTML(context.Background(), nil, "uid", "user", []string{dir})
	if len(res.Quarantined)+len(res.Flagged)+len(res.Skipped) != 0 {
		t.Fatalf("clean docroot produced output: %+v", res)
	}
}

// Size guard: a large index.html is a real page regardless of what strings it
// happens to contain.
func TestQuarantine_OversizedIndexIsFlaggedNotMoved(t *testing.T) {
	big := append([]byte("<html>CyberPanel Installed"), make([]byte, maxIndexHTMLScan+1)...)
	dir := wpDocroot(t, big)

	res := QuarantineShadowingIndexHTML(context.Background(), nil, "uid", "user", []string{dir})

	if len(res.Quarantined) != 0 {
		t.Fatalf("oversized page must not be moved, got %v", res.Quarantined)
	}
	if len(res.Flagged) != 1 {
		t.Fatalf("want 1 flagged, got %d (skipped=%v)", len(res.Flagged), res.Skipped)
	}
}

// Re-running a restore must not clobber the file quarantined the first time.
func TestQuarantine_ExistingBackupIsNotOverwritten(t *testing.T) {
	dir := wpDocroot(t, []byte("<html>CyberPanel Installed</html>"))
	preserved := filepath.Join(dir, "index.html"+quarantineSuffix)
	if err := os.WriteFile(preserved, []byte("FIRST RUN"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	res := QuarantineShadowingIndexHTML(context.Background(), nil, "uid", "user", []string{dir})

	if len(res.Quarantined) != 0 {
		t.Fatalf("must not overwrite an existing quarantine, got %v", res.Quarantined)
	}
	if len(res.Skipped) != 1 {
		t.Fatalf("want 1 skipped, got %d", len(res.Skipped))
	}
	got, err := os.ReadFile(preserved)
	if err != nil || string(got) != "FIRST RUN" {
		t.Errorf("earlier quarantine was clobbered: %q err=%v", got, err)
	}
}

// A directory named index.html must not be renamed.
func TestQuarantine_DirectoryNamedIndexHTMLIgnored(t *testing.T) {
	dir := wpDocroot(t, nil)
	if err := os.Mkdir(filepath.Join(dir, "index.html"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	res := QuarantineShadowingIndexHTML(context.Background(), nil, "uid", "user", []string{dir})
	if len(res.Quarantined)+len(res.Flagged) != 0 {
		t.Fatalf("directory must be ignored: %+v", res)
	}
}
