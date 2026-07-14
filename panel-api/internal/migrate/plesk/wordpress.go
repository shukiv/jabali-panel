package plesk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
)

// wordpress.go — WordPress detection for the Plesk importer.
//
// Primary source: `plesk ext wp-toolkit --list -format json`, which
// enumerates every WordPress instance on the server (columns: ID, Main
// Domain ID, Main Domain's Path, Owner ID, Alive, Site URL, Name,
// Version). We filter to the account's own domains and record each as an
// AppSpec so the operator + the (later) restore step know what to move.
//
// Fallback: when WP Toolkit isn't installed the CLI errors — we then scan
// each domain's docroot for wp-config.php (mirrors migrate/wordpressssh's
// filesystem detection).
//
// STATUS: JSON key spellings vary across Plesk / WP Toolkit versions, so
// the parser tries several spellings and degrades to the filesystem scan;
// not yet validated against a live host.

// describeWordPress returns the WordPress installs that belong to the
// account's domains. Best-effort: a probe failure warns and falls back to
// the docroot scan rather than failing the describe.
func (d *Discoverer) describeWordPress(ctx context.Context, s *session, domains []migrate.DomainSpec) ([]migrate.AppSpec, []migrate.Warning) {
	warns := []migrate.Warning{}
	owned := map[string]migrate.DomainSpec{}
	for _, dom := range domains {
		owned[strings.ToLower(dom.Name)] = dom
	}

	out, err := s.run(ctx, d.CommandTimeout, "plesk ext wp-toolkit --list -format json")
	if err != nil {
		warns = append(warns, migrate.Warning{
			Code:   "wp_toolkit_unavailable",
			Detail: fmt.Sprintf("wp-toolkit --list failed (%v); falling back to a docroot wp-config.php scan", err),
		})
		return d.scanWordPress(ctx, s, domains), warns
	}

	instances, perr := parseWPToolkitList(string(out))
	if perr != nil {
		warns = append(warns, migrate.Warning{
			Code:   "wp_toolkit_parse_failed",
			Detail: fmt.Sprintf("could not parse wp-toolkit JSON (%v); falling back to a docroot scan", perr),
		})
		return d.scanWordPress(ctx, s, domains), warns
	}

	apps := []migrate.AppSpec{}
	for _, inst := range instances {
		host := strings.ToLower(inst.domainHost())
		dom, ok := owned[host]
		if !ok {
			continue // instance belongs to another account
		}
		apps = append(apps, migrate.AppSpec{
			Kind:    "wordpress",
			Path:    joinDocrootPath(dom, inst.Path),
			Version: inst.Version,
		})
	}
	return apps, warns
}

// scanWordPress is the fallback: test each domain docroot for
// wp-config.php. Version is left blank (no cheap way to read it here).
func (d *Discoverer) scanWordPress(ctx context.Context, s *session, domains []migrate.DomainSpec) []migrate.AppSpec {
	apps := []migrate.AppSpec{}
	for _, dom := range domains {
		cmd := fmt.Sprintf("test -f %s/wp-config.php && echo found", shellQuote(dom.DocRoot))
		out, err := s.run(ctx, d.CommandTimeout, cmd)
		if err != nil || !strings.Contains(string(out), "found") {
			continue
		}
		apps = append(apps, migrate.AppSpec{Kind: "wordpress", Path: dom.DocRoot})
	}
	return apps
}

// wpInstance is the subset of a WP Toolkit instance we need, normalised
// across the key spellings different Plesk versions emit.
type wpInstance struct {
	MainDomain string
	SiteURL    string
	Path       string
	Version    string
}

// domainHost resolves the instance's domain from its main-domain field,
// falling back to the host parsed out of the site URL.
func (w wpInstance) domainHost() string {
	if w.MainDomain != "" {
		return w.MainDomain
	}
	if w.SiteURL != "" {
		if u, err := url.Parse(w.SiteURL); err == nil && u.Host != "" {
			return u.Host
		}
		// bare host without scheme
		return strings.SplitN(w.SiteURL, "/", 2)[0]
	}
	return ""
}

// parseWPToolkitList parses `wp-toolkit --list -format json`. The output
// is either a bare array or an object with an "instances"/"data" array.
// Keys vary by version, so each field is pulled with a fallback set.
func parseWPToolkitList(raw string) ([]wpInstance, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
		// try {"instances":[...]} / {"data":[...]} wrappers
		var wrap map[string]json.RawMessage
		if werr := json.Unmarshal([]byte(trimmed), &wrap); werr != nil {
			return nil, err
		}
		var inner json.RawMessage
		for _, k := range []string{"instances", "data", "list"} {
			if v, ok := wrap[k]; ok {
				inner = v
				break
			}
		}
		if inner == nil {
			return nil, err
		}
		if uerr := json.Unmarshal(inner, &rows); uerr != nil {
			return nil, uerr
		}
	}

	out := make([]wpInstance, 0, len(rows))
	for _, r := range rows {
		out = append(out, wpInstance{
			MainDomain: strFromKeys(r, "mainDomain", "main_domain", "domainName", "domain"),
			SiteURL:    strFromKeys(r, "siteUrl", "site_url", "url"),
			Path:       strFromKeys(r, "path", "mainDomainPath", "main_domain_path", "mainDomainsPath"),
			Version:    strFromKeys(r, "version", "wpVersion", "wp_version"),
		})
	}
	return out, nil
}

// strFromKeys returns the first key present as a non-empty string.
func strFromKeys(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

// joinDocrootPath turns a WP Toolkit "Main Domain's Path" (e.g.
// "/httpdocs/wordpress", relative to the vhost root) into an absolute
// filesystem path. When the path already sits under the domain's docroot
// (or is already absolute) we don't double it.
func joinDocrootPath(dom migrate.DomainSpec, wpPath string) string {
	wpPath = strings.TrimSpace(wpPath)
	if wpPath == "" {
		return dom.DocRoot
	}
	if strings.HasPrefix(wpPath, "/var/") {
		return wpPath // already absolute
	}
	// vhost root is the docroot with a trailing "/httpdocs" removed.
	vhostRoot := strings.TrimSuffix(dom.DocRoot, "/httpdocs")
	return path.Clean(vhostRoot + "/" + strings.TrimPrefix(wpPath, "/"))
}
