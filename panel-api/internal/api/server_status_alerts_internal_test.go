package api

// Regression for feedback_go_nil_slice_json_null_spa_crash: a HEALTHY server
// (no agent errors, no failed/inactive services, no disk/load thresholds)
// produced a nil alerts slice, which marshals as JSON `null` instead of `[]`.
// The SPA guards it, but non-SPA API clients (jabali-mcp, integrations) break on
// a list op against null. synthesizeAlerts must return a non-nil empty slice.

import (
	"encoding/json"
	"strings"
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

// JAB-379: the degraded-AppArmor alert decision table. A jabali profile that is
// complain/missing/unconfined → one warning alert; enforce + kernel-gated +
// disabled + a failed agent call → nothing.
func TestSynthesizeAlerts_AppArmorDegraded(t *testing.T) {
	aaResult := func(enabled bool, profiles ...[2]string) map[string]json.RawMessage {
		type prof struct {
			Name string `json:"name"`
			Mode string `json:"mode"`
		}
		payload := struct {
			Enabled  bool   `json:"enabled"`
			Profiles []prof `json:"profiles"`
		}{Enabled: enabled}
		for _, p := range profiles {
			payload.Profiles = append(payload.Profiles, prof{Name: p[0], Mode: p[1]})
		}
		b, _ := json.Marshal(payload)
		return map[string]json.RawMessage{"apparmor": b}
	}
	// find the single apparmor alert (if any) in the result.
	findAA := func(alerts []ServerStatusAlert) *ServerStatusAlert {
		var found *ServerStatusAlert
		n := 0
		for i := range alerts {
			if alerts[i].Kind == "apparmor" {
				found = &alerts[i]
				n++
			}
		}
		if n > 1 {
			t.Fatalf("want at most one apparmor alert, got %d", n)
		}
		return found
	}

	t.Run("all enforce → no alert", func(t *testing.T) {
		got := synthesizeAlerts(aaResult(true,
			[2]string{"jabali-bulwark", "enforce"},
			[2]string{"jabali-panel", "enforce"},
		), map[string]string{})
		if a := findAA(got); a != nil {
			t.Fatalf("enforce-only must not alert, got %q", a.Detail)
		}
	})

	t.Run("one complain → warning with name+mode", func(t *testing.T) {
		got := synthesizeAlerts(aaResult(true,
			[2]string{"jabali-bulwark", "complain"},
			[2]string{"jabali-panel", "enforce"},
		), map[string]string{})
		a := findAA(got)
		if a == nil {
			t.Fatal("complain profile must raise an apparmor alert")
		}
		if a.Level != "warning" {
			t.Errorf("level: want warning, got %q", a.Level)
		}
		if !strings.Contains(a.Detail, "jabali-bulwark (complain)") {
			t.Errorf("detail must name the profile+mode, got %q", a.Detail)
		}
		if !strings.Contains(a.Detail, "1 AppArmor profile") {
			t.Errorf("detail must count 1 profile (singular), got %q", a.Detail)
		}
	})

	t.Run("missing → warning", func(t *testing.T) {
		got := synthesizeAlerts(aaResult(true,
			[2]string{"jabali-fpm-app", "missing"},
		), map[string]string{})
		if findAA(got) == nil {
			t.Fatal("a missing profile must alert")
		}
	})

	t.Run("kernel-gated only → no alert", func(t *testing.T) {
		got := synthesizeAlerts(aaResult(true,
			[2]string{"jabali-bulwark", "kernel-gated"},
			[2]string{"jabali-panel", "kernel-gated"},
		), map[string]string{})
		if a := findAA(got); a != nil {
			t.Fatalf("kernel-gated is working-as-designed, must not alert, got %q", a.Detail)
		}
	})

	t.Run("enabled=false → no alert", func(t *testing.T) {
		got := synthesizeAlerts(aaResult(false,
			[2]string{"jabali-bulwark", "complain"},
		), map[string]string{})
		if a := findAA(got); a != nil {
			t.Fatalf("no-AppArmor host must not alert, got %q", a.Detail)
		}
	})

	t.Run("failed agent call → silent (no apparmor OR agent alert)", func(t *testing.T) {
		got := synthesizeAlerts(map[string]json.RawMessage{}, map[string]string{"apparmor": "timeout"})
		for _, a := range got {
			if a.Kind == "apparmor" {
				t.Fatalf("a failed summary call must not raise an apparmor alert, got %q", a.Detail)
			}
			if a.Kind == "agent" && strings.Contains(a.Detail, "apparmor") {
				t.Fatalf("a failed summary call must not raise the generic agent alert, got %q", a.Detail)
			}
		}
	})

	t.Run("multiple degraded → sorted, counted", func(t *testing.T) {
		got := synthesizeAlerts(aaResult(true,
			[2]string{"jabali-panel", "complain"},
			[2]string{"jabali-bulwark", "missing"},
			[2]string{"stalwart-mail", "enforce"},
		), map[string]string{})
		a := findAA(got)
		if a == nil {
			t.Fatal("two degraded profiles must alert")
		}
		if !strings.Contains(a.Detail, "2 AppArmor profiles") {
			t.Errorf("want plural count of 2, got %q", a.Detail)
		}
		// sorted: jabali-bulwark before jabali-panel
		if strings.Index(a.Detail, "jabali-bulwark") > strings.Index(a.Detail, "jabali-panel") {
			t.Errorf("degraded names must be sorted, got %q", a.Detail)
		}
	})
}
