package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
