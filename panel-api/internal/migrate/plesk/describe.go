package plesk

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

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

	return &migrate.AccountManifest{
		SchemaVersion: migrate.ManifestSchemaVersion,
		Source: migrate.SourceRef{
			Kind: models.MigrationSourcePlesk,
			Host: host,
			User: accountID,
		},
		Warnings: []migrate.Warning{{
			Code:   "plesk_areas_pending",
			Detail: "Plesk per-area builders (domains/databases/dns/cron), WordPress, mail, and customers/packages ship in follow-up steps (plans/gh429-plesk-migration.md).",
		}},
	}, nil
}
