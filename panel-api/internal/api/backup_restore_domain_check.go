package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/phpbasedir"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnscompile"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// errRestoreChecksUnwired refuses a restored domain when a store the checks
// need is missing: an unwired alias or settings store would make its guard
// find no conflict, and the restore would claim a check it never ran.
var errRestoreChecksUnwired = errors.New("the restore domain checks are not fully wired on this door")

// RestoreDomainCheck returns the backupmetadata.Deps.CheckDomain hook (GH
// #1898). A backup archive may come from an untrusted source, so a restored
// domain goes through the checks its create and update doors run:
//
//   - The name guards (domainops.CheckName) run with the TENANT rules. The
//     admin chose to restore, but the names come from the archive, so the
//     cross-tenant guard applies: a name nested under another owner's domain
//     is refused unless that domain allows delegation (#1812).
//   - The document root must sit under the owner's home (the admin rule, since
//     the original may have been set by an admin).
//
// Either failure refuses the domain. Two optional vhost fields are dropped
// instead, with a warning, and the domain is restored without them:
//
//   - raw nginx directives that fail the admin directive validator;
//   - a redirect-all target or type that the update door would refuse (both
//     are dropped together, so a type is never left without its target).
//   - a per-domain PHP setting the PHP settings page would refuse (the agent
//     renders them into the site's web server config). open_basedir from an
//     uploaded file may list only paths inside the owner's home.
//   - an index priority the domain page doesn't offer (GH #1993); the domain
//     then gets the default.
//   - a mail setting the domain page would refuse (GH #1993): an unknown mail
//     provider, a malformed provider DKIM token, DMARC np tag or CalDAV/CardDAV
//     host. A domain with the custom (DNS template) posture doesn't get Jabali
//     mail, as the domain's mail page refuses it.
//
// The domain's web settings (GH #1993) are held to the rules of the page that
// sets each one, and what fails is dropped with a warning. From an uploaded
// file (RestoreFromUpload) only what the account's owner could set comes
// back: an admin-only nginx rule type or the bot-challenge opt-out is left
// for the administrator to review and add again, and so are the typed rules,
// nginx options and advanced directives while the server doesn't let owners
// set those (tenant_domain_options_enabled).
func RestoreDomainCheck(domains domainops.SuffixDomainFinder, aliases domainops.AliasHostnameFinder,
	settings domainops.MailSettingsReader, previews domainops.PreviewDomainLister,
	source RestoreSource) func(ctx context.Context, row *models.Domain, ownerUsername string) ([]string, error) {
	return func(ctx context.Context, row *models.Domain, ownerUsername string) ([]string, error) {
		if domains == nil || aliases == nil || settings == nil {
			return nil, errRestoreChecksUnwired
		}
		if err := domainops.CheckName(ctx, domainops.NameCheckDeps{Domains: domains, Aliases: aliases, Settings: settings},
			row.Name, row.UserID, false); err != nil {
			return nil, err
		}
		if strings.TrimSpace(ownerUsername) == "" {
			return nil, errors.New("the archive names no owner username, so the document root cannot be checked")
		}
		if err := domainops.ValidateDocumentRoot(row.DocRoot, ownerUsername, row.Name); err != nil {
			return nil, fmt.Errorf("document root %q: %w", row.DocRoot, err)
		}

		var warnings []string
		if row.NginxCustomDirectives != nil && strings.TrimSpace(*row.NginxCustomDirectives) != "" {
			if msg := ValidateNginxDirectivesAdmin(*row.NginxCustomDirectives); msg != "" {
				row.NginxCustomDirectives = nil
				warnings = append(warnings, "custom nginx directives dropped: "+msg)
			}
		}
		if reason := restoredRedirectProblem(row); reason != "" {
			row.RedirectAllTo = nil
			row.RedirectAllType = nil
			warnings = append(warnings, "redirect-all dropped: "+reason)
		}
		if p := strings.TrimSpace(row.IndexPriority); p != "" && !IsValidIndexPriority(p) {
			row.IndexPriority = ""
			warnings = append(warnings, fmt.Sprintf("index priority %q dropped: it is not one the domain page offers", p))
		} else {
			row.IndexPriority = p
		}
		warnings = append(warnings, dropRestoredPHPSettings(row, ownerUsername, source)...)
		warnings = append(warnings, dropRestoredMailSettings(row)...)
		warnings = append(warnings, dropRestoredWebSettings(ctx, row, ownerUsername, settings, previews, source)...)
		return warnings, nil
	}
}

