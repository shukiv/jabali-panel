package htaccess

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/nginxrules"
)

// TestCompiledOutputPassesNginxT is the real gate: convert a representative
// .htaccess, compile the typed rules with the production nginxrules compiler,
// wrap them in a minimal http{server{}} config, and run `nginx -t`. Skipped
// when nginx isn't installed (e.g. the Go-only CI job); the unit tests above
// still pin the mapping.
func TestCompiledOutputPassesNginxT(t *testing.T) {
	nginxBin, err := exec.LookPath("nginx")
	if err != nil {
		t.Skip("nginx not installed; skipping nginx -t integration gate")
	}

	in := `RewriteBase /
Redirect 301 /old https://example.com/new
RewriteRule ^widget/([0-9]+)$ /widget.php?id=$1 [L]
Header always set X-Frame-Options "SAMEORIGIN"
php_value upload_max_filesize 64M
Order Allow,Deny
Allow from 203.0.113.0/24`
	r := Convert(in, "/")
	if len(r.Rules) == 0 {
		t.Fatalf("fixture produced no rules to validate")
	}

	d := &models.Domain{NginxRules: models.NginxRules(r.Rules)}
	directives := nginxrules.Compile(d)
	if strings.TrimSpace(directives) == "" {
		t.Fatalf("compiler produced empty output for %d rules", len(r.Rules))
	}

	dir := t.TempDir()
	pidPath := filepath.Join(dir, "nginx.pid")

	// Point error_log at stderr, pid into the test's temp dir, and turn
	// access_log off. Without these, `nginx -t` falls back to the compiled
	// defaults (/var/log/nginx/error.log, /run/nginx.pid, /var/log/nginx/
	// access.log), which an unprivileged CI runner cannot open -- nginx then
	// prints "syntax is ok" but still exits 1, false-failing this gate. The
	// config being validated is fine; this only isolates the test from the
	// runner's filesystem permissions (chronic self-hosted-runner flake).
	conf := "error_log stderr;\npid " + pidPath + ";\n" +
		"events {}\nhttp {\n  access_log off;\n  server {\n    listen 8081;\n    server_name _;\n" +
		directives + "  }\n}\n"

	confPath := filepath.Join(dir, "nginx.conf")
	if err := os.WriteFile(confPath, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(nginxBin, "-t", "-c", confPath, "-p", dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("nginx -t rejected compiled output: %v\n--- config ---\n%s\n--- nginx ---\n%s", err, conf, out)
	}
}

func TestRewriteEngineOffSkipsRules(t *testing.T) {
	in := `RewriteEngine Off
RewriteRule ^x$ /y [R=301,L]`
	r := Convert(in, "/")
	if len(r.Rules) != 0 {
		t.Errorf("RewriteEngine Off should suppress rewrite rules, got %+v", r.Rules)
	}
	if len(r.Warnings) == 0 {
		t.Errorf("a suppressed rule should warn")
	}
}

func TestMalformedDirectivesWarn(t *testing.T) {
	for _, in := range []string{
		"RewriteRule ^onlypattern",
		"Redirect",
		"RedirectMatch 301",
		"php_value onlyname",
		"Header",
	} {
		r := Convert(in, "/")
		if len(r.Rules) != 0 {
			t.Errorf("%q: malformed input must not emit a rule", in)
		}
		if len(r.Warnings) == 0 {
			t.Errorf("%q: malformed input should warn", in)
		}
	}
}

func TestOptionsMinusIndexesIsNote(t *testing.T) {
	r := Convert("Options -Indexes", "/")
	if len(r.Notes) == 0 {
		t.Errorf("Options -Indexes should be an informational note (already nginx default)")
	}
	if len(r.Rules) != 0 || hasSecurityWarning(r) {
		t.Errorf("Options -Indexes should be a no-op, got %+v / sec=%v", r.Rules, hasSecurityWarning(r))
	}
}

func TestRequireAllGrantedIsNote(t *testing.T) {
	r := Convert("Require all granted", "/")
	if len(r.Notes) == 0 || len(r.Rules) != 0 {
		t.Errorf("Require all granted is the default → note, no rule; got %+v notes=%v", r.Rules, r.Notes)
	}
}

func TestPendingCondWithoutRuleWarns(t *testing.T) {
	r := Convert("RewriteCond %{HTTP_HOST} ^example\\.com$", "/")
	if len(r.Warnings) == 0 {
		t.Errorf("a RewriteCond with no following RewriteRule should warn")
	}
}
