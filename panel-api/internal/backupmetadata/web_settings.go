package backupmetadata

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1993: a domain's web settings ride in the account backup and come back
// with the domain: its typed nginx rules and page redirects, the tenant
// directives and safe options, its environment variables, the page cache, and
// the switches its pages offer. Apply only puts them on the row; the restore
// checks (Deps.CheckDomain) then hold each one to the rules of the page that
// sets it, and drop what fails with a line in the report.

// setMetadataWebSettings copies dom's web settings into the backup's row.
func setMetadataWebSettings(dst *internalbackup.MetadataDomain, dom *models.Domain) {
	dst.NginxTenantDirectives = dom.NginxTenantDirectives
	if b, err := json.Marshal(dom.NginxSafeOptions); err == nil && string(b) != "{}" && string(b) != "null" {
		dst.NginxSafeOptions = string(b)
	}
	if len(dom.EnvVars) > 0 {
		if b, err := json.Marshal(dom.EnvVars); err == nil {
			dst.EnvVars = string(b)
		}
	}
	dst.CacheEnabled = dom.CacheEnabled
	dst.CachePath = dom.CachePath
	dst.CacheTTLSeconds = dom.CacheTTLSeconds
	dst.CacheQueryAllowlist = dom.CacheQueryAllowlist
	createWWW, webmail := dom.CreateWWW, dom.WebmailEnabled
	dst.CreateWWW = &createWWW
	dst.WebmailEnabled = &webmail
	dst.TempURLEnabled = dom.TempURLEnabled
	dst.BotChallengeExempt = dom.BotChallengeExempt
	dst.BotChallengeInclude = dom.BotChallengeInclude
	dst.AllowSubdomainDelegation = dom.AllowSubdomainDelegation
	dst.WebDisabled = dom.WebDisabled
	dst.DNSDisabled = dom.DNSDisabled
}

// setRestoredWebSettings puts the backup's web settings on row, a domain
// restored from dm, and returns a report line for each one it can't read.
// Paths in the typed rules are moved off the bundle's username onto the
// account's, as the document root is.
func setRestoredWebSettings(row *models.Domain, dm internalbackup.MetadataDomain, bundleUser, account string) []string {
	var problems []string
	if s := strings.TrimSpace(dm.NginxRules); s != "" && s != "null" {
		var rules models.NginxRules
		if err := json.Unmarshal([]byte(s), &rules); err != nil {
			problems = append(problems, fmt.Sprintf("nginx rules not restored: they can't be read (%v)", err))
		} else {
			for i := range rules {
				if rules[i].Type == "static_alias" || rules[i].Type == "media_alias" {
					// nginx's alias keeps the folder's trailing slash, which
					// rehomePath cleans away.
					t := rehomePath(rules[i].Target, "/home", bundleUser, account)
					if strings.HasSuffix(rules[i].Target, "/") && !strings.HasSuffix(t, "/") {
						t += "/"
					}
					rules[i].Target = t
				}
			}
			row.NginxRules = rules
		}
	}
	if s := strings.TrimSpace(dm.PageRedirects); s != "" && s != "null" {
		var redirects models.PageRedirects
		if err := json.Unmarshal([]byte(s), &redirects); err != nil {
			problems = append(problems, fmt.Sprintf("page redirects not restored: they can't be read (%v)", err))
		} else {
			row.PageRedirects = redirects
		}
	}
	row.NginxTenantDirectives = dm.NginxTenantDirectives
	if s := strings.TrimSpace(dm.NginxSafeOptions); s != "" {
		var opts models.NginxSafeOptions
		if err := json.Unmarshal([]byte(s), &opts); err != nil {
			problems = append(problems, fmt.Sprintf("nginx options not restored: they can't be read (%v)", err))
		} else {
			row.NginxSafeOptions = opts
		}
	}
	if s := strings.TrimSpace(dm.EnvVars); s != "" && s != "null" {
		var vars models.DomainEnvVars
		if err := json.Unmarshal([]byte(s), &vars); err != nil {
			problems = append(problems, fmt.Sprintf("environment variables not restored: they can't be read (%v)", err))
		} else {
			row.EnvVars = vars
		}
	}
	row.CacheEnabled = dm.CacheEnabled
	row.CachePath = dm.CachePath
	row.CacheTTLSeconds = dm.CacheTTLSeconds
	row.CacheQueryAllowlist = dm.CacheQueryAllowlist
	// Both default on, here and on the domain page.
	row.CreateWWW = dm.CreateWWW == nil || *dm.CreateWWW
	row.WebmailEnabled = dm.WebmailEnabled == nil || *dm.WebmailEnabled
	row.TempURLEnabled = dm.TempURLEnabled
	row.BotChallengeExempt = dm.BotChallengeExempt
	row.BotChallengeInclude = dm.BotChallengeInclude
	row.AllowSubdomainDelegation = dm.AllowSubdomainDelegation
	row.WebDisabled = dm.WebDisabled
	row.DNSDisabled = dm.DNSDisabled
	return problems
}