// RestoreAliasCheck returns the backupmetadata.Deps.CheckAlias hook (GH
// #1993): a web domain alias the archive brings goes through the alias page's
// hostname rule (GH #1625) for the domain it is restored onto, and is stored
// as the page stores it. Every store is required: an unwired guard would find
// no conflict, so the hook refuses instead.
func RestoreAliasCheck(domains domainops.SuffixDomainFinder, aliases domainops.AliasHostnameFinder,
	settings domainops.MailSettingsReader) func(ctx context.Context, dom *models.Domain, hostname string) (string, error) {
	return func(ctx context.Context, dom *models.Domain, hostname string) (string, error) {
		if domains == nil || aliases == nil || settings == nil {
			return "", errRestoreChecksUnwired
		}
		host, status, code, detail := checkAliasHostname(ctx, aliasHostnameDeps{Domains: domains, Aliases: aliases, Settings: settings}, dom, hostname)
		if status != 0 {
			if detail == "" {
				detail = code
			}
			return "", errors.New(detail)
		}
		return host, nil
	}
}

// RestoreSource is where a restored archive comes from.
type RestoreSource int

const (
	// RestoreFromOwnBackup is an archive this server wrote to one of its own
	// backup destinations.
	RestoreFromOwnBackup RestoreSource = iota
	// RestoreFromUpload is a file someone uploaded. Whoever made it chose its
	// contents.
	RestoreFromUpload
)

// restoredRedirectProblem applies the update door's redirect-all rules to a
// restored row and returns why it fails, or "".
func restoredRedirectProblem(row *models.Domain) string {
	if row.RedirectAllTo != nil {
		to := strings.TrimSpace(*row.RedirectAllTo)
		if to != "" {
			if err := ValidateRedirectURL(to); err != nil {
				return err.Error()
			}
			row.RedirectAllTo = &to
		}
	}
	if row.RedirectAllType != nil {
		if t := strings.TrimSpace(*row.RedirectAllType); t != "" && !IsValidRedirectType(t) {
			return fmt.Sprintf("invalid redirect type %q", t)
		}
	}
	return ""
}

// dropRestoredMailSettings holds a restored row's mail settings to the domain
// page's rules: what fails is dropped with a warning, the rest is stored in
// the page's form. An empty MailProvider (an archive made before it) is left
// for the column's default.
func dropRestoredMailSettings(row *models.Domain) []string {
	var warnings []string
	if p := row.MailProvider; p != "" && !models.ValidMailProvider(p) {
		row.MailProvider = ""
		warnings = append(warnings, fmt.Sprintf("mail provider dropped: %q is not one the panel offers; the domain gets the default", p))
	}
	if row.MailProvider == models.MailProviderCustom && row.EmailEnabled {
		row.EmailEnabled = false
		warnings = append(warnings, "Jabali mail not turned on: the domain was made from a DNS template, whose records say where its mail goes")
	}
	if row.M365Onmicrosoft != nil {
		v, err := dnscompile.NormaliseM365Onmicrosoft(*row.M365Onmicrosoft)
		if err != nil {
			warnings = append(warnings, "Microsoft 365 tenant dropped: "+err.Error())
		}
		row.M365Onmicrosoft = strPtrOrNil(v)
	}
	if row.GoogleDKIM != nil {
		v, err := dnscompile.ValidateGoogleDKIM(*row.GoogleDKIM)
		if err != nil {
			warnings = append(warnings, "Google DKIM dropped: "+err.Error())
		}
		row.GoogleDKIM = strPtrOrNil(v)
	}
	if np := strings.TrimSpace(row.DmarcNP); dnscompile.ValidDMARCNP(np) {
		row.DmarcNP = np
	} else {
		row.DmarcNP = ""
		warnings = append(warnings, fmt.Sprintf("DMARC np dropped: %q must be empty, none, quarantine, or reject", np))
	}
	for _, f := range []struct {
		name string
		v    *string
	}{{"CalDAV host", &row.CalDAVHost}, {"CardDAV host", &row.CardDAVHost}} {
		h := strings.TrimSpace(*f.v)
		if err := validateDAVHost(h); err != nil {
			*f.v = ""
			warnings = append(warnings, fmt.Sprintf("%s dropped: %v", f.name, err))
			continue
		}
		*f.v = h
	}
	return warnings
}

