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
	// All domains of the subscription: the primary (name=account) plus any
	// domain whose webspace_id points at the primary's id (addon domains).
	// The `plesk bin subscription/domain --info` output does NOT list the
	// domain set, so this comes from the psa `domains` table.
	domains := []string{account}
	sql := fmt.Sprintf(
		"SELECT name FROM domains WHERE name=%s OR webspace_id=(SELECT id FROM domains WHERE name=%s LIMIT 1)",
		sqlQuote(account), sqlQuote(account))
	if out, err := s.run(ctx, d.CommandTimeout, "plesk db -Ne "+shellQuote(sql)); err == nil {
		if parsed := splitLines(string(out)); len(parsed) > 0 {
			seen := map[string]bool{}
			domains = domains[:0]
			for _, n := range parsed {
				if !seen[n] {
					seen[n] = true
					domains = append(domains, n)
				}
			}
			if !seen[account] {
				domains = append([]string{account}, domains...)
			}
		}
	}

	// GH #429: read each domain's REAL docroot from psa `hosting.www_root`
	// instead of assuming /var/www/vhosts/<dom>/httpdocs. Plesk keeps a whole
	// subscription under ONE vhost dir keyed by the main domain, so a
	// subdomain/addon has a custom root (e.g. .../<main>/site1) and a no-hosting
	// (mail/parked) domain has none. The old assumption pointed the rsync at an
	// empty/nonexistent path. The JOIN yields no row for a no-hosting domain, so
	// it simply has no docroot. (Validated on Plesk Obsidian 18.0.79.)
	docroots := pleskDocroots(ctx, d, s, domains)

	rows := make([]migrate.DomainSpec, 0, len(domains))
	for _, dom := range domains {
		root := docroots[dom]
		spec := migrate.DomainSpec{
			Name:      dom,
			DocRoot:   root,       // "" when the domain has no physical hosting
			IsPrimary: dom == account,
			HasPHP:    root != "", // no hosting => no PHP; refined below
		}
		if out, err := s.run(ctx, d.CommandTimeout, "plesk bin domain --info "+shellQuote(dom)); err == nil {
			di := parsePleskInfo(string(out))
			// Real Plesk exposes "PHP support: Yes" (not "php"); PHP version
			// isn't in this block.
			if v := firstNonEmpty(di["php support"], di["php"]); v != "" {
				spec.HasPHP = truthy(v)
			}
			if v := firstNonEmpty(di["php version"], di["php_version"]); v != "" {
				spec.PHPVer = v
			}
		}
		rows = append(rows, spec)
	}
	return rows, nil
}

// pleskDocroots batch-reads the real docroot for each domain from psa
// `hosting.www_root` (GH #429). Domains with no hosting are absent from the
// result. Best-effort: a query failure yields an empty map and the caller
// records empty docroots (better than a wrong path).
func pleskDocroots(ctx context.Context, d *Discoverer, s *session, domains []string) map[string]string {
	out := map[string]string{}
	if len(domains) == 0 {
		return out
	}
	quoted := make([]string, len(domains))
	for i, dn := range domains {
		quoted[i] = sqlQuote(dn)
	}
	sql := "SELECT d.name, h.www_root FROM domains d JOIN hosting h ON d.id=h.dom_id WHERE d.name IN (" +
		strings.Join(quoted, ",") + ")"
	raw, err := s.run(ctx, d.CommandTimeout, "plesk db -Ne "+shellQuote(sql))
	if err != nil {
		return out
	}
	for _, ln := range splitLines(string(raw)) {
		parts := strings.SplitN(ln, "\t", 2)
		if len(parts) == 2 {
			name, root := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
			if name != "" && root != "" && root != "NULL" {
				out[name] = root
			}
		}
	}
	return out
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
		// `plesk bin database` has no list verb; enumerate via the psa DB.
		sql := fmt.Sprintf(
			"SELECT db.name FROM data_bases db JOIN domains d ON db.dom_id=d.id WHERE d.name=%s",
			sqlQuote(dom.Name))
		out, err := s.run(ctx, d.CommandTimeout, "plesk db -Ne "+shellQuote(sql))
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
