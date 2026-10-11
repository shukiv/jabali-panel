package reconciler

import (
	"context"
	"sort"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/trustedsenders"
)

// reconcileMailboxTrustedSenders (GH #2017) sends every mailbox's trusted
// senders to Stalwart, where the agent keeps them as contact cards the spam
// filter trusts. The API pushes a mailbox's list when it changes; this pass
// is the retry path and the audit:
//
//   - A sender saved while the mail server was down (row kept, warning
//     returned) is pushed on a following tick.
//   - Rows a restore wrote are pushed.
//   - Cards changed or deleted on Stalwart (in webmail, say) are put back
//     within PhaseMailboxTrustedSenders' audit interval.
//
// Only mailboxes with rows are swept. A mailbox whose last sender was
// removed is not: the API pushed its empty list, and deletes the row only
// after Stalwart accepted that push. Its ledger entry is forgotten, so
// trusting the same senders again is pushed rather than skipped.
//
// The batch read is a change detector. The push re-reads the mailbox's
// rows, so a sender deleted after this tick's read is not put back.
const (
	trustedSendersApplyBudgetPerTick = 20
	trustedSendersRetryInterval      = 15 * time.Minute
	// trustedSendersApplyTimeout: the agent reads every card of the account
	// to find the panel's, which a large address book makes slow.
	trustedSendersApplyTimeout = time.Minute
)

// WithMailboxTrustedSenders wires the trusted-senders pass. The mailbox repo
// is the one the other mailbox passes use. nil rows disables the pass.
func (r *Reconciler) WithMailboxTrustedSenders(rows repository.MailboxTrustedSenderRepository, mailboxes repository.MailboxRepository) *Reconciler {
	r.trustedSenders = rows
	if mailboxes != nil {
		r.mailboxes = mailboxes
	}
	return r
}

func (r *Reconciler) reconcileMailboxTrustedSenders(ctx context.Context) {
	if r.agent == nil || r.trustedSenders == nil || r.mailboxes == nil || r.domains == nil || r.serverSettings == nil {
		return
	}
	sctx, scancel := context.WithTimeout(ctx, 5*time.Second)
	srv, err := r.settingsGet(sctx)
	scancel()
	if err != nil || srv == nil || !srv.MailEnabled {
		return
	}

	rows, err := r.trustedSenders.ListAll(ctx)
	if err != nil {
		r.log.Warn("trusted-senders: list failed", "error", err)
		return
	}
	byMailbox := map[string][]models.MailboxTrustedSender{}
	for _, row := range rows {
		byMailbox[row.MailboxID] = append(byMailbox[row.MailboxID], row)
	}
	r.forgetTrustedSendersGone(byMailbox)
	if len(byMailbox) == 0 {
		return
	}

	ids := make([]string, 0, len(byMailbox))
	for id := range byMailbox {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	mbs, err := r.mailboxes.FindByIDs(ctx, ids)
	if err != nil {
		r.log.Warn("trusted-senders: load mailboxes failed", "error", err)
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
		r.log.Warn("trusted-senders: load domains failed", "error", err)
		return
	}
	hosted := make(map[string]bool, len(doms))
	for _, d := range doms {
		hosted[d.ID] = mailDirectoryDomain(d)
	}

	now := time.Now()
	applied := 0
	for _, id := range ids {
		mb, ok := mbByID[id]
		// Mail off for the domain, or hosted elsewhere: the rows stay, and
		// there is no Stalwart account to write them to.
		if !ok || !hosted[mb.DomainID] {
			continue
		}
		if applied >= trustedSendersApplyBudgetPerTick {
			return // the rest are applied on following ticks
		}
		if r.trustedSendersBackingOff(id, now) {
			continue
		}
		want := struct {
			Email     string
			Addresses []string
		}{mb.EmailCached, trustedsenders.Addresses(byMailbox[id])}
		ran, err := r.project(ctx, PhaseMailboxTrustedSenders, id, fingerprint(want), false, func() error {
			current, err := r.trustedSenders.ListByMailbox(ctx, id)
			if err != nil {
				return err
			}
			cctx, cancel := context.WithTimeout(ctx, trustedSendersApplyTimeout)
			defer cancel()
			return trustedsenders.Push(cctx, r.agent, mb.EmailCached, trustedsenders.Addresses(current))
		})
		if !ran {
			continue
		}
		applied++
		r.trustedSendersMu.Lock()
		if err != nil {
			if r.trustedSendersRetryAt == nil {
				r.trustedSendersRetryAt = map[string]time.Time{}
			}
			r.trustedSendersRetryAt[id] = now.Add(trustedSendersRetryInterval)
		} else {
			delete(r.trustedSendersRetryAt, id)
		}
		r.trustedSendersMu.Unlock()
		if err != nil {
			r.log.Warn("trusted-senders: apply failed", "mailbox", mb.EmailCached, "error", err)
		}
	}
}

// forgetTrustedSendersGone drops the ledger entry of every mailbox that had
// rows on the last tick and has none now.
func (r *Reconciler) forgetTrustedSendersGone(byMailbox map[string][]models.MailboxTrustedSender) {
	r.trustedSendersMu.Lock()
	defer r.trustedSendersMu.Unlock()
	for id := range r.trustedSendersSeen {
		if _, ok := byMailbox[id]; !ok {
			r.ledger.forget(PhaseMailboxTrustedSenders, id)
			delete(r.trustedSendersRetryAt, id)
		}
	}
	r.trustedSendersSeen = make(map[string]bool, len(byMailbox))
	for id := range byMailbox {
		r.trustedSendersSeen[id] = true
	}
}

// trustedSendersBackingOff reports whether id's last push failed less than
// trustedSendersRetryInterval ago.
func (r *Reconciler) trustedSendersBackingOff(id string, now time.Time) bool {
	r.trustedSendersMu.Lock()
	defer r.trustedSendersMu.Unlock()
	until, ok := r.trustedSendersRetryAt[id]
	return ok && now.Before(until)
}