// dropRestoredPHPSettings clears each per-domain PHP setting the PHP settings
// page would refuse, with a warning, and stores open_basedir in the page's
// canonical form. From an uploaded file open_basedir is held to the tenant's
// rules: only paths inside the owner's home (GH #1993).
func dropRestoredPHPSettings(row *models.Domain, ownerUsername string, source RestoreSource) []string {
	var warnings []string
	for _, s := range restoredPHPSettingRules {
		if s.check == nil {
			continue
		}
		if err := s.check(row, ownerUsername, source == RestoreFromUpload); err != nil {
			s.clear(row)
			warnings = append(warnings, fmt.Sprintf("PHP %s dropped: %v", s.directive, err))
		}
	}
	return warnings
}

// restoredPHPSetting is the PHP settings page's rule for one directive it
// writes. check returns why the page refuses the row's value (nil when the
// value is unset or accepted) and may rewrite it to the stored form; clear
// unsets it. An on/off switch has no check: the page takes either value.
type restoredPHPSetting struct {
	directive string
	check     func(row *models.Domain, owner string, tenant bool) error
	clear     func(row *models.Domain)
}

// restoredPHPSettingRules covers every directive in
// models.PHPPolicyDirectives (TestRestoredPHPSettingRules_CoverEveryPageDirective).
var restoredPHPSettingRules = []restoredPHPSetting{
	phpSizeSetting("memory_limit", func(d *models.Domain) **string { return &d.PHPMemoryLimit }),
	phpSizeSetting("upload_max_filesize", func(d *models.Domain) **string { return &d.PHPUploadMaxFilesize }),
	phpSizeSetting("post_max_size", func(d *models.Domain) **string { return &d.PHPPostMaxSize }),
	phpIntSetting("max_input_vars", func(d *models.Domain) **int { return &d.PHPMaxInputVars }, phpRangeCheck("max_input_vars")),
	phpIntSetting("max_execution_time", func(d *models.Domain) **int { return &d.PHPMaxExecutionTime }, phpRangeCheck("max_execution_time")),
	phpIntSetting("max_input_time", func(d *models.Domain) **int { return &d.PHPMaxInputTime }, phpRangeCheck("max_input_time")),
	{directive: "display_errors"},
	phpIntSetting("error_reporting", func(d *models.Domain) **int { return &d.PHPErrorReporting }, validateErrorReporting),
	{"date.timezone", func(row *models.Domain, _ string, _ bool) error {
		if row.PHPTimezone == nil {
			return nil
		}
		return validateTimezone(*row.PHPTimezone)
	}, func(row *models.Domain) { row.PHPTimezone = nil }},
	{directive: "log_errors"},
	{directive: "file_uploads"},
	{directive: "short_open_tag"},
	{"open_basedir", func(row *models.Domain, owner string, tenant bool) error {
		if row.PHPOpenBasedir == nil {
			return nil
		}
		norm, err := phpbasedir.Normalize(*row.PHPOpenBasedir, owner, tenant)
		if err != nil {
			return err
		}
		row.PHPOpenBasedir = &norm
		return nil
	}, func(row *models.Domain) { row.PHPOpenBasedir = nil }},
	{directive: "allow_url_fopen"},
}

func phpSizeSetting(directive string, field func(*models.Domain) **string) restoredPHPSetting {
	return restoredPHPSetting{directive, func(row *models.Domain, _ string, _ bool) error {
		if v := *field(row); v != nil {
			return validateSizeParam(*v)
		}
		return nil
	}, func(row *models.Domain) { *field(row) = nil }}
}

func phpIntSetting(directive string, field func(*models.Domain) **int, valid func(int) error) restoredPHPSetting {
	return restoredPHPSetting{directive, func(row *models.Domain, _ string, _ bool) error {
		if v := *field(row); v != nil {
			return valid(*v)
		}
		return nil
	}, func(row *models.Domain) { *field(row) = nil }}
}

func phpRangeCheck(name string) func(int) error {
	return func(v int) error { return validateIntParam(v, name) }
}
