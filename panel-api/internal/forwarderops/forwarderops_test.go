package forwarderops

import (
	"context"
	"encoding/json"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type fakeAgent struct {
	calls      int
	lastCmd    string
	lastParams map[string]any
	err        error
}

func (a *fakeAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	a.calls++
	a.lastCmd = cmd
	if m, ok := params.(map[string]any); ok {
		a.lastParams = m
	}
	return nil, a.err
}

// fakeFwdRepo overrides only ListByMailboxID; the embedded nil interface
// satisfies the rest of repository.EmailForwarderRepository (none are called).
type fakeFwdRepo struct {
	repository.EmailForwarderRepository
	rows []models.EmailForwarder
}

func (r *fakeFwdRepo) ListByMailboxID(_ context.Context, _ string, _ repository.ListOptions) ([]models.EmailForwarder, int64, error) {
	return r.rows, int64(len(r.rows)), nil
}

func strp(s string) *string { return &s }

// A nil agent (or nil repo) must be a no-op so callers without an agent handle
// — e.g. the backup scheduler — can pass nil safely.
func TestConverge_NilAgentIsNoop(t *testing.T) {
	if err := Converge(context.Background(), nil, &fakeFwdRepo{}, "mb-1", "m@d.com"); err != nil {
		t.Fatalf("nil agent must be a no-op, got %v", err)
	}
}

// Converge sends exactly the enabled forwarders, in the forwarder.apply wire
// shape: aliases as {local_part}, externals as {target, keep_copy}. Disabled
// rows are dropped.
func TestConverge_BuildsParamsAndSkipsDisabled(t *testing.T) {
	repo := &fakeFwdRepo{rows: []models.EmailForwarder{
		{Type: "alias", LocalPart: strp("sales"), Enabled: true},
		{Type: "external", Target: "keep@out.org", KeepCopy: true, Enabled: true},
		{Type: "external", Target: "off@out.org", Enabled: false}, // disabled → skipped
	}}
	ag := &fakeAgent{}

	if err := Converge(context.Background(), ag, repo, "mb-1", "notifications@example.com"); err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if ag.calls != 1 || ag.lastCmd != "forwarder.apply" {
		t.Fatalf("expected one forwarder.apply call, got calls=%d cmd=%q", ag.calls, ag.lastCmd)
	}
	if got := ag.lastParams["mailbox_email"]; got != "notifications@example.com" {
		t.Errorf("mailbox_email = %v", got)
	}
	aliases, _ := ag.lastParams["aliases"].([]map[string]string)
	if len(aliases) != 1 || aliases[0]["local_part"] != "sales" {
		t.Errorf("aliases = %#v (want one local_part=sales)", ag.lastParams["aliases"])
	}
	externals, _ := ag.lastParams["externals"].([]map[string]any)
	if len(externals) != 1 {
		t.Fatalf("externals = %#v (want exactly one — disabled dropped)", ag.lastParams["externals"])
	}
	if externals[0]["target"] != "keep@out.org" || externals[0]["keep_copy"] != true {
		t.Errorf("external = %#v (want target=keep@out.org keep_copy=true)", externals[0])
	}
}
