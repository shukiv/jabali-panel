// Package forwarderops holds the single shared path that converges a mailbox's
// email forwarders to Stalwart, so every caller — the HTTP handler, the cPanel
// migration, and the backup restore — pushes the same desired state instead of
// each re-implementing (or forgetting) it.
//
// Before this, only api.applyForwarders converged forwarders, and only on a
// forwarder mutation. The bulk import paths created email_forwarders rows
// without ever pushing them, so a restored/migrated forwarder did not actually
// forward until someone edited it in the UI (GH #1795 follow-up).
package forwarderops

import (
	"context"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Converge pushes a mailbox's full forwarder desired state to Stalwart via the
// agent's forwarder.apply: type=alias entries become account aliases, type=
// external entries become the redirect Sieve (keep_copy → `redirect :copy`).
// The agent self-heals the Stalwart Principal if it is not registered yet
// (GH #1795), so this works even right after the mailbox was created.
//
// Best-effort and idempotent — the DB is truth. A nil agent or forwarders repo
// is a no-op (returns nil), so a caller without an agent handle (e.g. the backup
// scheduler) can pass nil safely. Only enabled forwarders are sent; a mailbox
// with no enabled forwarders converges to an empty set, which clears any stale
// Sieve.
func Converge(ctx context.Context, ag agent.AgentInterface, forwarders repository.EmailForwarderRepository, mailboxID, mailboxEmail string) error {
	if ag == nil || forwarders == nil {
		return nil
	}
	rows, _, err := forwarders.ListByMailboxID(ctx, mailboxID, repository.ListOptions{Limit: 500})
	if err != nil {
		return err
	}
	aliases := []map[string]string{}
	externals := []map[string]any{}
	for _, f := range rows {
		if !f.Enabled {
			continue
		}
		switch f.Type {
		case "alias":
			if f.LocalPart != nil {
				aliases = append(aliases, map[string]string{"local_part": *f.LocalPart})
			}
		case "external":
			externals = append(externals, map[string]any{"target": f.Target, "keep_copy": f.KeepCopy})
		}
	}
	params := map[string]any{
		"mailbox_email": mailboxEmail,
		"aliases":       aliases,
		"externals":     externals,
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err = ag.Call(cctx, "forwarder.apply", params)
	return err
}
