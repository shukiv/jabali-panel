package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type fakeDkim2Agent struct {
	calls []map[string]any
	fail  bool
}

func (f *fakeDkim2Agent) Call(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
	if method != "domain_email.dkim2_set" {
		return nil, nil
	}
	m, _ := params.(map[string]any)
	f.calls = append(f.calls, m)
	if f.fail {
		return nil, fmt.Errorf("boom")
	}
	return json.RawMessage(`{"ok":true,"changed":true}`), nil
}

type fakeDkim2DomainRepo struct {
	repository.DomainRepository
	rows []models.Domain
}

func (f *fakeDkim2DomainRepo) List(context.Context, repository.ListOptions) ([]models.Domain, int64, error) {
	return f.rows, int64(len(f.rows)), nil
}

func dkim2Reconciler(agent *fakeDkim2Agent, srv *models.ServerSettings, domains []models.Domain) *Reconciler {
	return &Reconciler{
		domains:        &fakeDkim2DomainRepo{rows: domains},
		serverSettings: &fakeSettingsRepo{srv: srv},
		agent:          agent,
		log:            slog.New(slog.DiscardHandler),
	}
}

// GH #648: only mail-enabled domains converge; steady state is call-free;
// a toggle flip re-converges every mail domain exactly once.
func TestReconcileDKIM2_AppliedStateGating(t *testing.T) {
	agent := &fakeDkim2Agent{}
	srv := &models.ServerSettings{}
	r := dkim2Reconciler(agent, srv, []models.Domain{
		{Name: "mail1.example", EmailEnabled: true},
		{Name: "mail2.example", EmailEnabled: true},
		{Name: "web-only.example", EmailEnabled: false},
	})
	ctx := context.Background()

	// First tick: both mail domains converge to off; web-only untouched.
	r.reconcileDKIM2(ctx)
	if len(agent.calls) != 2 {
		t.Fatalf("first tick calls = %d, want 2", len(agent.calls))
	}
	for _, c := range agent.calls {
		if c["enabled"] != false {
			t.Fatalf("first tick must converge to disabled, got %v", c)
		}
		if c["domain_name"] == "web-only.example" {
			t.Fatal("non-mail domain must not be converged")
		}
	}

	// Steady state: no further calls.
	r.reconcileDKIM2(ctx)
	if len(agent.calls) != 2 {
		t.Fatalf("steady state made calls (total=%d)", len(agent.calls))
	}

	// Toggle on: both re-converge with enabled=true.
	srv.DKIM2SigningEnabled = true
	r.reconcileDKIM2(ctx)
	if len(agent.calls) != 4 {
		t.Fatalf("toggle flip calls = %d, want 4 total", len(agent.calls))
	}
	if agent.calls[2]["enabled"] != true || agent.calls[3]["enabled"] != true {
		t.Fatal("post-flip converge must carry enabled=true")
	}
	r.reconcileDKIM2(ctx)
	if len(agent.calls) != 4 {
		t.Fatal("second steady state made calls")
	}
}

// A failed converge stays out of the applied map and retries next tick; a
// domain whose email is later enabled converges without a toggle flip.
func TestReconcileDKIM2_RetryAndLateMailDomain(t *testing.T) {
	agent := &fakeDkim2Agent{fail: true}
	srv := &models.ServerSettings{DKIM2SigningEnabled: true}
	repo := &fakeDkim2DomainRepo{rows: []models.Domain{{Name: "mail1.example", EmailEnabled: true}}}
	r := &Reconciler{
		domains:        repo,
		serverSettings: &fakeSettingsRepo{srv: srv},
		agent:          agent,
		log:            slog.New(slog.DiscardHandler),
	}
	ctx := context.Background()

	r.reconcileDKIM2(ctx)
	agent.fail = false
	r.reconcileDKIM2(ctx)
	if len(agent.calls) != 2 {
		t.Fatalf("failed converge must retry (calls=%d)", len(agent.calls))
	}

	// New mail domain appears: converged on the next tick, existing one not re-called.
	repo.rows = append(repo.rows, models.Domain{Name: "late.example", EmailEnabled: true})
	r.reconcileDKIM2(ctx)
	if len(agent.calls) != 3 {
		t.Fatalf("late mail domain must converge exactly once (calls=%d)", len(agent.calls))
	}
	if agent.calls[2]["domain_name"] != "late.example" {
		t.Fatalf("third call = %v, want late.example", agent.calls[2])
	}
}
