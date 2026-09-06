package main

import (
	"os"
	"strings"
	"testing"
)

// TestSettingsCLI_RoutesThroughSettingsops pins JAB-290: the `jabali settings`
// command must derive its nginx effect + validation from internal/settingsops
// (the shared owner the REST PATCH also uses), and must no longer hand-build the
// nginx.tunables.apply parameter map or import the HTTP (api) package for
// validation. The wire itself is covered in settings_cmd_dispatch_test.go and
// internal/settingsops; this guards against a future re-fork.
func TestSettingsCLI_RoutesThroughSettingsops(t *testing.T) {
	src, err := os.ReadFile("settings_cmd.go")
	if err != nil {
		t.Fatalf("read settings_cmd.go: %v", err)
	}
	s := string(src)

	for _, want := range []string{"settingsops.NginxEffects(", "settingsops.Validate("} {
		if !strings.Contains(s, want) {
			t.Fatalf("CLI must route through %s (JAB-290)", want)
		}
	}
	// The hand-copied wire must be gone: no literal verb, no per-key param map.
	for _, gone := range []string{`"nginx.tunables.apply"`, `"client_max_body_size":`} {
		if strings.Contains(s, gone) {
			t.Fatalf("CLI must not rebuild the nginx wire (%s) — settingsops owns it (JAB-290)", gone)
		}
	}
	// AC1: the CLI no longer imports the HTTP package for validation.
	if strings.Contains(s, "panel-api/internal/api\"") {
		t.Fatal("CLI settings command must not import internal/api (JAB-290 AC1)")
	}
}
