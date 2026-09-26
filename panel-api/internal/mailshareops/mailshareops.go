// Package mailshareops is the single path that applies a mailbox's shares
// (mailbox_shares rows) to Stalwart. The HTTP handlers, the CLI and the
// reconcile sweep all go through it, so each one pushes the same desired state.
//
// A share gives another mailbox access to the owner's Inbox: Stalwart stores
// it as the Inbox's JMAP shareWith map. The agent's mailbox.share_set replaces
// that whole map, so every push carries the owner's FULL share list from the
// DB, which is truth.
//
// Before this package, nothing called mailbox.share_set: the reconciler phase
// meant to push shares was never registered, so shares saved in the panel only
// ever existed as rows.
package mailshareops

import (
	"context"
	"errors"
	"fmt"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Deps are the stores and the agent a share operation needs.
type Deps struct {
	Agent     agent.AgentInterface
	Mailboxes repository.MailboxRepository
	Domains   repository.DomainRepository
	Shares    repository.MailboxShareRepository
}

var (
	// ErrTargetNotFound covers a target mailbox that does not exist AND one
	// that belongs to another account, so a caller cannot probe for other
	// tenants' mailboxes.
	ErrTargetNotFound = errors.New("mailshareops: target mailbox not found in the owner's account")
	ErrSelfShare      = errors.New("mailshareops: a mailbox cannot be shared with itself")
	ErrNoRights       = errors.New("mailshareops: at least one right is required")
	ErrAlreadyShared  = errors.New("mailshareops: the mailbox is already shared with this target")
	ErrNotFound       = errors.New("mailshareops: share not found")
	// ErrApply means Stalwart did not accept the owner's share list.
	ErrApply = errors.New("mailshareops: the mail server did not accept the share list")
)

const (
	applyTimeout = 15 * time.Second
	// maxSharesPerOwner bounds one owner's list read. The UI and CLI create
	// one share per target mailbox, so an owner reaching it would need more
	// mailboxes than any account has.
	maxSharesPerOwner = 500
)

// Payload is the mailbox.share_set request: the owner's full share list,
// keyed by target mailbox email.
type Payload struct {
	OwnerEmail string                   `json:"owner_email"`
	Shares     map[string]models.Rights `json:"shares"`
}

// Desired builds the owner's payload from its share rows, leaving out
// excludeShareID ("" keeps every row). A row whose target mailbox no longer
// exists is left out.
func Desired(ctx context.Context, d Deps, owner *models.Mailbox, excludeShareID string) (Payload, error) {
	rows, _, err := d.Shares.FindByOwnerID(ctx, owner.ID, repository.ListOptions{Limit: maxSharesPerOwner})
	if err != nil {
		return Payload{}, fmt.Errorf("list shares of %s: %w", owner.EmailCached, err)
	}
	targetIDs := make([]string, 0, len(rows))
	for _, s := range rows {
		targetIDs = append(targetIDs, s.SharedWithMailboxID)
	}
	byID := map[string]models.Mailbox{}
	if len(targetIDs) > 0 {
		targets, err := d.Mailboxes.FindByIDs(ctx, targetIDs)
		if err != nil {
			return Payload{}, fmt.Errorf("load share targets of %s: %w", owner.EmailCached, err)
		}
		for _, t := range targets {
			byID[t.ID] = t
		}
	}
	p := Payload{OwnerEmail: owner.EmailCached, Shares: map[string]models.Rights{}}
	for _, s := range rows {
		if s.ID == excludeShareID {
			continue
		}
		t, ok := byID[s.SharedWithMailboxID]
		if !ok {
			continue
		}
		p.Shares[t.EmailCached] = s.Rights
	}
	return p, nil
}

// Push sends one owner's share list to the agent. A nil agent is a failure,
// never a silent success: a revoke that was not applied must not look applied.
func Push(ctx context.Context, ag agent.AgentInterface, p Payload) error {
	if ag == nil {
		return fmt.Errorf("%w: agent not configured", ErrApply)
	}
	cctx, cancel := context.WithTimeout(ctx, applyTimeout)
	defer cancel()
	if _, err := ag.Call(cctx, "mailbox.share_set", p); err != nil {
		return fmt.Errorf("%w: %w", ErrApply, err)
	}
	return nil
}

// Apply re-reads the owner's share rows and pushes them. It reads the DB at
// apply time, so a caller that decided to apply from an older read (the
// reconcile sweep) still pushes the current list.
func Apply(ctx context.Context, d Deps, ownerMailboxID string) error {
	owner, err := d.Mailboxes.FindByID(ctx, ownerMailboxID)
	if err != nil {
		return fmt.Errorf("load owner mailbox %s: %w", ownerMailboxID, err)
	}
	p, err := Desired(ctx, d, owner, "")
	if err != nil {
		return err
	}
	return Push(ctx, d.Agent, p)
}

// CreateResult is a saved share. ApplyErr is set when the row was saved but
// Stalwart did not accept the list yet; the reconcile sweep retries it.
type CreateResult struct {
	Share    *models.MailboxShare
	ApplyErr error
}

// Create validates a new share, saves it and applies the owner's list.
//
// The target must be a different mailbox that belongs to the same account
// (panel user) as the owner, and at least one right must be set. A share
// gives the target access to the owner's mail, so a share across accounts is
// refused for every caller, admins included.
func Create(ctx context.Context, d Deps, owner *models.Mailbox, targetMailboxID string, rights models.Rights, managedBy string) (*CreateResult, error) {
	if rights == (models.Rights{}) {
		return nil, ErrNoRights
	}
	if targetMailboxID == owner.ID {
		return nil, ErrSelfShare
	}
	target, err := d.Mailboxes.FindByID(ctx, targetMailboxID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrTargetNotFound
		}
		return nil, fmt.Errorf("load target mailbox: %w", err)
	}
	same, err := sameAccount(ctx, d, owner, target)
	if err != nil {
		return nil, err
	}
	if !same {
		return nil, ErrTargetNotFound
	}
	existing, _, err := d.Shares.FindByOwnerID(ctx, owner.ID, repository.ListOptions{Limit: maxSharesPerOwner})
	if err != nil {
		return nil, fmt.Errorf("list shares of %s: %w", owner.EmailCached, err)
	}
	for _, s := range existing {
		if s.SharedWithMailboxID == target.ID {
			return nil, ErrAlreadyShared
		}
	}
	share := &models.MailboxShare{
		ID:                  ids.NewULID(),
		OwnerMailboxID:      owner.ID,
		SharedWithMailboxID: target.ID,
		Rights:              rights,
		ManagedBy:           managedBy,
	}
	if err := d.Shares.Create(ctx, share); err != nil {
		return nil, fmt.Errorf("save share: %w", err)
	}
	return &CreateResult{Share: share, ApplyErr: Apply(ctx, d, owner.ID)}, nil
}

