package reconciler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeUpdateState records UpsertApt / UpsertJabali so tests can assert whether
// last-known-good counts were (over)written.
type fakeUpdateState struct {
	repository.UpdateStateRepository
	aptCalls     int
	lastAptTotal int
	lastAptSec   int
}

func (f *fakeUpdateState) Get(context.Context) (*models.UpdateState, error) {
	return &models.UpdateState{}, nil
}
func (f *fakeUpdateState) UpsertJabali(context.Context, int, string, time.Time) error { return nil }
func (f *fakeUpdateState) UpsertApt(_ context.Context, total, security int, _ time.Time) error {
	f.aptCalls++
	f.lastAptTotal, f.lastAptSec = total, security
	return nil
}
func (f *fakeUpdateState) UpsertAptStatus(context.Context, *time.Time, bool) error { return nil }

func pollReconciler(agent *fakeAgent, us *fakeUpdateState, channel string) *Reconciler {
	return &Reconciler{
		agent:          agent,
		updateState:    us,
		serverSettings: &fakeSettingsRepo{srv: &models.ServerSettings{ReleaseChannel: channel}},
		log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestUpdatePoll_ForwardsStableChannel(t *testing.T) {
	fa := &fakeAgent{resultByMethod: map[string]json.RawMessage{
		"system.update_check": json.RawMessage(`{"current_sha":"abc","behind_count":0}`),
		"system.apt_check":    json.RawMessage(`{"total":4,"security_total":1}`),
	}}
	us := &fakeUpdateState{}
	r := pollReconciler(fa, us, "stable")

	r.reconcileUpdatePoll(context.Background())

	// system.update_check must carry channel=stable.
	var chan_ string
	fa.mu.Lock()
	for _, c := range fa.calls {
		if c.method == "system.update_check" {
			if m, ok := c.params.(map[string]any); ok {
				chan_, _ = m["channel"].(string)
			}
		}
	}
	fa.mu.Unlock()
	if chan_ != "stable" {
		t.Fatalf("update_check channel = %q, want stable", chan_)
	}
	if us.aptCalls != 1 || us.lastAptTotal != 4 || us.lastAptSec != 1 {
		t.Fatalf("clean apt counts not persisted: calls=%d total=%d sec=%d", us.aptCalls, us.lastAptTotal, us.lastAptSec)
	}
}

func TestUpdatePoll_DefaultsDevelopmentChannel(t *testing.T) {
	fa := &fakeAgent{resultByMethod: map[string]json.RawMessage{
		"system.update_check": json.RawMessage(`{"current_sha":"abc","behind_count":0}`),
		"system.apt_check":    json.RawMessage(`{"total":0,"security_total":0}`),
	}}
	r := pollReconciler(fa, &fakeUpdateState{}, "development")
	r.reconcileUpdatePoll(context.Background())

	var chan_ string
	fa.mu.Lock()
	for _, c := range fa.calls {
		if c.method == "system.update_check" {
			if m, ok := c.params.(map[string]any); ok {
				chan_, _ = m["channel"].(string)
			}
		}
	}
	fa.mu.Unlock()
	if chan_ != "development" {
		t.Fatalf("update_check channel = %q, want development", chan_)
	}
}

func TestUpdatePoll_StructuredAptFailurePreservesLastKnownGood(t *testing.T) {
	fa := &fakeAgent{resultByMethod: map[string]json.RawMessage{
		"system.update_check": json.RawMessage(`{"current_sha":"abc","behind_count":0}`),
		// Structured JAB-10 failure: zero counts + an error object.
		"system.apt_check": json.RawMessage(`{"total":0,"security_total":0,"error":{"reason":"apt lock","command":"apt-get update","exit_code":100}}`),
	}}
	us := &fakeUpdateState{}
	r := pollReconciler(fa, us, "stable")

	r.reconcileUpdatePoll(context.Background())

	if us.aptCalls != 0 {
		t.Fatalf("UpsertApt was called on a structured failure (clobbers last-known-good) — calls=%d", us.aptCalls)
	}
}
