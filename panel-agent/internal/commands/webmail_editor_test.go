package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The editor env file holds exactly the panel's URL (an empty URL still
// writes the line, turning editing off), and webmail is restarted
// (try-restart) only when the file changed.
func TestWebmailEditorApply_WritesFileAndRestartsOnlyOnChange(t *testing.T) {
	tmp := t.TempDir()
	origEnv, origCtl := webmailEditorEnvFile, runSystemctl
	t.Cleanup(func() {
		webmailEditorEnvFile, runSystemctl = origEnv, origCtl
	})
	webmailEditorEnvFile = filepath.Join(tmp, "bulwark-editor.env")
	var restarts [][]string
	runSystemctl = func(_ context.Context, args ...string) ([]byte, error) {
		restarts = append(restarts, args)
		return nil, nil
	}
	apply := func(p webmailEditorApplyParams) {
		t.Helper()
		raw, _ := json.Marshal(p)
		if _, err := webmailEditorApplyHandler(context.Background(), raw); err != nil {
			t.Fatal(err)
		}
	}

	apply(webmailEditorApplyParams{WopiClientURL: "https://office.example.com"})
	body, err := os.ReadFile(webmailEditorEnvFile)
	if err != nil {
		t.Fatal(err)
	}
	want := "# Webmail document editor, written by the panel (webmail.editor.apply).\n" +
		"WOPI_CLIENT_URL=\"https://office.example.com\"\n"
	if string(body) != want {
		t.Fatalf("env file = %q, want %q", body, want)
	}
	if fi, err := os.Stat(webmailEditorEnvFile); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("env file mode = %v, %v", fi, err)
	}
	if len(restarts) != 1 || strings.Join(restarts[0], " ") != "try-restart jabali-webmail.service" {
		t.Fatalf("restarts = %v, want one try-restart", restarts)
	}

	// A second identical call does not rewrite the file and does not restart.
	before, _ := os.Stat(webmailEditorEnvFile)
	apply(webmailEditorApplyParams{WopiClientURL: "https://office.example.com"})
	after, _ := os.Stat(webmailEditorEnvFile)
	if len(restarts) != 1 {
		t.Fatalf("an unchanged editor restarted webmail (%d)", len(restarts))
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("an unchanged editor rewrote the file")
	}

	// Empty still writes the line with an empty value, and restarts once.
	apply(webmailEditorApplyParams{WopiClientURL: ""})
	body, err = os.ReadFile(webmailEditorEnvFile)
	if err != nil {
		t.Fatal(err)
	}
	wantEmpty := "# Webmail document editor, written by the panel (webmail.editor.apply).\n" +
		"WOPI_CLIENT_URL=\"\"\n"
	if string(body) != wantEmpty {
		t.Fatalf("env file = %q, want %q", body, wantEmpty)
	}
	if len(restarts) != 2 {
		t.Fatalf("clearing the editor did not restart webmail (%d)", len(restarts))
	}

	// Invalid params are refused.
	raw, _ := json.Marshal(webmailEditorApplyParams{WopiClientURL: "http://office.example.com"})
	if _, err := webmailEditorApplyHandler(context.Background(), raw); err == nil {
		t.Fatal("invalid params were applied")
	}
}
