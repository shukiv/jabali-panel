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

type fakePageTplRepo struct {
	rows map[string]string
}

func (f *fakePageTplRepo) List(context.Context) ([]models.PageTemplate, error) { return nil, nil }
func (f *fakePageTplRepo) Get(_ context.Context, key string) (*models.PageTemplate, error) {
	if v, ok := f.rows[key]; ok {
		return &models.PageTemplate{Key: key, Content: v}, nil
	}
	return nil, repository.ErrNotFound
}
func (f *fakePageTplRepo) Upsert(context.Context, string, string) error { return nil }
func (f *fakePageTplRepo) EnsureDefaults(context.Context) (int, error)  { return 0, nil }

type fakePagesAgent struct {
	calls []map[string]any
	fail  bool
}

func (f *fakePagesAgent) Call(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
	if method != "page_templates.sync_error_pages" {
		return nil, nil
	}
	m, _ := params.(map[string]any)
	f.calls = append(f.calls, m)
	if f.fail {
		return nil, fmt.Errorf("boom")
	}
	return json.RawMessage(`{"written":[]}`), nil
}

func errorPagesReconciler(agent *fakePagesAgent, srv *models.ServerSettings) *Reconciler {
	return &Reconciler{
		pageTemplates:  &fakePageTplRepo{rows: map[string]string{models.PageTemplateError404: "<h1>custom 404</h1>"}},
		serverSettings: &fakeSettingsRepo{srv: srv},
		agent:          agent,
		log:            slog.New(slog.DiscardHandler),
	}
}

// GH #860: one sync carries the error pages, the disabled/unconfigured
// bodies AND the catch-all toggle; the hash gate makes the steady-state
// tick a noop and a toggle flip triggers exactly one more sync.
func TestReconcileErrorPages_SendsAllBodiesAndToggle(t *testing.T) {
	agent := &fakePagesAgent{}
	srv := &models.ServerSettings{}
	r := errorPagesReconciler(agent, srv)
	ctx := context.Background()

	r.reconcileErrorPages(ctx)
	if len(agent.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(agent.calls))
	}
	call := agent.calls[0]
	if call["error_404"] != "<h1>custom 404</h1>" {
		t.Fatalf("error_404 must come from the repo row, got %q", call["error_404"])
	}
	if call["domain_disabled"] != repository.DefaultPageTemplateBody(models.PageTemplateDomainDisabled) {
		t.Fatal("missing row must fall back to the compiled disabled default")
	}
	if call["domain_unconfigured"] != repository.DefaultPageTemplateBody(models.PageTemplateDomainUnconfigured) {
		t.Fatal("missing row must fall back to the compiled unconfigured default")
	}
	if call["unconfigured_enabled"] != false {
		t.Fatalf("unconfigured_enabled = %v, want false (default off)", call["unconfigured_enabled"])
	}

	// Steady state: nothing changed — the hash gate must skip the call.
	r.reconcileErrorPages(ctx)
	if len(agent.calls) != 1 {
		t.Fatalf("steady-state tick must not re-call the agent (calls=%d)", len(agent.calls))
	}

	// Admin flips the toggle: exactly one more sync, with the new value.
	srv.UnconfiguredPageEnabled = true
	r.reconcileErrorPages(ctx)
	if len(agent.calls) != 2 {
		t.Fatalf("toggle flip must trigger a sync (calls=%d)", len(agent.calls))
	}
	if agent.calls[1]["unconfigured_enabled"] != true {
		t.Fatal("second sync must carry unconfigured_enabled=true")
	}
}

// A failed sync must not cache the hash — the next tick retries.
func TestReconcileErrorPages_FailedSyncRetries(t *testing.T) {
	agent := &fakePagesAgent{fail: true}
	r := errorPagesReconciler(agent, &models.ServerSettings{})
	ctx := context.Background()

	r.reconcileErrorPages(ctx)
	agent.fail = false
	r.reconcileErrorPages(ctx)
	if len(agent.calls) != 2 {
		t.Fatalf("failed sync must retry next tick (calls=%d)", len(agent.calls))
	}
	// And once it succeeds, the gate closes.
	r.reconcileErrorPages(ctx)
	if len(agent.calls) != 2 {
		t.Fatalf("post-success tick must be a noop (calls=%d)", len(agent.calls))
	}
}
