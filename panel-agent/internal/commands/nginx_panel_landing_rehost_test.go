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

// panelLandingFixture is a jabali-default.conf shaped like install.sh's
// install_nginx_default_vhost output: a :80 redirect block and a :443 default
// block (both `server_name _;`), then the GH#135 dedicated :443 landing vhost
// carrying the real hostname in server_name, root and the mail redirects. Only
// the landing server_name must ever change.
func panelLandingFixture(host string) string {
	return `server {
    listen 80 default_server;
    listen [::]:80 default_server;
    server_name _;
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl default_server;
    listen [::]:443 ssl default_server;
    server_name _;

    ssl_certificate     /etc/jabali/tls/panel.crt;
    ssl_certificate_key /etc/jabali/tls/panel.key;

    location / {
        include /etc/nginx/jabali-catchall.conf;
    }
}

server {
    listen 443 ssl;
    listen [::]:443 ssl;
    server_name ` + host + `;

    ssl_certificate     /etc/jabali/tls/panel.crt;
    ssl_certificate_key /etc/jabali/tls/panel.key;

    root  /var/www/` + host + `;
    index index.php index.html;

    location = /webmail  { return 301 https://mail.` + host + `/; }
    location = /webmail/ { return 301 https://mail.` + host + `/; }

    location / {
        try_files $uri $uri/ =404;
    }
}
`
}

// withPanelLandingVhost points the verb at a temp jabali-default.conf for the
// duration of one non-parallel test.
func withPanelLandingVhost(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "jabali-default.conf")
	if contents != "" {
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prev := panelLandingVhostPath
	panelLandingVhostPath = path
	t.Cleanup(func() { panelLandingVhostPath = prev })
	return path
}

func callLandingRehost(t *testing.T, hostname string) (nginxPanelLandingRehostResponse, error) {
	t.Helper()
	raw, err := nginxPanelLandingRehostHandler(context.Background(), json.RawMessage(`{"hostname":"`+hostname+`"}`))
	if err != nil {
		return nginxPanelLandingRehostResponse{}, err
	}
	b, _ := json.Marshal(raw)
	var resp nginxPanelLandingRehostResponse
	if uerr := json.Unmarshal(b, &resp); uerr != nil {
		t.Fatalf("unmarshal response: %v", uerr)
	}
	return resp, nil
}

func TestRewritePanelLandingServerName(t *testing.T) {
	t.Parallel()
	in := panelLandingFixture("old.example.com")
	out, n := rewritePanelLandingServerName([]byte(in), "old.example.com", "mx.example.com")
	s := string(out)
	if n != 1 {
		t.Fatalf("replacements = %d, want 1 (only the one non-`_` server_name)", n)
	}
	if !strings.Contains(s, "server_name mx.example.com;") {
		t.Errorf("landing server_name not rewritten:\n%s", s)
	}
	if strings.Count(s, "server_name _;") != 2 {
		t.Errorf("the two catch-all `server_name _;` blocks must be untouched:\n%s", s)
	}
	// root and the mail redirects keep the OLD host by construction — server_name only.
	if !strings.Contains(s, "root  /var/www/old.example.com;") {
		t.Errorf("root docroot must be left on the old host (no docroot migration):\n%s", s)
	}
	if strings.Count(s, "https://mail.old.example.com/;") != 2 {
		t.Errorf("mail webmail redirects must be left untouched (#1546 decoupling):\n%s", s)
	}
	if strings.Contains(s, "https://mail.mx.example.com/") {
		t.Errorf("mail redirect was wrongly re-pointed to the new host")
	}
}

func TestNginxPanelLandingRehost_Rewrites(t *testing.T) {
	// execCommandContext is the always-exit-0 stub from TestMain, so nginx -t
	// and reload both "succeed" — this asserts the file mutation only.
	path := withPanelLandingVhost(t, panelLandingFixture("old.example.com"))

	resp, err := callLandingRehost(t, "mx.example.com")
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !resp.Rewritten || resp.Replacements != 1 {
		t.Fatalf("resp = %+v, want rewritten with 1 replacement", resp)
	}
	out, _ := os.ReadFile(path)
	s := string(out)
	if !strings.Contains(s, "server_name mx.example.com;") {
		t.Errorf("new server_name not on disk:\n%s", s)
	}
	if strings.Contains(s, "server_name old.example.com;") {
		t.Errorf("old server_name survived on disk")
	}
	if strings.Count(s, "server_name _;") != 2 {
		t.Errorf("catch-all blocks mutated")
	}
	if !strings.Contains(s, "root  /var/www/old.example.com;") || strings.Count(s, "https://mail.old.example.com/;") != 2 {
		t.Errorf("root or mail redirects were changed (must stay on old host):\n%s", s)
	}
	// Mode preserved by the atomic write.
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o644 {
		t.Errorf("mode = %o, want 644", fi.Mode().Perm())
	}
}

func TestNginxPanelLandingRehost_NoChurnWhenCurrent(t *testing.T) {
	path := withPanelLandingVhost(t, panelLandingFixture("mx.example.com"))
	before, _ := os.ReadFile(path)

	resp, err := callLandingRehost(t, "mx.example.com") // already current
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if resp.Rewritten {
		t.Fatalf("expected no rewrite when server_name already current, got %+v", resp)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("file mutated on the no-churn path")
	}
}

func TestNginxPanelLandingRehost_RejectsInvalidHost(t *testing.T) {
	path := withPanelLandingVhost(t, panelLandingFixture("old.example.com"))
	before, _ := os.ReadFile(path)

	if _, err := nginxPanelLandingRehostHandler(context.Background(), json.RawMessage(`{"hostname":"bad host!!"}`)); err == nil {
		t.Fatal("expected error for invalid hostname")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("file mutated on invalid hostname")
	}
}

func TestNginxPanelLandingRehost_MissingServerName(t *testing.T) {
	// A file with only `server_name _;` blocks has no landing host to rehost.
	onlyCatchAll := "server {\n    listen 443 ssl default_server;\n    server_name _;\n}\n"
	withPanelLandingVhost(t, onlyCatchAll)
	if _, err := callLandingRehost(t, "mx.example.com"); err == nil {
		t.Fatal("expected error when no landing server_name is present")
	}
}

func TestNginxPanelLandingRehost_MissingFile(t *testing.T) {
	withPanelLandingVhost(t, "") // no file written
	if _, err := callLandingRehost(t, "mx.example.com"); err == nil {
		t.Fatal("expected error for missing jabali-default.conf")
	}
}

func TestNginxPanelLandingRehost_NginxTestFailureRollsBack(t *testing.T) {
	path := withPanelLandingVhost(t, panelLandingFixture("old.example.com"))
	before, _ := os.ReadFile(path)

	// Make `nginx -t` fail (exit 1) but leave everything else succeeding. A
	// broken jabali-default.conf must be rolled back to the original bytes.
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "nginx" && len(args) > 0 && args[0] == "-t" {
			return exec.CommandContext(ctx, "false")
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })

	if _, err := callLandingRehost(t, "mx.example.com"); err == nil {
		t.Fatal("expected error when nginx -t fails")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatalf("file not rolled back after nginx -t failure:\n%s", string(after))
	}
}
