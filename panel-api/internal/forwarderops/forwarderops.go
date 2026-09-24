// Package forwarderops holds the single shared path that converges a mailbox's
// server-side mail rules to Stalwart, so every caller — the HTTP handlers, the
// CLI, the cPanel migration, the backup restore, and the reconcile sweep —
// pushes the same desired state instead of each re-implementing (or forgetting)
// it.
//
// GH #1795: a mailbox's external forwards and its autoresponder both compile to
// the account's single active standard SieveScript, which Stalwart executes at
// delivery. They therefore cannot be applied independently (activating one
// deactivates the other), so Converge sends the FULL composite — forwards AND
// autoresponder — in one mailbox.sieve.apply. jabali previously wrote forwards
// to the x:SieveUserScript store (never run at delivery) and the autoresponder
// to a separate VacationResponse object; both are superseded here.
package forwarderops

import (
	"context"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Converge pushes a mailbox's full server-side rule state to Stalwart via the
// agent's mailbox.sieve.apply: type=external forwarders become the composite
// script's redirect(s) (keep_copy → `redirect :copy`), and the autoresponder
// row (if any) becomes the composite's vacation block. type=alias forwarders
// are served by the SQL directory, not Sieve, so they are not sent here. The
// agent self-heals the Stalwart Principal if it is not registered yet
// (GH #1795), so this works even right after the mailbox was created.
//
// Best-effort and idempotent — the DB is truth. A nil agent or forwarders repo
// is a no-op (returns nil), so a caller without an agent handle (e.g. the backup
// scheduler) can pass nil safely. A nil autoresponders repo means "no
// autoresponder in the composite" — the reconcile sweep, which always has both
// repos, re-converges the complete state on its next tick, so a caller that
// only has the forwarders repo stays eventually-consistent.
func Converge(ctx context.Context, ag agent.AgentInterface, forwarders repository.EmailForwarderRepository, autoresponders repository.EmailAutoresponderRepository, mailboxID, mailboxEmail string) error {
	if ag == nil || forwarders == nil {
		return nil
	}
	rows, _, err := forwarders.ListByMailboxID(ctx, mailboxID, repository.ListOptions{Limit: 500})
	if err != nil {
		return err
	}
	externals := []map[string]any{}
	for _, f := range rows {
		if !f.Enabled {
			continue
		}
		if f.Type == "external" {
			externals = append(externals, map[string]any{"target": f.Target, "keep_copy": f.KeepCopy})
		}
	}

	params := map[string]any{
		"mailbox_email": mailboxEmail,
		"externals":     externals,
		"autoresponder": autoresponderPayload(ctx, autoresponders, mailboxID),
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err = ag.Call(cctx, "mailbox.sieve.apply", params)
	return err
}

// autoresponderPayload projects the mailbox's autoresponder row into the
// mailbox.sieve.apply "autoresponder" sub-object, or nil when there is no repo
// or no row. Dates are RFC 3339. A read error yields nil (the sweep re-converges
// later) rather than failing the whole convergence.
func autoresponderPayload(ctx context.Context, autoresponders repository.EmailAutoresponderRepository, mailboxID string) map[string]any {
	if autoresponders == nil {
		return nil
	}
	ar, err := autoresponders.FindByMailboxID(ctx, mailboxID)
	if err != nil || ar == nil {
		return nil
	}
	rfc := func(t *time.Time) *string {
		if t == nil {
			return nil
		}
		s := t.UTC().Format(time.RFC3339)
		return &s
	}
	return map[string]any{
		"enabled":   ar.Enabled,
		"from_date": rfc(ar.FromDate),
		"to_date":   rfc(ar.ToDate),
		"subject":   ar.Subject,
		"text_body": ar.TextBody,
		"html_body": ar.HTMLBody,
	}
}
