package plesk

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
)

// areas.go — per-area manifest builders for the Plesk importer.
//
// STATUS: coded against the Plesk Obsidian CLI docs + the documented
// vhost layout (`/var/www/vhosts/<domain>/httpdocs`, HTTPD_VHOSTS_D in
// /etc/psa/psa.conf). NOT yet validated against a live Plesk host — each
// builder is best-effort and records a manifest Warning on partial /
// missing data rather than failing the whole describe (mirrors the
// directadmin builders' incremental cadence). DNS zones, cron, and SSH
// keys are deferred to a follow-up step behind explicit warnings rather
// than shipping unvalidated parsers.

const pleskVhostRoot = "/var/www/vhosts"

// describeDomains enumerates a subscription's domains. Best path:
// `plesk bin subscription --info <name>` exposes the domain set; when
// that field can't be parsed we fall back to the subscription name as
// the sole (primary) domain. Per-domain docroot + PHP come from
// `plesk bin domain --info <domain>`; docroot defaults to the standard
// Plesk vhost path.
func (d *Discoverer) describeDomains(ctx context.Context, s *session, account string) ([]migrate.DomainSpec, error) {
	domains := []string{account}
	if out, err := s.run(ctx, d.CommandTimeout, "plesk bin subscription --info "+shellQuote(account)); err == nil {
		info := parsePleskInfo(string(out))
		if v := firstNonEmpty(info["domains"], info["domain aliases"]); v != "" {
			if parsed := splitCSV(v); len(parsed) > 0 {
				domains = parsed
			}
		}
	}

	rows := make([]migrate.DomainSpec, 0, len(domains))
	for _, dom := range domains {
		spec := migrate.DomainSpec{
			Name:      dom,
			DocRoot:   fmt.Sprintf("%s/%s/httpdocs", pleskVhostRoot, dom),
			IsPrimary: dom == account,
			HasPHP:    true,
		}
		if out, err := s.run(ctx, d.CommandTimeout, "plesk bin domain --info "+shellQuote(dom)); err == nil {
			di := parsePleskInfo(string(out))
			if v := firstNonEmpty(di["php version"], di["php_version"]); v != "" {
				spec.PHPVer = v
			}
			if v := firstNonEmpty(di["www-root"], di["www_root"], di["document root"]); v != "" {
				spec.DocRoot = v
			}
			if v := di["php"]; v != "" {
				spec.HasPHP = truthy(v)
			}
		}
		rows = append(rows, spec)
	}
	return rows, nil
}

// describeDatabases lists the MySQL databases for each domain via
// `plesk bin database --list -domain <domain>` (one database name per
// line). Postgres databases are recorded with a warning (v1 restore is
// MySQL-only, same as the other importers). A per-domain failure warns
// and continues rather than aborting the whole account.
func (d *Discoverer) describeDatabases(ctx context.Context, s *session, domains []migrate.DomainSpec) ([]migrate.DatabaseSpec, []migrate.Warning, error) {
	rows := []migrate.DatabaseSpec{}
	warns := []migrate.Warning{}
	for _, dom := range domains {
		out, err := s.run(ctx, d.CommandTimeout, "plesk bin database --list -domain "+shellQuote(dom.Name))
		if err != nil {
			warns = append(warns, migrate.Warning{
				Code:   "databases_domain_failed",
				Detail: fmt.Sprintf("%s: %v", dom.Name, err),
			})
			continue
		}
		for _, name := range splitLines(string(out)) {
			rows = append(rows, migrate.DatabaseSpec{
				Engine:    "mysql",
				Name:      name,
				GrantUser: "",
			})
		}
	}
	return rows, warns, nil
}

// AccountSize implements migrate.SizeProber: best-effort byte count of
// the subscription's vhost tree via `du -sb`. Returns 0 (not an error)
// when the path can't be read so the lazy size endpoint degrades to
// "unknown" rather than 500ing.
func (d *Discoverer) AccountSize(ctx context.Context, raw migrate.Session, login string) (int64, error) {
	s, ok := raw.(*session)
	if !ok {
		return 0, errors.New("AccountSize: wrong session type")
	}
	cmd := fmt.Sprintf("du -sb %s/%s 2>/dev/null | cut -f1", pleskVhostRoot, shellQuote(login))
	out, err := s.run(ctx, d.CommandTimeout, cmd)
	if err != nil {
		return 0, nil // best-effort; caller renders "unknown"
	}
	// First whitespace field: robust whether or not the shell `cut -f1`
	// already collapsed the `du` output to bare bytes.
	return parseFirstInt(string(out)), nil
}

// --- small helpers ---

// shellQuote single-quotes an argument for safe SSH command
// interpolation. Untrusted source-side names never reach the shell
// unquoted.
func shellQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

func splitLines(s string) []string {
	out := []string{}
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		out = append(out, t)
	}
	return out
}

func splitCSV(s string) []string {
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		t := strings.TrimSpace(part)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "yes", "true", "enabled", "1":
		return true
	default:
		return false
	}
}