// Delete revokes one of the owner's shares. It pushes the owner's list
// WITHOUT the share first and deletes the row only after Stalwart accepted
// it. When the push fails the row stays and the error wraps ErrApply, so the
// panel never shows a revoke that did not happen.
func Delete(ctx context.Context, d Deps, ownerMailboxID, shareID string) error {
	s, err := d.Shares.FindByID(ctx, shareID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("load share: %w", err)
	}
	if s.OwnerMailboxID != ownerMailboxID {
		return ErrNotFound
	}
	owner, err := d.Mailboxes.FindByID(ctx, ownerMailboxID)
	if err != nil {
		return fmt.Errorf("load owner mailbox %s: %w", ownerMailboxID, err)
	}
	p, err := Desired(ctx, d, owner, shareID)
	if err != nil {
		return err
	}
	if err := Push(ctx, d.Agent, p); err != nil {
		return err
	}
	// Owner-scoped delete, as the HTTP layer always used (see DeleteByOwner).
	if err := d.Shares.DeleteByOwner(ctx, shareID, ownerMailboxID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("delete share: %w", err)
	}
	return nil
}

// sameAccount reports whether both mailboxes' domains belong to the same
// panel user.
func sameAccount(ctx context.Context, d Deps, a, b *models.Mailbox) (bool, error) {
	if a.DomainID == b.DomainID {
		return true, nil
	}
	da, err := d.Domains.FindByID(ctx, a.DomainID)
	if err != nil {
		return false, fmt.Errorf("load owner domain: %w", err)
	}
	db, err := d.Domains.FindByID(ctx, b.DomainID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("load target domain: %w", err)
	}
	return da.UserID != "" && da.UserID == db.UserID, nil
}
