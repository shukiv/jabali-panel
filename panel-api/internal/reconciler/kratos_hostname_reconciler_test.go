package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type fakeKratosRehostAgent struct {
	calls []map[string]any
	fail  bool
}

func (f *fakeKratosRehostAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	if method != "kratos.config.rehost" {
		return nil, nil
	}
	m, _ := params.(map[string]any)
	f.calls = append(f.calls, m)
	if f.fail {
		return nil, errors.New("agent down")
	}
	return json.RawMessage(`{"rewritten":true}`), nil
}

func kratosReconcilerFixture(host string) []byte {
	return []byte("serve:\n  public:\n    base_url: \"https://" + host + ":8443/.ory/\"\n")
}

func newKratosRehostReconciler(hostname string, read func(string) ([]byte, error), a *fakeKratosRehostAgent) *Reconciler {
	return &Reconciler{
		agent:                a,
		serverSettings:       &fakeSettingsRepo{srv: &models.ServerSettings{Hostname: hostname}},
		readKratosConfigFile: read,
		log:                  slog.New(slog.DiscardHandler),
	}
}

func TestReconcileKratosHostname_DriftDispatches(t *testing.T) {
	a := &fakeKratosRehostAgent{}
	r := newKratosRehostReconciler("new.host.com",
		func(string) ([]byte, error) { return kratosReconcilerFixture("old.example.com"), nil }, a)

	r.reconcileKratosHostname(context.Background())

	if len(a.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(a.calls))
	}
	if a.calls[0]["hostname"] != "new.host.com" {
		t.Fatalf("dispatched hostname = %v, want new.host.com", a.calls[0]["hostname"])
	}
}

func TestReconcileKratosHostname_NoDriftNoDispatch(t *testing.T) {
	a := &fakeKratosRehostAgent{}
	// kratos.yml already on the current hostname (case-insensitive).
	r := newKratosRehostReconciler("New.Host.Com",
		func(string) ([]byte, error) { return kratosReconcilerFixture("new.host.com"), nil }, a)

	r.reconcileKratosHostname(context.Background())

	if len(a.calls) != 0 {
		t.Fatalf("calls = %d, want 0 (no drift)", len(a.calls))
	}
}

func TestReconcileKratosHostname_UnreadableNoDispatch(t *testing.T) {
	a := &fakeKratosRehostAgent{}
	r := newKratosRehostReconciler("new.host.com",
		func(string) ([]byte, error) { return nil, errors.New("permission denied") }, a)

	r.reconcileKratosHostname(context.Background())

	if len(a.calls) != 0 {
		t.Fatalf("calls = %d, want 0 (unreadable config must not dispatch a rewriter)", len(a.calls))
	}
}

func TestReconcileKratosHostname_NoBaseURLNoDispatch(t *testing.T) {
	a := &fakeKratosRehostAgent{}
	r := newKratosRehostReconciler("new.host.com",
		func(string) ([]byte, error) { return []byte("serve:\n  public:\n    host: unix:/run/x.sock\n"), nil }, a)

	r.reconcileKratosHostname(context.Background())

	if len(a.calls) != 0 {
		t.Fatalf("calls = %d, want 0 (no base_url to compare)", len(a.calls))
	}
}

func TestReconcileKratosHostname_EmptyHostnameNoDispatch(t *testing.T) {
	a := &fakeKratosRehostAgent{}
	r := newKratosRehostReconciler("",
		func(string) ([]byte, error) { return kratosReconcilerFixture("old.example.com"), nil }, a)

	r.reconcileKratosHostname(context.Background())

	if len(a.calls) != 0 {
		t.Fatalf("calls = %d, want 0 (empty settings hostname)", len(a.calls))
	}
}
