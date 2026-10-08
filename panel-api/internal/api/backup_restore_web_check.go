package api

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/phpenv"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// restoredCachePathRe is a page-cache path as the cache switch stores it: "/"
// or an install's subfolder. The agent renders it into a vhost regex.
var restoredCachePathRe = regexp.MustCompile(`^/([A-Za-z0-9._~-]+(/[A-Za-z0-9._~-]+)*)?$`)

// dropRestoredWebSettings holds a restored domain's web settings to the rules
// of the page that sets each one, clears what fails, and returns one warning
// for each (GH #1993).
func dropRestoredWebSettings(ctx context.Context, row *models.Domain, ownerUsername string,
	settings domainops.MailSettingsReader, previews domainops.PreviewDomainLister, source RestoreSource) []string {
	var warnings []string
	if source == RestoreFromUpload {
		warnings = append(warnings, dropUnlessOwnersSetDomainOptions(ctx, row, settings)...)
	}
	warnings = append(warnings, dropRestoredNginxRules(row, ownerUsername, source)...)
	warnings = append(warnings, dropRestoredPageRedirects(row)...)

	if row.NginxTenantDirectives != nil && strings.TrimSpace(*row.NginxTenantDirectives) != "" {
		if msg := ValidateNginxDirectivesTenant(*row.NginxTenantDirectives); msg != "" {
			row.NginxTenantDirectives = nil
			warnings = append(warnings, "advanced nginx directives dropped: "+msg)
		}
	}
	if err := row.NginxSafeOptions.Validate(); err != nil {
		row.NginxSafeOptions = models.NginxSafeOptions{}
		warnings = append(warnings, "nginx options dropped: "+err.Error())
	}
	warnings = append(warnings, dropRestoredEnvVars(row)...)
	warnings = append(warnings, dropRestoredCacheSettings(row)...)

	if row.BotChallengeExempt && source == RestoreFromUpload {
		row.BotChallengeExempt = false
		warnings = append(warnings, "bot challenge opt-out not restored: only an administrator can set it, and an uploaded backup doesn't restore it")
	}
	if row.TempURLEnabled {
		switch other, err := previewConflict(ctx, previews, row); {
		case err != nil:
			row.TempURLEnabled = false
			warnings = append(warnings, fmt.Sprintf("preview URL left off: %v", err))
		case other != "":
			row.TempURLEnabled = false
			warnings = append(warnings, "preview URL left off: it would collide with "+other)
		}
	}
	return warnings
}

// dropUnlessOwnersSetDomainOptions clears the typed nginx rules, nginx
// options and advanced directives an uploaded file brings while the server
// doesn't let an account's owner set them (tenant_domain_options_enabled),
// as their pages would. A server whose settings can't be read restores none.
func dropUnlessOwnersSetDomainOptions(ctx context.Context, row *models.Domain, settings domainops.MailSettingsReader) []string {
	has := len(row.NginxRules) > 0 || row.NginxSafeOptions != (models.NginxSafeOptions{}) ||
		(row.NginxTenantDirectives != nil && strings.TrimSpace(*row.NginxTenantDirectives) != "")
	if !has {
		return nil
	}
	reason := "this server doesn't let account owners set them"
	if settings != nil {
		st, err := settings.Get(ctx)
		switch {
		case err != nil:
			reason = fmt.Sprintf("the server settings could not be read (%v)", err)
		case st != nil && st.TenantDomainOptionsEnabled:
			return nil
		}
	}
	row.NginxRules = nil
	row.NginxSafeOptions = models.NginxSafeOptions{}
	row.NginxTenantDirectives = nil
	return []string{"nginx rules, nginx options and advanced directives not restored from an uploaded backup: " + reason + "; review them and add them again in the domain's settings"}
}

func previewConflict(ctx context.Context, previews domainops.PreviewDomainLister, row *models.Domain) (string, error) {
	if previews == nil {
		return "", fmt.Errorf("the preview URL check is not wired")
	}
	return domainops.PreviewSlugConflict(ctx, previews, row.Name, row.ID)
}

