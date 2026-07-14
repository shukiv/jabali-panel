package plesk

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// Compile-time: the Plesk Discoverer answers cheap per-account size
// queries for the lazy size endpoint.
var _ migrate.SizeProber = (*Discoverer)(nil)

// DescribeAccount returns a manifest scaffold for one Plesk
// subscription. Step 1 validates the subscription exists on the source
// and returns an empty-but-valid manifest (SchemaVersion + Source). The
// per-area builders (Domains via `plesk bin domain --info`, Databases
// via `plesk bin database --info`, DNS via `plesk bin dns --info`,
// Cron, then WordPress + mail + customers/packages) ship in follow-up
// steps — the same incremental shape the directadmin package took, so
// the registry + admin REST plumbing can wire Plesk now.
func (d *Discoverer) DescribeAccount(ctx context.Context, raw migrate.Session, accountID string) (*migrate.AccountManifest, error) {
	s, ok := raw.(*session)
	if !ok {
		return nil, errors.New("DescribeAccount: wrong session type")
	}
	if accountID == "" {
		return nil, errors.New("DescribeAccount: accountID empty")
	}
	host := ""
	if s.client != nil {
		host = s.client.RemoteAddr().String()
	}

	// Validate the subscription exists on the source. `plesk bin
	// subscription --info <name>` exits non-zero for an unknown
	// subscription. When the operator supplied an SSH principal rather
	// than a subscription name, auto-pivot to the sole subscription on a
	// single-tenant source; a multi-tenant source errors with the list.
	probe := fmt.Sprintf("plesk bin subscription --info '%s'",
		strings.ReplaceAll(accountID, "'", `'\''`))
	if _, err := s.run(ctx, d.CommandTimeout, probe); err != nil {
		accounts, listErr := d.ListAccounts(ctx, s)
		if listErr != nil {
			return nil, fmt.Errorf("subscription %q not found on source and auto-detect failed: %w", accountID, listErr)
		}
		switch len(accounts) {
		case 0:
			return nil, fmt.Errorf("no Plesk subscriptions found on source; supplied %q is not a subscription", accountID)
		case 1:
			accountID = accounts[0].ID
		default:
			names := make([]string, 0, len(accounts))
			for _, a := range accounts {
				names = append(names, a.ID)
			}
			return nil, fmt.Errorf("subscription %q not found; pick one of: %s", accountID, strings.Join(names, ", "))
		}
	}

	m := &migrate.AccountManifest{
		SchemaVersion: migrate.ManifestSchemaVersion,
		Source: migrate.SourceRef{
			Kind: models.MigrationSourcePlesk,
			Host: host,
			User: accountID,
		},
		Warnings: []migrate.Warning{},
	}

	// Per-area builders. Best-effort: each area records a warning on
	// failure but does not short-circuit. Domains is load-bearing — a
	// hard failure with zero domains parsed returns an error so the
	// operator sees it.
	var firstErr error
	if doms, err := d.describeDomains(ctx, s, accountID); err != nil {
		firstErr = fmt.Errorf("domains: %w", err)
		m.Warnings = append(m.Warnings, migrate.Warning{Code: "domains_failed", Detail: err.Error()})
	} else {
		m.Domains = doms
	}

	if dbs, warns, err := d.describeDatabases(ctx, s, m.Domains); err != nil {
		if firstErr == nil {
			firstErr = fmt.Errorf("databases: %w", err)
		}
		m.Warnings = append(m.Warnings, migrate.Warning{Code: "databases_failed", Detail: err.Error()})
	} else {
		m.Databases = dbs
		m.Warnings = append(m.Warnings, warns...)
	}

	// Mailboxes (GH #429 step 4): enumerate /var/qmail/mailnames per
	// owned domain. Best-effort — warns, never fatal.
	boxes, mailWarns := d.describeMailboxes(ctx, s, m.Domains)
	m.Mailboxes = boxes
	m.Warnings = append(m.Warnings, mailWarns...)

	// WordPress detection (GH #429 step 3): WP Toolkit JSON with a
	// docroot wp-config.php fallback. Best-effort — warns, never fatal.
	apps, wpWarns := d.describeWordPress(ctx, s, m.Domains)
	m.Apps = apps
	m.Warnings = append(m.Warnings, wpWarns...)

	// DNS zones, cron, SSH keys, mailboxes, and the customers/packages
	// layer land in later steps — flagged so the operator knows the
	// manifest is partial (plans/gh429-plesk-migration.md).
	m.Warnings = append(m.Warnings, migrate.Warning{
		Code:   "plesk_areas_pending",
		Detail: "DNS/cron/SSH and customers/packages ship in follow-up steps; describe currently covers domains + databases + WordPress + mailboxes.",
	})

	if firstErr != nil && len(m.Domains) == 0 {
		return nil, firstErr
	}
	return m, nil
}
