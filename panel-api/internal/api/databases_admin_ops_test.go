package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeDBAdmin records audit rows; everything else is a no-op stub so it
// satisfies repository.DBAdminRepository.
type fakeDBAdmin struct {
	audits    []models.DBAdminAudit
	createErr error
	created   []*models.DBAdminJob
}

func (f *fakeDBAdmin) Audit(_ context.Context, a models.DBAdminAudit) error {
	f.audits = append(f.audits, a)
	return nil
}
func (f *fakeDBAdmin) ListTuning(context.Context, string) ([]models.DBTuningSetting, error) {
	return nil, nil
}
func (f *fakeDBAdmin) ListAllTuning(context.Context) ([]models.DBTuningSetting, error) {
	return nil, nil
}
func (f *fakeDBAdmin) UpsertTuning(context.Context, string, string, string) error { return nil }
func (f *fakeDBAdmin) MarkTuningApplied(context.Context, string, string, time.Time) error {
	return nil
}
func (f *fakeDBAdmin) CreateJob(_ context.Context, j *models.DBAdminJob) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.created = append(f.created, j)
	return nil
}
func (f *fakeDBAdmin) FinishJob(context.Context, string, string, string) error { return nil }
func (f *fakeDBAdmin) GetJob(context.Context, string) (*models.DBAdminJob, error) {
	return nil, repository.ErrNotFound
}
func (f *fakeDBAdmin) RunningJob(context.Context, string) (*models.DBAdminJob, error) {
	return nil, repository.ErrNotFound
}

type stubAgent struct{ fail bool }

func (s stubAgent) Call(_ context.Context, _ string, _ any) (json.RawMessage, error) {
	if s.fail {
		return nil, errors.New("agent down")
	}
	return json.RawMessage(`{"ok":true}`), nil
}

// The deepening invariant (ADR-0099 review): every privileged agent
// action audits BOTH outcomes. Pre-extraction the timeout+Call+audit
// dance was copy-pasted across 6 handlers with audit-on-failure
// applied inconsistently — the bug class the M46 smoke campaign hit.
func TestRunAgentAction_AuditsBothOutcomes(t *testing.T) {
	mk := func(fail bool) (*databaseAdminOpsHandler, *fakeDBAdmin) {
		fa := &fakeDBAdmin{}
		h := &databaseAdminOpsHandler{cfg: DatabaseAdminOpsHandlerConfig{
			Agent:   stubAgent{fail: fail},
			DBAdmin: fa,
			Log:     slog.Default(),
		}}
		return h, fa
	}

	// success → exactly one audit row, outcome "ok"
	h, fa := mk(false)
	raw, err := h.runAgentAction(context.Background(), agentAction{
		Actor: "u1", Engine: "mariadb", Action: "root_password.rotate",
		Cmd: "db.root.set_password", Params: map[string]any{"x": 1},
	})
	if err != nil || raw == nil {
		t.Fatalf("ok path: err=%v raw=%v", err, raw)
	}
	if len(fa.audits) != 1 || fa.audits[0].Outcome != "ok" || fa.audits[0].Action != "root_password.rotate" {
		t.Fatalf("ok path audit wrong: %+v", fa.audits)
	}

	// failure → error returned AND an audit row outcome "error"
	h2, fa2 := mk(true)
	_, err = h2.runAgentAction(context.Background(), agentAction{
		Actor: "u1", Engine: "mariadb", Action: "process.kill", Target: "42",
		Cmd: "db.kill", Params: map[string]any{"id": "42"},
	})
	if err == nil {
		t.Fatal("failure path must return the agent error")
	}
	if len(fa2.audits) != 1 || fa2.audits[0].Outcome != "error" || fa2.audits[0].Target != "42" {
		t.Fatalf("failure path must audit outcome=error with target: %+v", fa2.audits)
	}
}
