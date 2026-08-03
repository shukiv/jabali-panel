package cpanel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/htaccess"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// DomainImportResult is returned to the restore-stage caller for
// progress reporting + manifest update.
type DomainImportResult struct {
	Created      int
	EmailEnabled int
	Skipped      []string
	// Failed names domains whose agent-side create FAILED, as opposed to ones
	// deliberately skipped (already present, filtered out). JAB-214: these used
	// to land in Skipped alongside benign entries, so the restore reported
	// "domains: created=0" plus a skip line and still exited 0 — while the
	// account had no vhost, no DNS zone and no mail routing. A domain is a core
	// area; the caller promotes these to criticals, which downgrades the run to
	// DEGRADED and exits non-zero unless --allow-degraded.
	Failed []string
	// HtaccessRulesImported counts typed Rule Builder entries converted from
	// migrated .htaccess files (ADR-0130). HtaccessWarnings carries the
	// per-domain "not converted" advisories for the migration manifest.
	HtaccessRulesImported int
	HtaccessWarnings      []string
}

type domainEmailEnableResp struct {
	Ok            bool   `json:"ok"`
	DKIMSelector  string `json:"dkim_selector"`
	DKIMPublicKey string `json:"dkim_public_key"`
}

// ImportDomains creates panel domain rows + nginx vhosts for every
// zone file found in the parsed tarball. If a domain's mail directory
// exists under the tarball's MailRoot, email is enabled on the spot so
// the ImportMailboxes stage that follows can deliver into Stalwart.
//
// Idempotent: existing domains (matched by name) are skipped rather
// than duplicated.
func ImportDomains(
	ctx context.Context,
	domainsRepo repository.DomainRepository,
	agentCli agent.AgentInterface,
	parsed *ParsedTarball,
	targetUserID string,
	targetUsername string,
) (*DomainImportResult, error) {
	if parsed == nil {
		return nil, fmt.Errorf("ImportDomains: parsed nil")
	}
	res := &DomainImportResult{}
	mailDomains := scanMailDomains(parsed)
	// jabali convention: every domain docroot lives at
	//   /home/<user>/domains/<dom>/public_html/
	// regardless of source layout. M35.8 ImportHomeSplit rsyncs
	// per-domain content into that target, so the nginx vhost +
	// FPM pool point at the right path. Source-layout-specific
	// overrides (DA already-resolved DocRoots) still win when set.
	docRootFor := func(name string) string {
		if parsed.DocRoots != nil {
			if override, ok := parsed.DocRoots[name]; ok && override != "" {
				return override
			}
		}
		return filepath.Join("/home", targetUsername, "domains", name, "public_html")
	}

	type domainEntry struct {
		name    string
		docRoot string
	}
	var entries []domainEntry
	if len(parsed.ZoneFiles) > 0 {
		for _, zonePath := range parsed.ZoneFiles {
			domainName := strings.TrimSuffix(filepath.Base(zonePath), ".db")
			if domainName == "" {
				res.Skipped = append(res.Skipped, fmt.Sprintf("domain_skip:empty_name_from_%s", zonePath))
				continue
			}
			entries = append(entries, domainEntry{
				name:    domainName,
				docRoot: docRootFor(domainName),
			})
		}
	} else {
		for _, name := range parsed.DomainNames {
			if name == "" {
				continue
			}
			entries = append(entries, domainEntry{name: name, docRoot: docRootFor(name)})
		}
	}

	for _, e := range entries {
		domainName := e.name
		if _, err := domainsRepo.FindByName(ctx, domainName); err == nil {
			res.Skipped = append(res.Skipped, "domain_skip:already_exists:"+domainName)
			continue
		}

		domainID := ids.NewULID()
		docRoot := e.docRoot

		if _, err := agentCli.Call(ctx, "domain.create", map[string]any{
			"domain_id":      domainID,
			"domain":         domainName,
			"username":       targetUsername,
			"doc_root":       docRoot,
			"index_priority": "html_first",
		}); err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("domain_skip:agent_create_failed:%s:%v", domainName, err))
			res.Failed = append(res.Failed, fmt.Sprintf("%s (%v)", domainName, err))
			continue
		}

		now := time.Now()
		d := &models.Domain{
			ID:            domainID,
			UserID:        targetUserID,
			Name:          domainName,
			DocRoot:       docRoot,
			IsEnabled:     true,
			IndexPriority: "html_first",
			GhostState:    "unchecked",
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		// #327: a source domain with NO mail must not inherit jabali's
		// mail-by-default. MailProvider "none" makes the reconciler's
		// includeMail=false (no MX/SPF/DMARC/mail-A bootstrap); email_enabled
		// off keeps the DKIM + mail-cert reconcilers away. Mail domains fall
		// through to domain.email_enable below (provider stays the "jabali"
		// default).
		if !mailDomains[domainName] {
			d.MailProvider = models.MailProviderNone
			d.EmailEnabled = false
		}

		// .htaccess -> typed Rule Builder entries (ADR-0130). The source
		// docroot was rsynced into docRoot by the home-split step that runs
		// before this stage, so a .htaccess present there converts to typed
		// NginxRules now, persisted with the row (the reconciler renders them
		// into the vhost on its next tick — nginx -t gated). An absent
		// .htaccess is the common case and is a silent no-op; unconverted
		// lines surface as per-domain manifest warnings, never dropped quietly.
		applyMigratedHtaccess(filepath.Join(docRoot, ".htaccess"), domainName, d, res)

		if err := domainsRepo.Create(ctx, d); err != nil {
			// JAB-57: domain.create already built the nginx vhost (+ enabled
			// symlink) but the panel row failed to land — an unmanaged vhost
			// that serves traffic and collides with a retry (whose collision
			// check keys on the domains row, now absent). Roll it back:
			// domain.delete removes the vhost + reloads nginx. The docroot
			// under /home is left (harmless; overwritten by the home-split
			// rsync on retry) — only the serving/system state is reverted.
			delCtx, dcancel := context.WithTimeout(ctx, 60*time.Second)
			_, delErr := agentCli.Call(delCtx, "domain.delete", map[string]string{"domain": domainName})
			dcancel()
			if delErr != nil {
				res.Skipped = append(res.Skipped, fmt.Sprintf("domain_skip:db_create_failed_AND_vhost_rollback_failed:%s:row=%v:delete=%v", domainName, err, delErr))
			} else {
				res.Skipped = append(res.Skipped, fmt.Sprintf("domain_skip:db_create_failed_vhost_rolled_back:%s:%v", domainName, err))
			}
			continue
		}
		res.Created++

		if !mailDomains[domainName] {
			continue
		}

		raw, err := agentCli.Call(ctx, "domain.email_enable", map[string]any{
			"domain_id":   domainID,
			"domain_name": domainName,
		})
		if err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("email_skip:enable_failed:%s:%v", domainName, err))
			continue
		}
		var emailResp domainEmailEnableResp
		if err := json.Unmarshal(raw, &emailResp); err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("email_skip:decode_failed:%s:%v", domainName, err))
			continue
		}

		enabledAt := time.Now()
		if err := domainsRepo.UpdateEmailState(ctx, domainID, repository.DomainEmailState{
			Enabled:        true,
			DkimSelector:   &emailResp.DKIMSelector,
			DkimPublicKey:  &emailResp.DKIMPublicKey,
			EmailEnabledAt: &enabledAt,
		}); err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("email_skip:db_update_failed:%s:%v", domainName, err))
			continue
		}
		res.EmailEnabled++
	}
	return res, nil
}

