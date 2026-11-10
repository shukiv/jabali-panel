package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
)

// Pins the load-bearing lines of the REPO pool template (the box copy at
// /etc/jabali-panel/php-pool.conf.tmpl is installed from it on every
// `jabali update`). Two regressions this guards:
//
//   - JAB-199: /run/jabali-wp-purge missing from open_basedir made the
//     jabali-cache purge spool invisible from the FPM jail — every WP
//     content-edit purge of the nginx micro-cache was a silent no-op
//     fleet-wide.
//   - GH #2466: OPcache sizing in the pool (php_admin_value) never took
//     effect — the master allocates it at startup, before it reads the
//     pool — so the template must not carry it. The size comes from the
//     server-wide 15-jabali-opcache.ini; a pool override goes into the
//     slug's jabali-ext ini (applyPoolOpcacheIni).
func TestRepoPoolTemplatePinsJailAndOpcache(t *testing.T) {
	path := filepath.Join("..", "..", "..", "install", "php", "jabali-php-pool.conf.tmpl")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read repo pool template: %v", err)
	}
	s := string(b)

	base := ""
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "php_admin_value[open_basedir]") {
			base = line
			break
		}
	}
	if base == "" {
		t.Fatal("template has no open_basedir line — the jail is the whole tenant-isolation posture")
	}
	if !strings.Contains(base, ":/run/jabali-wp-purge") {
		t.Errorf("open_basedir must include the WP purge spool (JAB-199): %s", base)
	}
	if !strings.Contains(base, "/home/{{.User}}") {
		t.Errorf("open_basedir must stay anchored to the tenant home: %s", base)
	}

	for d := range opcacheStartupDirectives {
		if strings.Contains(s, "php_admin_value["+d+"]") {
			t.Errorf("the pool template sets %s, which the master only reads at startup (GH #2466)", d)
		}
	}

	// JAB-230: without a sendmail_path every PHP mail()/wp_mail() call on the
	// box fails (install.sh purges all MTAs). php_admin_value so tenants
	// cannot repoint the exec path.
	if !strings.Contains(s, "php_admin_value[sendmail_path] = /usr/local/libexec/jabali/jabali-sendmail -t -i") {
		t.Error("sendmail_path must point at the jabali-sendmail shim (JAB-230)")
	}
}

// TestPoolTemplateSlowlog renders the repo template and pins the GH #1332 item 12
// slow-log block: emitted (with the agent-derived path) only when the threshold
// is > 0, absent when disabled.
func TestPoolTemplateSlowlog(t *testing.T) {
	path := filepath.Join("..", "..", "..", "install", "php", "jabali-php-pool.conf.tmpl")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read repo pool template: %v", err)
	}
	tmpl, err := template.New("pool").Parse(string(b))
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	render := func(spec phpPoolSpecTemplate) string {
		var sb strings.Builder
		if err := tmpl.Execute(&sb, spec); err != nil {
			t.Fatalf("execute template: %v", err)
		}
		return sb.String()
	}

	base := phpPoolSpecTemplate{
		PoolName: "jabali-alice", User: "alice", Group: "alice",
		SocketPath: "/run/php/jabali-alice/fpm.sock", PmMode: "static",
		PmMaxChildren: 5, ProcessIdleTimeoutSeconds: 60,
		DisableFunctions: "exec",
	}

	on := base
	on.SlowlogTimeoutSeconds = 5
	on.SlowlogPath = "/home/alice/logs/php-slow.log"
	got := render(on)
	if !strings.Contains(got, "request_slowlog_timeout = 5s") {
		t.Errorf("enabled: missing request_slowlog_timeout:\n%s", got)
	}
	if !strings.Contains(got, "slowlog = /home/alice/logs/php-slow.log") {
		t.Errorf("enabled: missing slowlog path:\n%s", got)
	}

	off := render(base) // SlowlogTimeoutSeconds == 0
	if strings.Contains(off, "slowlog") || strings.Contains(off, "request_slowlog_timeout") {
		t.Errorf("disabled: slow-log lines must be absent:\n%s", off)
	}
}

// TestPoolTemplateSessionUploadProgressOff pins session upload progress off in
// every tenant pool (CVE-2026-115560: php.net's interim advice, 2026-10-23).
// php_admin_flag, so a tenant's .user.ini or ini_set cannot turn it back on,
// and the directive stays out of adminFlagAllowlist so no pool override can.
func TestPoolTemplateSessionUploadProgressOff(t *testing.T) {
	path := filepath.Join("..", "..", "..", "install", "php", "jabali-php-pool.conf.tmpl")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read repo pool template: %v", err)
	}
	tmpl, err := template.New("pool").Parse(string(b))
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	var sb strings.Builder
	if err := tmpl.Execute(&sb, phpPoolSpecTemplate{
		PoolName: "jabali-alice", User: "alice", Group: "alice",
		SocketPath: "/run/php/jabali-alice/fpm.sock", PmMode: "static",
		PmMaxChildren: 5, ProcessIdleTimeoutSeconds: 60,
		DisableFunctions: "exec",
	}); err != nil {
		t.Fatalf("execute template: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(sb.String(), "\n") {
		if strings.Contains(line, "session.upload_progress.enabled") && !strings.HasPrefix(line, ";") {
			lines = append(lines, line)
		}
	}
	if len(lines) != 1 || lines[0] != "php_admin_flag[session.upload_progress.enabled] = off" {
		t.Errorf("rendered pool must set exactly php_admin_flag[session.upload_progress.enabled] = off, got %q", lines)
	}
	if adminFlagAllowlist["session.upload_progress.enabled"] {
		t.Error("session.upload_progress.enabled must not be in adminFlagAllowlist: a pool override would turn it back on")
	}
}