// hasWebSettings reports whether dm carries the web settings at all: an
// archive made before them restores each at its default, but must not
// overwrite a domain's own with those defaults. The builder always writes
// the webmail switch.
func hasWebSettings(dm internalbackup.MetadataDomain) bool { return dm.WebmailEnabled != nil }

// copyOverwritableWebSettings copies the web settings an overwrite may give an
// existing domain from src to dst.
func copyOverwritableWebSettings(dst, src *models.Domain) {
	dst.PageRedirects = src.PageRedirects
	dst.NginxTenantDirectives = src.NginxTenantDirectives
	dst.NginxSafeOptions = src.NginxSafeOptions
	dst.EnvVars = src.EnvVars
	dst.CacheEnabled, dst.CachePath = src.CacheEnabled, src.CachePath
	dst.CacheTTLSeconds, dst.CacheQueryAllowlist = src.CacheTTLSeconds, src.CacheQueryAllowlist
	dst.WebmailEnabled, dst.TempURLEnabled = src.WebmailEnabled, src.TempURLEnabled
	dst.BotChallengeExempt, dst.BotChallengeInclude = src.BotChallengeExempt, src.BotChallengeInclude
	dst.AllowSubdomainDelegation = src.AllowSubdomainDelegation
}

// overwriteDomainEnvAndCache gives existing the backup's environment
// variables and page-cache settings, each as the checks left it on probe,
// through the setters their pages use. A setting the checks changed keeps the
// domain's own.
func overwriteDomainEnvAndCache(ctx context.Context, d Deps, report func(string, ...any), existing, probe, backup *models.Domain) bool {
	changed := false
	if sameJSON(probe.EnvVars, backup.EnvVars) && !sameJSON(probe.EnvVars, existing.EnvVars) {
		if err := d.Domains.UpdateEnvVars(ctx, existing.ID, probe.EnvVars); err != nil {
			report("environment variables not updated: %v", err)
		} else {
			changed = true
		}
	}
	if probe.CacheEnabled != existing.CacheEnabled {
		if err := d.Domains.UpdateCacheEnabled(ctx, existing.ID, probe.CacheEnabled); err != nil {
			report("page cache not updated: %v", err)
		} else {
			changed = true
		}
	}
	if probe.CachePath == backup.CachePath && probe.CachePath != existing.CachePath {
		if err := d.Domains.UpdateCachePath(ctx, existing.ID, probe.CachePath); err != nil {
			report("page cache path not updated: %v", err)
		} else {
			changed = true
		}
	}
	if probe.CacheTTLSeconds == backup.CacheTTLSeconds && probe.CacheTTLSeconds != existing.CacheTTLSeconds {
		if err := d.Domains.UpdateCacheTTL(ctx, existing.ID, probe.CacheTTLSeconds); err != nil {
			report("page cache lifetime not updated: %v", err)
		} else {
			changed = true
		}
	}
	if probe.CacheQueryAllowlist == backup.CacheQueryAllowlist && probe.CacheQueryAllowlist != existing.CacheQueryAllowlist {
		if err := d.Domains.UpdateCacheQueryAllowlist(ctx, existing.ID, probe.CacheQueryAllowlist); err != nil {
			report("page cache query parameters not updated: %v", err)
		} else {
			changed = true
		}
	}
	return changed
}

// sameJSON reports whether a and b encode the same; an empty list and none
// count as the same.
func sameJSON(a, b any) bool {
	ja, ea := json.Marshal(a)
	jb, eb := json.Marshal(b)
	norm := func(j []byte) string {
		if s := string(j); s != "null" && s != "[]" && s != "{}" && s != `""` {
			return s
		}
		return ""
	}
	return ea == nil && eb == nil && norm(ja) == norm(jb)
}
