package phases

import (
	"context"
	"fmt"
	"sync"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// disclaimerPhase keeps the disclaimer MTA hook registered with Stalwart
// (domain.disclaimer_apply). The hook reads each domain's disclaimer from the
// DB, which is truth (ADR-0051, ADR-0143).
type disclaimerPhase struct {
	agent agent.AgentInterface
	now   func() time.Time

	mu     sync.Mutex
	lastOK time.Time
}

// disclaimerRecheck is how often the phase asks the agent to check the
// disclaimer MTA hook. The check is global, not per domain.
const disclaimerRecheck = 10 * time.Minute

func NewDisclaimerPhase(ag agent.AgentInterface) Phase {
	return &disclaimerPhase{agent: ag, now: time.Now}
}

func (p *disclaimerPhase) Name() string { return "disclaimer" }

func (p *disclaimerPhase) ReconcileDomain(ctx context.Context, dom *models.Domain, _ map[string]interface{}) error {
	if p.agent == nil || dom == nil || !dom.EmailEnabled {
		return nil
	}
	// The agent only checks the global MTA hook (ADR-0143: the hook reads
	// each domain's disclaimer from the DB, and the API pushes a change at
	// once), so one successful call covers every domain until the recheck.
	// Per domain per pass it was several JMAP requests each, the bulk of
	// Stalwart's journal lines.
	p.mu.Lock()
	fresh := !p.lastOK.IsZero() && p.now().Sub(p.lastOK) < disclaimerRecheck
	p.mu.Unlock()
	if fresh {
		return nil
	}
	text := ""
	if dom.DisclaimerText != nil {
		text = *dom.DisclaimerText
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := p.agent.Call(callCtx, "domain.disclaimer_apply", map[string]any{
		"domain_name": dom.Name,
		"enabled":     dom.DisclaimerEnabled,
		"text":        text,
	}); err != nil {
		return fmt.Errorf("disclaimer: push %s: %w", dom.Name, err)
	}
	p.mu.Lock()
	p.lastOK = p.now()
	p.mu.Unlock()
	return nil
}

func (p *disclaimerPhase) ReconcileMailbox(_ context.Context, _ *models.Mailbox, _ *models.Domain, _ map[string]interface{}) error {
	return nil
}
