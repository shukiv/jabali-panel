package reconciler

import (
	"context"
	"sort"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailshareops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// reconcileMailboxShares applies every owner mailbox's share list to
// Stalwart (the owner Inbox's JMAP shareWith). The API and the CLI apply a
// share when it is created or deleted; this sweep is the fleet backfill and
// the retry path:
//
//   - Shares saved before the panel applied them (the reconciler phase meant
//     to push them was never registered) are pushed on the first tick after
//     the upgrade.
//   - A create whose apply failed (row kept, warning returned) is retried.
//   - Drift on Stalwart is repaired within PhaseMailboxShares' audit interval.
//
// Only owners that have share rows are swept. An owner whose last share was
// deleted is not: the delete path already pushed its empty list, and it
// deletes the row only after Stalwart accepted that push.
//
// The batch read is a change detector only. The apply itself goes through
// mailshareops.Apply, which re-reads the owner's rows, so a share deleted
// after this tick's read is not pushed back.
const (
	mailboxShareApplyBudgetPerTick = 50
	mailboxShareRetryInterval      = 5 * time.Minute
	mailboxShareListPageSize       = 500
)

// WithMailboxShares wires the mailbox-share sweep. The mailbox repo is the one
// the other mailbox passes use. nil disables the pass.
func (r *Reconciler) WithMailboxShares(shares repository.MailboxShareRepository, mailboxes repository.MailboxRepository) *Reconciler {
	r.mailboxShares = shares
	if mailboxes != nil {
		r.mailboxes = mailboxes
	}
	return r
}

func (r *Reconciler) reconcileMailboxShares(ctx context.Context) {
	if r.agent == nil || r.mailboxShares == nil || r.mailboxes == nil || r.domains == nil || r.serverSettings == nil {
		return
	}
	sctx, scancel := context.WithTimeout(ctx, 5*time.Second)
	srv, err := r.settingsGet(sctx)
	scancel()
	if err != nil || srv == nil || !srv.MailEnabled {
		return
	}

	rows, err := r.listAllMailboxShares(ctx)
	if err != nil {
		r.log.Warn("mailbox-shares: list shares failed", "error", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	byOwner := map[string][]models.MailboxShare{}
	mailboxIDs := make([]string, 0, len(rows)*2)
	for _, s := range rows {
		byOwner[s.OwnerMailboxID] = append(byOwner[s.OwnerMailboxID], s)
		mailboxIDs = append(mailboxIDs, s.OwnerMailboxID, s.SharedWithMailboxID)
	}
	mbs, err := r.mailboxes.FindByIDs(ctx, mailboxIDs)
	if err != nil {
		r.log.Warn("mailbox-shares: load mailboxes failed", "error", err)
		return
	}
	mbByID := make(map[string]models.Mailbox, len(mbs))
	domainIDs := make([]string, 0, len(mbs))
	for _, mb := range mbs {
		mbByID[mb.ID] = mb
		domainIDs = append(domainIDs, mb.DomainID)
	}
	doms, err := r.domains.FindByIDs(ctx, domainIDs)
	if err != nil {
		r.log.Warn("mailbox-shares: load domains failed", "error", err)
		return
	}
	emailOn := make(map[string]bool, len(doms))
	for _, d := range doms {
		emailOn[d.ID] = d.EmailEnabled
	}

	owners := make([]string, 0, len(byOwner))
	for id := range byOwner {
		owners = append(owners, id)
	}
	sort.Strings(owners)

	deps := mailshareops.Deps{Agent: r.agent, Mailboxes: r.mailboxes, Domains: r.domains, Shares: r.mailboxShares}
	now := time.Now()
	applied := 0
	for _, ownerID := range owners {
		owner, ok := mbByID[ownerID]
		if !ok {
			continue
		}
		// Mail off for the domain (soft disable or the mail-only purge)
		// keeps the rows; there is no Stalwart account to apply them to.
		if !emailOn[owner.DomainID] {
			continue
		}
		if applied >= mailboxShareApplyBudgetPerTick {
			return // the rest are applied on following ticks
		}
		if r.mailboxShareBackingOff(ownerID, now) {
			continue
		}
		want := mailshareops.Payload{OwnerEmail: owner.EmailCached, Shares: map[string]models.Rights{}}
		for _, s := range byOwner[ownerID] {
			if t, ok := mbByID[s.SharedWithMailboxID]; ok {
				want.Shares[t.EmailCached] = s.Rights
			}
		}
		ran, err := r.project(ctx, PhaseMailboxShares, ownerID, fingerprint(want), false, func() error {
			return mailshareops.Apply(ctx, deps, ownerID)
		})
		if !ran {
			continue
		}
		applied++
		r.mailboxShareMu.Lock()
		if err != nil {
			if r.mailboxShareRetryAt == nil {
				r.mailboxShareRetryAt = map[string]time.Time{}
			}
			r.mailboxShareRetryAt[ownerID] = now.Add(mailboxShareRetryInterval)
		} else {
			delete(r.mailboxShareRetryAt, ownerID)
		}
		r.mailboxShareMu.Unlock()
		if err != nil {
			r.log.Warn("mailbox-shares: apply failed", "owner", owner.EmailCached, "error", err)
		}
	}
}

// mailboxShareBackingOff reports whether ownerID's last apply failed less
// than mailboxShareRetryInterval ago. Without it, owners that keep failing
// (an account missing on Stalwart) would use up the per-tick budget ahead of
// every other owner.
func (r *Reconciler) mailboxShareBackingOff(ownerID string, now time.Time) bool {
	r.mailboxShareMu.Lock()
	defer r.mailboxShareMu.Unlock()
	until, ok := r.mailboxShareRetryAt[ownerID]
	return ok && now.Before(until)
}

func (r *Reconciler) listAllMailboxShares(ctx context.Context) ([]models.MailboxShare, error) {
	var all []models.MailboxShare
	for offset := 0; ; offset += mailboxShareListPageSize {
		page, _, err := r.mailboxShares.ListAll(ctx, repository.ListOptions{Offset: offset, Limit: mailboxShareListPageSize})
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < mailboxShareListPageSize {
			return all, nil
		}
	}
}
