package api

import (
	"encoding/json"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// JAB-95 Phase 3: a successful warmup response stamps the run done with the
// agent's stats.
func TestFinalizeCacheWarmupRun_Success(t *testing.T) {
	run := &models.CacheWarmupRun{State: models.CacheWarmupStateRunning}
	raw, _ := json.Marshal(map[string]any{
		"requested": 100, "clamped_to": 50, "attempted": 47,
		"warmed": 45, "skipped": 0, "failed": 2, "first_error": "/x -> 500",
	})
	finalizeCacheWarmupRun(run, raw, nil)

	if run.State != models.CacheWarmupStateDone {
		t.Fatalf("state=%q, want done", run.State)
	}
	if run.ClampedTo != 50 || run.Attempted != 47 || run.Warmed != 45 || run.Failed != 2 {
		t.Errorf("stats not mapped: %+v", run)
	}
	if run.Total != 47 || run.CursorPos != 47 {
		t.Errorf("total/cursor should track attempted: total=%d cursor=%d", run.Total, run.CursorPos)
	}
	if run.FinishedAt == nil {
		t.Error("finished_at should be set")
	}
}

// A transport error stamps the run failed with the error text.
func TestFinalizeCacheWarmupRun_Error(t *testing.T) {
	run := &models.CacheWarmupRun{State: models.CacheWarmupStateRunning}
	finalizeCacheWarmupRun(run, nil, errors.New("agent unavailable"))
	if run.State != models.CacheWarmupStateFailed {
		t.Fatalf("state=%q, want failed", run.State)
	}
	if run.FirstError != "agent unavailable" {
		t.Errorf("first_error=%q", run.FirstError)
	}
	if run.FinishedAt == nil {
		t.Error("finished_at should be set")
	}
}
