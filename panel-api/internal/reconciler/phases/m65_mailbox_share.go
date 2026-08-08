package phases

import (
	"context"
	"fmt"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// mailboxSharePhase converges jabali mailbox_shares → Stalwart
// Mailbox.shareWith on each owner mailbox's INBOX. DB is truth.
type mailboxSharePhase struct {
	agent         agent.AgentInterface
	shares        repository.MailboxShareRepository
	mailboxes     repository.MailboxRepository
	domains       repository.DomainRepository
}

func NewMailboxSharePhase(
	ag agent.AgentInterface,
	shares repository.MailboxShareRepository,
	mailboxes repository.MailboxRepository,
	domains repository.DomainRepository,
) Phase {
	return &mailboxSharePhase{agent: ag, shares: shares, mailboxes: mailboxes, domains: domains}
}

func (p *mailboxSharePhase) Name() string { return "mailbox_share" }

func (p *mailboxSharePhase) ReconcileDomain(_ context.Context, _ *models.Domain, _ map[string]interface{}) error {
	return nil
}

func (p *mailboxSharePhase) ReconcileMailbox(ctx context.Context, mb *models.Mailbox, dom *models.Domain, _ map[string]interface{}) error {
	if p.agent == nil || p.shares == nil || p.mailboxes == nil || p.domains == nil || dom == nil {
		return nil
	}
	// Fetch all shares owned by this mailbox.
	shares, _, err := p.shares.FindByOwnerID(ctx, mb.ID, repository.ListOptions{Limit: 500})
	if err != nil {
		return fmt.Errorf("mailbox_share: list %s: %w", mb.ID, err)
	}

	// Build target email → rights map.
	//
	// Batch the lookups (JAB-147 pattern): resolving each grant with its own
	// FindByID pair ran 2 queries per share on the 60s reconcile hot path,
	// even though FindByIDs already exists on both repositories.
	targetIDs := make([]string, 0, len(shares))
	for _, s := range shares {
		targetIDs = append(targetIDs, s.SharedWithMailboxID)
	}
	targets, terr := p.mailboxes.FindByIDs(ctx, targetIDs)
	if terr != nil {
		return fmt.Errorf("batch-load share targets: %w", terr)
	}
	targetByID := make(map[string]models.Mailbox, len(targets))
	domIDs := make([]string, 0, len(targets))
	for _, t := range targets {
		targetByID[t.ID] = t
		domIDs = append(domIDs, t.DomainID)
	}
	targetDomains, derr := p.domains.FindByIDs(ctx, domIDs)
	if derr != nil {
		return fmt.Errorf("batch-load share target domains: %w", derr)
	}
	domByID := make(map[string]models.Domain, len(targetDomains))
	for _, d := range targetDomains {
		domByID[d.ID] = d
	}

	sharesByEmail := make(map[string]models.Rights, len(shares))
	for _, s := range shares {
		target, ok := targetByID[s.SharedWithMailboxID]
		if !ok {
			continue
		}
		tdom, ok := domByID[target.DomainID]
		if !ok {
			continue
		}
		sharesByEmail[target.LocalPart+"@"+tdom.Name] = s.Rights
	}

	// Push even if empty (clears shares on Stalwart when jabali has none).
	params := map[string]any{
		"owner_email": mb.LocalPart + "@" + dom.Name,
		"shares":      sharesByEmail,
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := p.agent.Call(callCtx, "mailbox.share_set", params); err != nil {
		return fmt.Errorf("mailbox_share: agent push for %s: %w", mb.ID, err)
	}
	return nil
}
