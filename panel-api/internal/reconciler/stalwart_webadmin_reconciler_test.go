package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type fakeWebadminAgent struct {
	calls []map[string]any
	fail  bool
}

func (f *fakeWebadminAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	if method != "mail.webadmin.apply" {
		return nil, nil
	}
	m, _ := params.(map[string]any)
	f.calls = append(f.calls, m)
	if f.fail {
		return nil, errors.New("stalwart down")
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func newWebadminReconciler(s *models.ServerSettings, a *fakeWebadminAgent) *Reconciler {
	return &Reconciler{agent: a, serverSettings: &fakeSettingsRepo{srv: s}, log: slog.New(slog.DiscardHandler)}
}

// GH #2542: a server that opened the door before the fix gets the OAuth client
// on the first pass after the update, once.
func TestReconcileStalwartWebadmin_AppliesOncePerProcess(t *testing.T) {
	a := &fakeWebadminAgent{}
	r := newWebadminReconciler(&models.ServerSettings{Hostname: "panel.example.com", StalwartWebadminEnabled: true}, a)
	r.reconcileStalwartWebadmin(context.Background())
	r.reconcileStalwartWebadmin(context.Background())
	if len(a.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(a.calls))
	}
	c := a.calls[0]
	if c["enabled"] != true || c["server_name"] != "panel.example.com" || c["port"] != 8449 {
		t.Fatalf("params = %v", c)
	}
}

func TestReconcileStalwartWebadmin_DoorOffDispatchesNothing(t *testing.T) {
	a := &fakeWebadminAgent{}
	r := newWebadminReconciler(&models.ServerSettings{Hostname: "panel.example.com"}, a)
	r.reconcileStalwartWebadmin(context.Background())
	if len(a.calls) != 0 {
		t.Fatalf("calls = %d, want 0", len(a.calls))
	}
}

func TestReconcileStalwartWebadmin_RetriesAfterFailureAndOnHostnameChange(t *testing.T) {
	a := &fakeWebadminAgent{fail: true}
	s := &models.ServerSettings{Hostname: "panel.example.com", StalwartWebadminEnabled: true}
	r := newWebadminReconciler(s, a)
	r.reconcileStalwartWebadmin(context.Background())
	a.fail = false
	r.reconcileStalwartWebadmin(context.Background())
	if len(a.calls) != 2 {
		t.Fatalf("calls = %d, want 2 (a failed apply is retried)", len(a.calls))
	}
	s.Hostname = "new.example.com"
	r.reconcileStalwartWebadmin(context.Background())
	if len(a.calls) != 3 || a.calls[2]["server_name"] != "new.example.com" {
		t.Fatalf("calls = %v, want a third apply for the new hostname", a.calls)
	}
}
