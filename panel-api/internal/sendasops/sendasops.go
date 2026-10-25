// Package sendasops keeps Stalwart's send-as rule in step with the panel's
// send-as grants (GH #347, GH #2267).
//
// mailbox_send_delegations is the truth. Every push sends the full set of
// pairs to the agent (mail.sendas.reconcile), which rebuilds Stalwart's
// mustMatchSender expression and reloads its settings.
//
// The Send As tab pushes on every grant change. The set also changes with no
// grant change: a mailbox is enabled, disabled or deleted, or its domain is
// renamed. Run covers those. It pushes when the set differs from the last one
// the agent took in full, and again after any failed push, so a reload that
// failed after the expression was stored is retried too.
package sendasops

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

const (
	// TickInterval is how often Run checks the set. A check that finds no
	// change is one query and no agent call.
	TickInterval = time.Minute
	// pushTimeout bounds one agent call (stalwart-cli update + reload).
	pushTimeout = 20 * time.Second
)

// PairLister is the subset of MailboxSendDelegationRepository a Syncer reads.
type PairLister interface {
	ListAllPairs(ctx context.Context) ([]models.SendAsPair, error)
}

// Syncer serialises every push. Reading the pairs and pushing them happen
// under one lock, so a push of an older set can't land after a newer one.
type Syncer struct {
	pairs PairLister
	agent agent.AgentInterface
	// lock has one slot, so a request waiting for it can give up when its
	// context ends.
	lock chan struct{}
	// applied is true when last is the set the agent last took in full.
	applied bool
	last    []models.SendAsPair
}

// New returns nil when either dependency is missing; a nil Syncer does nothing.
func New(pairs PairLister, ag agent.AgentInterface) *Syncer {
	if pairs == nil || ag == nil {
		return nil
	}
	return &Syncer{pairs: pairs, agent: ag, lock: make(chan struct{}, 1)}
}

// Push sends the current set whether or not it changed. The Send As tab uses
// it, so the person who made the change learns whether the mail server took it.
func (s *Syncer) Push(ctx context.Context) error {
	_, err := s.sync(ctx, true)
	return err
}

// SyncIfChanged sends the current set unless the agent already took this same
// set in full. It reports whether it called the agent.
func (s *Syncer) SyncIfChanged(ctx context.Context) (bool, error) {
	return s.sync(ctx, false)
}

func (s *Syncer) sync(ctx context.Context, force bool) (bool, error) {
	if s == nil {
		return false, nil
	}
	select {
	case s.lock <- struct{}{}:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	defer func() { <-s.lock }()

	pairs, err := s.pairs.ListAllPairs(ctx)
	if err != nil {
		return false, fmt.Errorf("list pairs: %w", err)
	}
	// ListAllPairs is ordered, so the same set compares equal.
	if !force && s.applied && slices.Equal(pairs, s.last) {
		return false, nil
	}
	cctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()
	if _, err := s.agent.Call(cctx, "mail.sendas.reconcile", map[string]any{"pairs": pairs}); err != nil {
		s.applied = false
		return true, err
	}
	s.applied = true
	s.last = pairs
	return true, nil
}

// Run pushes once at start (a fresh install, a restored DB or a reset Stalwart
// gets the current set), then checks every TickInterval until ctx ends. It logs
// each push, and a failure once until a push goes through again.
func (s *Syncer) Run(ctx context.Context, log *slog.Logger) {
	if s == nil {
		return
	}
	if log == nil {
		log = slog.Default()
	}
	failing := false
	check := func() {
		pushed, err := s.SyncIfChanged(ctx)
		switch {
		case err != nil:
			if ctx.Err() == nil && !failing {
				log.Warn("sendas sync: the mail server did not take the send-as list; retrying every minute", "err", err)
			}
			failing = true
		case pushed:
			log.Info("sendas sync: the mail server has the current send-as list")
			failing = false
		}
	}
	check()
	t := time.NewTicker(TickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			check()
		}
	}
}
