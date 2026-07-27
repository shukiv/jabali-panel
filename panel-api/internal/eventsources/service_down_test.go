package eventsources

import "testing"

// GH: 57 false service.down alarms fired during an AppArmor enforce-flip when
// `systemctl is-active` transiently returned EMPTY for every monitored unit.
// An empty (undeterminable) state must NOT be treated as an outage.
func TestServiceStateNotDown(t *testing.T) {
	notDown := []string{"", "active", "activating", "deactivating", "reloading"}
	for _, s := range notDown {
		if !serviceStateNotDown(s) {
			t.Errorf("state %q should be treated as not-down (skip), but was flagged as down", s)
		}
	}
	down := []string{"inactive", "failed", "not-found"}
	for _, s := range down {
		if serviceStateNotDown(s) {
			t.Errorf("state %q is a real outage signal and must not be skipped", s)
		}
	}
}

// TestEnabledStateSkips guards GH #726: a Custom install without mail/DNS/
// PostgreSQL leaves those units absent, so `systemctl is-enabled` returns
// "not-found". The monitor must SKIP them (not flood the inbox with
// service.down for modules the operator never installed), while still firing
// for a genuinely enabled unit that's down.
func TestEnabledStateSkips(t *testing.T) {
	// uninstalled / operator-off / transient-check-fail -> skip (no alert)
	for _, s := range []string{
		"", "not-found", "disabled", "static", "masked",
		"indirect", "alias", "linked-runtime", "transient",
	} {
		if !enabledStateSkips(s) {
			t.Errorf("enabledStateSkips(%q) = false, want true (uninstalled/disabled must not alert)", s)
		}
	}
	// genuinely enabled (should be running) -> DO fire on inactive
	for _, s := range []string{"enabled", "enabled-runtime", "generated"} {
		if enabledStateSkips(s) {
			t.Errorf("enabledStateSkips(%q) = true, want false (an enabled-but-down unit must alert)", s)
		}
	}
}
