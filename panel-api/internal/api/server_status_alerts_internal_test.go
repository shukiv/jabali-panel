package api

// Regression for feedback_go_nil_slice_json_null_spa_crash: a HEALTHY server
// (no agent errors, no failed/inactive services, no disk/load thresholds)
// produced a nil alerts slice, which marshals as JSON `null` instead of `[]`.
// The SPA guards it, but non-SPA API clients (jabali-mcp, integrations) break on
// a list op against null. synthesizeAlerts must return a non-nil empty slice.

import (
	"encoding/json"
	"testing"
)

func TestSynthesizeAlerts_HealthyServerReturnsEmptyNotNil(t *testing.T) {
	got := synthesizeAlerts(map[string]json.RawMessage{}, map[string]string{})
	if got == nil {
		t.Fatal("synthesizeAlerts returned nil on a healthy server → marshals as JSON null")
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != "[]" {
		t.Errorf("healthy-server alerts must marshal as [], got %s", b)
	}
}
