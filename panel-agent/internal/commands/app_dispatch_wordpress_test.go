package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// TestAppInstall_RoutesToWordPressHandler proves the package init() in
// wordpress_install.go did register the WP installer on appInstallers
// AND that a body shaped like the panel-api fixture is accepted by the
// dispatcher (errors out only on the wp-cli step, which we can't run
// in unit tests). Catches the failure mode where wordpress_install.go
// is moved/renamed and forgets to call RegisterAppInstaller.
func TestAppInstall_RoutesToWordPressHandler(t *testing.T) {
	requireHostMutationAllowed(t) // GH #994: routes into the real WP install (systemd-run + rm -rf under the tenant home)
	body := json.RawMessage(`{
		"app_type": "wordpress",
		"os_user": "alice",
		"docroot": "/home/alice/domains/example.com/public_html",
		"db_name": "alice_wp_x",
		"db_user": "alice_wp_x",
		"db_password": "p",
		"db_host": "localhost",
		"site_url": "https://example.com",
		"site_title": "t",
		"admin_user": "admin",
		"admin_pass": "p",
		"admin_email": "a@b.c",
		"locale": "en_US",
		"subdirectory": "",
		"use_www": false
	}`)

	_, err := appInstallHandler(context.Background(), body)
	// We expect SOME error (no real wp-cli, no docroot on disk) — but
	// it must come from the WordPress installer downstream of dispatch,
	// NOT a "unknown app_type" or "missing app_type" rejection by the
	// dispatcher itself.
	if err == nil {
		t.Skip("wp-cli somehow succeeded in unit test environment; cannot assert routing")
	}
	if msg := err.Error(); strings.Contains(msg, "unknown app_type") || strings.Contains(msg, "app_type is required") {
		t.Fatalf("dispatcher refused the routed call: %v", err)
	}
}

// TestAppDelete_RoutesToWordPressHandler is the delete twin. The agent
// delete checks docroot validity first and rejects an unrooted path
// with CodeInvalidArgument — which is fine for routing assertion: the
// error came from the WP delete handler, not the dispatcher.
func TestAppDelete_RoutesToWordPressHandler(t *testing.T) {
	tmp := t.TempDir()
	docroot := filepath.Join(tmp, "alice/domains/example.com/public_html")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"app_type": "wordpress",
		"os_user":  "alice",
		"docroot":  docroot,
		"domain":   "example.com",
	})
	_, err := appDeleteHandler(context.Background(), body)
	if err == nil {
		return // delete may legitimately succeed if no WP files to remove
	}
	var ae *agentwire.AgentError
	if errors.As(err, &ae) && (strings.Contains(ae.Message, "unknown app_type") || strings.Contains(ae.Message, "app_type is required")) {
		t.Fatalf("dispatcher refused the routed call: %v", err)
	}
}

// TestAppClone_DispatchOnly proves clone routing works (the agent's
// wordpressCloneHandler will reject the body for missing fields, but
// that error comes from the WP handler — not the dispatcher).
func TestAppClone_DispatchOnly(t *testing.T) {
	body := json.RawMessage(`{"app_type":"wordpress"}`)
	_, err := appCloneHandler(context.Background(), body)
	if err == nil {
		return
	}
	if strings.Contains(err.Error(), "unknown app_type") || strings.Contains(err.Error(), "app_type is required") {
		t.Fatalf("dispatcher refused the routed call: %v", err)
	}
}
