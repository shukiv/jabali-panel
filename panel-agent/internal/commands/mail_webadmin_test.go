package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMailWebadminApply_RenderAndGate(t *testing.T) {
	newFakeOAuthStalwart(t) // apply registers the WebAdmin OAuth client (GH #2542)
	sa, se := t.TempDir(), t.TempDir()
	oldA, oldE := mailVhostSitesAvailable, mailVhostSitesEnabled
	mailVhostSitesAvailable, mailVhostSitesEnabled = sa, se
	defer func() { mailVhostSitesAvailable, mailVhostSitesEnabled = oldA, oldE }()
	stalwartAdminHtpasswd = filepath.Join(t.TempDir(), "htpasswd")

	oldReload := nginxTestAndReload
	nginxTestAndReload = func(context.Context) error { return nil }
	defer func() { nginxTestAndReload = oldReload }()

	params, _ := json.Marshal(map[string]any{
		"enabled":       true,
		"server_name":   "mx.example.com",
		"port":          8449,
		"ssl_cert_path": "/etc/jabali/tls/panel.crt",
		"ssl_key_path":  "/etc/jabali/tls/panel.key",
		"allow_cidrs":   []string{"203.0.113.0/24", "198.51.100.5"},
	})
	if _, err := mailWebadminApplyHandler(context.Background(), params); err != nil {
		t.Fatalf("apply: %v", err)
	}

	conf, _ := os.ReadFile(filepath.Join(sa, stalwartAdminVhostName))
	cs := string(conf)
	for _, want := range []string{
		"server_name mx.example.com;",
		"listen 8449 ssl",
		"proxy_pass http://127.0.0.1:8446;", // Stalwart stays loopback
		"allow 203.0.113.0/24;", "allow 198.51.100.5;", "deny all;",
		"return 302 /admin/;", // bare root -> SPA entry
	} {
		if !strings.Contains(cs, want) {
			t.Errorf("vhost missing %q\n%s", want, cs)
		}
	}
	// basic-auth was dropped (ADR-0142) — it must NOT be in the vhost.
	for _, gone := range []string{"auth_basic", "auth_basic_user_file", "htpasswd"} {
		if strings.Contains(cs, gone) {
			t.Errorf("vhost still references %q (basic-auth should be gone)\n%s", gone, cs)
		}
	}
	// symlink enabled
	if _, err := os.Lstat(filepath.Join(se, stalwartAdminVhostName)); err != nil {
		t.Error("vhost not symlinked into sites-enabled")
	}

	// disable removes the vhost
	dp, _ := json.Marshal(map[string]any{"enabled": false})
	if _, err := mailWebadminApplyHandler(context.Background(), dp); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sa, stalwartAdminVhostName)); !os.IsNotExist(err) {
		t.Error("disable must remove the vhost conf")
	}
}

func TestMailWebadminApply_RejectsBadCIDR(t *testing.T) {
	stalwartAdminHtpasswd = filepath.Join(t.TempDir(), "htpasswd")
	params, _ := json.Marshal(map[string]any{
		"enabled": true, "server_name": "admin.mail.x.com",
		"ssl_cert_path": "/c", "ssl_key_path": "/k",
		"allow_cidrs": []string{"not-a-cidr"},
	})
	if _, err := mailWebadminApplyHandler(context.Background(), params); err == nil {
		t.Fatal("bad CIDR must be rejected")
	}
}
