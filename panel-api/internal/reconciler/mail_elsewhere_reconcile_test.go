package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type fakeElsewhereAgent struct {
	calls   [][]string
	fail    bool
	skipped bool
}

func (f *fakeElsewhereAgent) Call(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
	if method != "mail.elsewhere.sync" {
		return nil, fmt.Errorf("unexpected method %s", method)
	}
	m, _ := params.(map[string]any)
	names, _ := m["domains"].([]string)
	f.calls = append(f.calls, append([]string(nil), names...))
	if f.fail {
		return nil, fmt.Errorf("boom")
	}
	if f.skipped {
		return json.RawMessage(`{"skipped":true}`), nil
	}
	return json.RawMessage(`{"routing":true}`), nil
}

func elsewhereReconciler(agent *fakeElsewhereAgent, srv *models.ServerSettings, repo *fakeDkim2DomainRepo) *Reconciler {
	return &Reconciler{
		domains:        repo,
		serverSettings: &fakeSettingsRepo{srv: srv},
		agent:          agent,
		log:            slog.New(slog.DiscardHandler),
	}
}

// The domains whose mail is off or hosted by another provider are listed to
// the mail server; hosted ones are not. Names are lowercased, sorted and
// unique, and a name the mail server would refuse is left out.
func TestReconcileMailElsewhere_ListsNotHostedDomains(t *testing.T) {
	agent := &fakeElsewhereAgent{}
	repo := &fakeDkim2DomainRepo{rows: []models.Domain{
		{Name: "hosted.example", EmailEnabled: true, MailProvider: models.MailProviderJabali},
		{Name: "legacy.example", EmailEnabled: true, MailProvider: ""},
		{Name: "Google.Example", EmailEnabled: false, MailProvider: models.MailProviderGoogle},
		{Name: "m365.example", EmailEnabled: false, MailProvider: models.MailProviderM365},
		{Name: "off.example", EmailEnabled: false, MailProvider: models.MailProviderJabali},
		{Name: "none.example", EmailEnabled: false, MailProvider: models.MailProviderNone},
		{Name: "custom.example", EmailEnabled: false, MailProvider: models.MailProviderCustom},
		{Name: "bad name.example", EmailEnabled: false, MailProvider: models.MailProviderNone},
	}}
	r := elsewhereReconciler(agent, &models.ServerSettings{MailEnabled: true}, repo)
	r.reconcileMailElsewhere(context.Background())

	if len(agent.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(agent.calls))
	}
	got := strings.Join(agent.calls[0], ",")
	want := "custom.example,google.example,m365.example,none.example,off.example"
	if got != want {
		t.Errorf("listed %s, want %s", got, want)
	}
}

// Unchanged list: no call until the resync interval passes; a change is sent
// at once, including the change to an empty list (the last domain came home).
func TestReconcileMailElsewhere_HashGatedWithResync(t *testing.T) {
	agent := &fakeElsewhereAgent{}
	repo := &fakeDkim2DomainRepo{rows: []models.Domain{
		{Name: "moved.example", EmailEnabled: false, MailProvider: models.MailProviderGoogle},
	}}
	r := elsewhereReconciler(agent, &models.ServerSettings{MailEnabled: true}, repo)
	ctx := context.Background()

	r.reconcileMailElsewhere(ctx)
	r.reconcileMailElsewhere(ctx)
	if len(agent.calls) != 1 {
		t.Fatalf("unchanged list re-sent: calls = %d", len(agent.calls))
	}

	r.mailElsewhereAt = time.Now().Add(-mailElsewhereResync - time.Second)
	r.reconcileMailElsewhere(ctx)
	if len(agent.calls) != 2 {
		t.Fatalf("no resync after the interval: calls = %d", len(agent.calls))
	}

	repo.rows[0].EmailEnabled = true
	repo.rows[0].MailProvider = models.MailProviderJabali
	r.reconcileMailElsewhere(ctx)
	if len(agent.calls) != 3 || len(agent.calls[2]) != 0 {
		t.Fatalf("change to an empty list not sent: calls = %v", agent.calls)
	}
}

// A failed or skipped call is retried on the next tick.
func TestReconcileMailElsewhere_RetriesFailedAndSkipped(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*fakeElsewhereAgent)
	}{
		{"failed", func(a *fakeElsewhereAgent) { a.fail = true }},
		{"skipped", func(a *fakeElsewhereAgent) { a.skipped = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent := &fakeElsewhereAgent{}
			tc.set(agent)
			repo := &fakeDkim2DomainRepo{rows: []models.Domain{
				{Name: "moved.example", EmailEnabled: false, MailProvider: models.MailProviderGoogle},
			}}
			r := elsewhereReconciler(agent, &models.ServerSettings{MailEnabled: true}, repo)
			ctx := context.Background()
			r.reconcileMailElsewhere(ctx)
			r.reconcileMailElsewhere(ctx)
			if len(agent.calls) != 2 {
				t.Errorf("calls = %d, want a retry on the next tick", len(agent.calls))
			}
		})
	}
}

// A box without the mail module has no mail server to tell.
func TestReconcileMailElsewhere_MailModuleOffNoCall(t *testing.T) {
	agent := &fakeElsewhereAgent{}
	repo := &fakeDkim2DomainRepo{rows: []models.Domain{
		{Name: "moved.example", EmailEnabled: false, MailProvider: models.MailProviderGoogle},
	}}
	r := elsewhereReconciler(agent, &models.ServerSettings{MailEnabled: false}, repo)
	r.reconcileMailElsewhere(context.Background())
	if len(agent.calls) != 0 {
		t.Errorf("calls = %d, want none with mail off", len(agent.calls))
	}
}
