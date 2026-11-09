package phases

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type countingAgent struct {
	calls int
	err   error
}

func (a *countingAgent) Call(context.Context, string, any) (json.RawMessage, error) {
	a.calls++
	if a.err != nil {
		return nil, a.err
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func newTestDisclaimerPhase(ag *countingAgent, now *time.Time) *disclaimerPhase {
	p := NewDisclaimerPhase(ag).(*disclaimerPhase)
	p.now = func() time.Time { return *now }
	return p
}

func emailDomain(name string) *models.Domain {
	return &models.Domain{Name: name, EmailEnabled: true}
}

// domain.disclaimer_apply only checks the global MTA hook (the hook reads each
// domain's disclaimer from the DB), so a pass over every domain makes one call,
// not one per domain: each call is several JMAP requests and journal lines.
func TestDisclaimerPhase_OneCallForAPass(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	ag := &countingAgent{}
	p := newTestDisclaimerPhase(ag, &now)

	for _, d := range []string{"a.example.com", "b.example.com", "c.example.com"} {
		if err := p.ReconcileDomain(context.Background(), emailDomain(d), nil); err != nil {
			t.Fatal(err)
		}
	}
	if ag.calls != 1 {
		t.Fatalf("agent called %d times for one pass, want 1", ag.calls)
	}

	now = now.Add(disclaimerRecheck)
	if err := p.ReconcileDomain(context.Background(), emailDomain("a.example.com"), nil); err != nil {
		t.Fatal(err)
	}
	if ag.calls != 2 {
		t.Fatalf("agent called %d times after the recheck interval, want 2", ag.calls)
	}
}

// A failed call isn't remembered: the next domain tries again.
func TestDisclaimerPhase_RetriesAfterAFailure(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	ag := &countingAgent{err: errors.New("agent down")}
	p := newTestDisclaimerPhase(ag, &now)

	if err := p.ReconcileDomain(context.Background(), emailDomain("a.example.com"), nil); err == nil {
		t.Fatal("want the agent's error")
	}
	ag.err = nil
	if err := p.ReconcileDomain(context.Background(), emailDomain("b.example.com"), nil); err != nil {
		t.Fatal(err)
	}
	if ag.calls != 2 {
		t.Fatalf("agent called %d times, want a retry after the failure", ag.calls)
	}
}

func TestDisclaimerPhase_SkipsDomainsWithoutEmail(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	ag := &countingAgent{}
	p := newTestDisclaimerPhase(ag, &now)

	if err := p.ReconcileDomain(context.Background(), &models.Domain{Name: "a.example.com"}, nil); err != nil {
		t.Fatal(err)
	}
	if ag.calls != 0 {
		t.Fatalf("agent called %d times for a domain without email", ag.calls)
	}
}
