package models

import (
	"encoding/json"
	"testing"
)

func rawJSON(v any) *json.RawMessage {
	b, _ := json.Marshal(v)
	r := json.RawMessage(b)
	return &r
}

func TestRetentionDays_DefaultsAndOverride(t *testing.T) {
	// No config → code defaults (90 baseline, 365 security, 0 audit).
	var none ServerSettings
	if d := none.RetentionDays(RetentionCatNotifications); d != 90 {
		t.Errorf("notifications default = %d, want 90", d)
	}
	if d := none.RetentionDays(RetentionCatSecurityEvents); d != 365 {
		t.Errorf("security default = %d, want 365", d)
	}
	if d := none.RetentionDays(RetentionCatAudit); d != 0 {
		t.Errorf("audit default = %d, want 0 (keep forever)", d)
	}
	if d := none.RetentionDays("unknown-cat"); d != 90 {
		t.Errorf("unknown category = %d, want 90 fallback", d)
	}

	// Operator override wins for the configured category; others still default.
	s := ServerSettings{LogRetention: rawJSON(map[string]int{
		RetentionCatNotifications: 30,
		RetentionCatAudit:         180, // opt into pruning the audit log
	})}
	if d := s.RetentionDays(RetentionCatNotifications); d != 30 {
		t.Errorf("notifications override = %d, want 30", d)
	}
	if d := s.RetentionDays(RetentionCatAudit); d != 180 {
		t.Errorf("audit override = %d, want 180", d)
	}
	if d := s.RetentionDays(RetentionCatSecurityEvents); d != 365 {
		t.Errorf("unconfigured security = %d, want 365 default", d)
	}

	// 0 means keep forever, and an out-of-range value falls back to default.
	zero := ServerSettings{LogRetention: rawJSON(map[string]int{RetentionCatBandwidth: 0})}
	if d := zero.RetentionDays(RetentionCatBandwidth); d != 0 {
		t.Errorf("explicit 0 = %d, want 0", d)
	}
	bad := ServerSettings{LogRetention: rawJSON(map[string]int{RetentionCatUpdates: 999999})}
	if d := bad.RetentionDays(RetentionCatUpdates); d != 90 {
		t.Errorf("out-of-range = %d, want 90 default", d)
	}
}
