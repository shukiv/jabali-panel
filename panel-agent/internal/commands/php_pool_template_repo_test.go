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
//   - JAB-200: 32MB opcache with default max_accelerated_files saturated
//     on any real WP site (10k+ files) and opcache does not evict —
//     once full, every request recompiles.
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

	if !strings.Contains(s, "php_admin_value[opcache.memory_consumption] = 128") {
		t.Error("opcache.memory_consumption must be 128 (JAB-200 — 32 saturates on real WP sites)")
	}
	if !strings.Contains(s, "php_admin_value[opcache.max_accelerated_files] = 20000") {
		t.Error("opcache.max_accelerated_files must be raised above one site's file count (JAB-200)")
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
