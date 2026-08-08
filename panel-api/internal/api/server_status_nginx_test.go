package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// An nginx sub-call failure must surface as a DEDICATED critical alert, not
// the generic warning/agent sub-call line — nginx being down is a
// whole-vhost outage on the box. The aggregator polls `nginx.status`
// (systemd unit state), which errors exactly when nginx is not running;
// it deliberately does NOT run `nginx -t` on this hot path.
func TestSynthesizeAlerts_NginxInvalidIsCritical(t *testing.T) {
	errMap := map[string]string{
		"nginx": "nginx not running: state=failed",
	}
	alerts := synthesizeAlerts(map[string]json.RawMessage{}, errMap)

	var crit *ServerStatusAlert
	for i := range alerts {
		if alerts[i].Kind == "nginx" && alerts[i].Level == "critical" {
			crit = &alerts[i]
		}
		// the generic errMap loop must NOT also emit an agent warning for nginx
		if alerts[i].Kind == "agent" && alerts[i].Level == "warning" &&
			strings.Contains(alerts[i].Detail, "'nginx'") {
			t.Errorf("nginx must not produce a generic agent warning: %q", alerts[i].Detail)
		}
	}
	if crit == nil {
		t.Fatalf("expected a critical Kind=nginx alert, got %+v", alerts)
	}
	if !strings.Contains(crit.Detail, "nginx") {
		t.Errorf("critical nginx alert detail should mention nginx: %q", crit.Detail)
	}
}

// a non-nginx errMap entry still produces the generic agent warning.
func TestSynthesizeAlerts_OtherErrStillGenericWarning(t *testing.T) {
	alerts := synthesizeAlerts(map[string]json.RawMessage{}, map[string]string{"cpu": "timeout"})
	found := false
	for _, a := range alerts {
		if a.Kind == "agent" && a.Level == "warning" && strings.Contains(a.Detail, "'cpu'") {
			found = true
		}
	}
	if !found {
		t.Errorf("non-nginx sub-call error should still be a generic agent warning: %+v", alerts)
	}
}