// dropRestoredNginxRules keeps each typed nginx rule the domain's rule
// builder would accept, in order. An uploaded file keeps only the types a
// tenant may set (validateTenantNginxRules); an alias folder must sit in the
// account's own home.
func dropRestoredNginxRules(row *models.Domain, ownerUsername string, source RestoreSource) []string {
	if len(row.NginxRules) == 0 {
		return nil
	}
	var warnings []string
	home := "/home/" + ownerUsername + "/"
	kept := models.NginxRules{}
	frontController := false
	for i, rule := range row.NginxRules {
		label := fmt.Sprintf("nginx rule %d (%s) dropped", i+1, rule.Type)
		one := models.NginxRules{rule}
		var err error
		if source == RestoreFromUpload {
			if _, ok := tenantSafeNginxRuleTypes[rule.Type]; !ok && isValidNginxRuleType(rule.Type) {
				warnings = append(warnings, label+": only an administrator can add this kind of rule, and an uploaded backup doesn't restore it; review it and add it again in the domain's settings")
				continue
			}
			err = validateTenantNginxRules(one)
		} else {
			err = validateNginxRules(one)
		}
		switch {
		case err != nil:
			warnings = append(warnings, label+": "+strings.TrimPrefix(err.Error(), "rule 0: "))
			continue
		case (rule.Type == "static_alias" || rule.Type == "media_alias") && !strings.HasPrefix(path.Clean(rule.Target)+"/", home):
			warnings = append(warnings, fmt.Sprintf("%s: its folder %s is outside the account's home", label, rule.Target))
			continue
		case rule.Type == "front_controller" && frontController:
			warnings = append(warnings, label+": a domain can have only one front_controller rule")
			continue
		case len(kept) == maxNginxRules:
			warnings = append(warnings, fmt.Sprintf("nginx rules after rule %d dropped: a domain can have at most %d", i, maxNginxRules))
			row.NginxRules = kept
			return warnings
		}
		if rule.Type == "front_controller" {
			frontController = true
		}
		kept = append(kept, rule)
	}
	row.NginxRules = kept
	return warnings
}

// maxPageRedirects is validatePageRedirects' cap.
const maxPageRedirects = 100

// dropRestoredPageRedirects keeps each page redirect the redirects page would
// accept, in order.
func dropRestoredPageRedirects(row *models.Domain) []string {
	if len(row.PageRedirects) == 0 {
		return nil
	}
	var warnings []string
	kept := models.PageRedirects{}
	for i, pr := range row.PageRedirects {
		if len(kept) == maxPageRedirects {
			warnings = append(warnings, fmt.Sprintf("page redirects after redirect %d dropped: a domain can have at most %d", i, maxPageRedirects))
			break
		}
		if err := validatePageRedirects(models.PageRedirects{pr}); err != nil {
			warnings = append(warnings, fmt.Sprintf("page redirect %d (%s) dropped: %s", i+1, pr.Source, strings.TrimPrefix(err.Error(), "entry 0: ")))
			continue
		}
		kept = append(kept, pr)
	}
	row.PageRedirects = kept
	return warnings
}

// dropRestoredEnvVars keeps each environment variable the domain's
// environment page would accept.
func dropRestoredEnvVars(row *models.Domain) []string {
	if len(row.EnvVars) == 0 {
		return nil
	}
	var warnings []string
	seen := map[string]bool{}
	kept := models.DomainEnvVars{}
	for _, kv := range row.EnvVars {
		var err error
		switch {
		case len(kept) == phpenv.MaxVars:
			err = fmt.Errorf("a domain can have at most %d", phpenv.MaxVars)
		case seen[kv.Key]:
			err = fmt.Errorf("it is set twice")
		default:
			if err = phpenv.ValidKey(kv.Key); err == nil {
				err = phpenv.ValidValue(kv.Value)
			}
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("environment variable %q dropped: %v", kv.Key, err))
			continue
		}
		seen[kv.Key] = true
		kept = append(kept, kv)
	}
	row.EnvVars = kept
	return warnings
}

// dropRestoredCacheSettings brings the page-cache settings into the ranges
// the cache page stores.
func dropRestoredCacheSettings(row *models.Domain) []string {
	var warnings []string
	switch ttl := row.CacheTTLSeconds; {
	case ttl <= 0:
		row.CacheTTLSeconds = 600
	case ttl < 10:
		row.CacheTTLSeconds = 10
	case ttl > 86400:
		row.CacheTTLSeconds = 86400
	}
	if p := strings.TrimSpace(row.CachePath); p == "" {
		row.CachePath = "/"
	} else if !restoredCachePathRe.MatchString(p) || strings.Contains(p, "..") {
		row.CachePath = "/"
		warnings = append(warnings, fmt.Sprintf("page cache path %q dropped: it is not a folder path; the cache covers the whole site", p))
	}
	if row.CacheQueryAllowlist != "" {
		csv, err := normalizeCacheQueryAllowlist(splitAllowlistCSV(row.CacheQueryAllowlist))
		if err != nil {
			row.CacheQueryAllowlist = ""
			warnings = append(warnings, "page cache query parameters dropped: "+err.Error())
		} else {
			row.CacheQueryAllowlist = csv
		}
	}
	return warnings
}
