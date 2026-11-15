package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// docker_app_logrotate.go — host logrotate snippets for docker-app persistent
// log volumes (JAB-121).
//
// Several catalog apps bind an internal log directory into the app's DataRoot
// (flarum storage/logs, erpnext frappe-bench/logs, onlyoffice, plausible
// clickhouse-server, …). Those file logs bypass Docker's journald log driver
// AND the static jabali logrotate drop-in (install/logrotate/jabali), so a busy
// app grows tenant disk without bound. For every volume the catalog marks with
// a `rotate:` policy, the install handler drops a logrotate snippet here; the
// delete handler removes it.

// dockerAppLogrotateDir is the host logrotate.d directory. The agent runs as
// root, so it can write here directly.
const dockerAppLogrotateDir = "/etc/logrotate.d"

// dockerAppLogrotatePath returns the snippet path for one app slug. Callers
// validate the slug against dockerAppSlugRE before using it in a filename.
func dockerAppLogrotatePath(slug string) string {
	return filepath.Join(dockerAppLogrotateDir, "jabali-dockerapp-"+slug)
}

// buildDockerAppLogrotate renders the logrotate config body covering every log
// volume of one docker app. dataDir is the app's DataRoot
// (/var/lib/jabali/docker-apps/<slug>); each volume's file logs live at
// <dataDir>/<volume>/*.log. Pure (no I/O) so it is unit-testable.
//
// Defaults: 14 days retention, copytruncate (safe while a container holds the
// file open — no reload needed), daily + compress. A per-volume max_size adds
// a size trigger; retention_days/mode override the defaults.
func buildDockerAppLogrotate(slug, dataDir string, specs []dockerAppLogRotate) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Managed by jabali (JAB-121) — docker-app %q file logs.\n", slug)
	b.WriteString("# Regenerated on every install; manual edits are overwritten.\n")
	for _, s := range specs {
		if !dockerAppSlugRE.MatchString(s.Volume) {
			continue // defensive: never build a glob from an unvalidated name
		}
		days := s.RetentionDays
		if days <= 0 {
			days = 14
		}
		mode := s.Mode
		if mode != "create" {
			mode = "copytruncate"
		}
		glob := filepath.Join(dataDir, s.Volume, "*.log")
		fmt.Fprintf(&b, "\n%s {\n", glob)
		b.WriteString("    daily\n")
		fmt.Fprintf(&b, "    rotate %d\n", days)
		if s.MaxSize != "" {
			fmt.Fprintf(&b, "    maxsize %s\n", s.MaxSize)
		}
		b.WriteString("    missingok\n")
		b.WriteString("    notifempty\n")
		b.WriteString("    compress\n")
		b.WriteString("    delaycompress\n")
		b.WriteString("    " + mode + "\n")
		b.WriteString("    su root root\n")
		b.WriteString("}\n")
	}
	return b.String()
}

// writeDockerAppLogrotate writes (or replaces) the app's logrotate snippet. No
// usable specs → the snippet is removed (an app that dropped its log volumes
// must not leave a stale config that logrotate keeps parsing).
func writeDockerAppLogrotate(slug, dataDir string, specs []dockerAppLogRotate) error {
	if !dockerAppSlugRE.MatchString(slug) {
		return fmt.Errorf("invalid slug %q", slug)
	}
	// Count volumes that would actually render (valid name).
	valid := 0
	for _, s := range specs {
		if dockerAppSlugRE.MatchString(s.Volume) {
			valid++
		}
	}
	if valid == 0 {
		// No usable specs → leave any existing snippet ALONE. Removal is the
		// delete handler's job (removeDockerAppLogrotate); a minimal-payload
		// install or a logrotate_ensure with empty specs (recovery) must never
		// nuke a snippet a prior real install wrote (JAB-121 phase 2).
		return nil
	}
	// logrotate parses every file in /etc/logrotate.d; write directly (0644
	// root:root). The file is tiny and rewritten on each install, so a torn
	// write self-heals on the next run — a temp+rename here would risk
	// logrotate parsing the leftover temp (its default tabooext excludes .tmp).
	if err := os.WriteFile(dockerAppLogrotatePath(slug), []byte(buildDockerAppLogrotate(slug, dataDir, specs)), 0o644); err != nil {
		return fmt.Errorf("write logrotate snippet: %w", err)
	}
	return nil
}

// removeDockerAppLogrotate deletes the app's snippet. Best-effort: a missing
// file is not an error.
func removeDockerAppLogrotate(slug string) {
	if !dockerAppSlugRE.MatchString(slug) {
		return
	}
	_ = os.Remove(dockerAppLogrotatePath(slug))
}
