package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
//   - a per-domain PHP limit the PHP settings page would refuse (the agent
//     renders the sizes into the site's web server config).
//   - an index priority the domain page doesn't offer (GH #1993); the domain
//     then gets the default.
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
		warnings = append(warnings, dropRestoredPHPLimits(row)...)
		warnings = append(warnings, dropRestoredWebSettings(ctx, row, ownerUsername, settings, previews, source)...)
		return warnings, nil
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

// dropRestoredPHPLimits clears each per-domain PHP limit the PHP settings page
// would refuse and returns one warning for each.
func dropRestoredPHPLimits(row *models.Domain) []string {
	var warnings []string
	for _, f := range []struct {
		name string
		v    **string
	}{
		{"memory_limit", &row.PHPMemoryLimit},
		{"upload_max_filesize", &row.PHPUploadMaxFilesize},
		{"post_max_size", &row.PHPPostMaxSize},
	} {
		if *f.v == nil {
			continue
		}
		if err := validateSizeParam(**f.v); err != nil {
			*f.v = nil
			warnings = append(warnings, fmt.Sprintf("PHP %s dropped: %v", f.name, err))
		}
	}
	for _, f := range []struct {
		name string
		v    **int
	}{
		{"max_input_vars", &row.PHPMaxInputVars},
		{"max_execution_time", &row.PHPMaxExecutionTime},
		{"max_input_time", &row.PHPMaxInputTime},
	} {
		if *f.v == nil {
			continue
		}
		if err := validateIntParam(**f.v, f.name); err != nil {
			*f.v = nil
			warnings = append(warnings, fmt.Sprintf("PHP %s dropped: %v", f.name, err))
		}
	}
	return warnings
}