// scanMailDomains returns the set of domain names that have a mail
// directory under the tarball's MailRoot (i.e. domains that had
// mailboxes in the source panel).
func scanMailDomains(parsed *ParsedTarball) map[string]bool {
	result := make(map[string]bool)
	mailRoot := parsed.MailRoot
	if mailRoot == "" && parsed.HomeDir != "" {
		mailRoot = filepath.Join(parsed.HomeDir, "mail")
	}
	if mailRoot == "" {
		return result
	}
	entries, err := os.ReadDir(mailRoot)
	if err != nil {
		return result
	}
	for _, e := range entries {
		if e.IsDir() {
			result[e.Name()] = true
		}
	}
	return result
}

// applyMigratedHtaccess reads the .htaccess at path (if any), converts it to
// typed NginxRules on d, and records counts/warnings on res. Best-effort: a
// missing file is normal and silent; conversion never fails the migration.
// Capped at the per-domain rule limit (extra rules are reported, not applied).
func applyMigratedHtaccess(path, domainName string, d *models.Domain, res *DomainImportResult) {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return
	}
	conv := htaccess.Convert(string(raw), "/")
	rules := conv.Rules
	// Mirror the API's per-domain rule cap (GH #301).
	const maxImportRules = 500
	if len(rules) > maxImportRules {
		res.HtaccessWarnings = append(res.HtaccessWarnings,
			fmt.Sprintf("%s: .htaccess produced %d rules; only the first %d were imported", domainName, len(rules), maxImportRules))
		rules = rules[:maxImportRules]
	}
	if len(rules) > 0 {
		d.NginxRules = models.NginxRules(rules)
		res.HtaccessRulesImported += len(rules)
	}
	for _, w := range conv.Warnings {
		res.HtaccessWarnings = append(res.HtaccessWarnings,
			fmt.Sprintf("%s: %s", domainName, w.Reason))
	}
}
