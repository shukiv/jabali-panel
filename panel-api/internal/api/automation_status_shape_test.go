package api

import (
	"encoding/json"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
)

// TestAutomationStatus_NormalizedServiceHealthAndNet locks the JAB-150
// shaping: the raw "services" payload is folded into a normalized
// "service_health" array (capability-aware) and the net_telemetry leg
// surfaces as "net", while the pre-existing raw fields stay present.
func TestAutomationStatus_NormalizedServiceHealthAndNet(t *testing.T) {
	ag := agent.NewMockClient()
	ag.On("system.info", map[string]any{"hostname": "mx"})
	ag.On("system.cpu_usage", map[string]any{"percent": 12.5})
	ag.On("system.net_telemetry", map[string]any{
		"download_bps": 10000, "upload_bps": 5000,
		"packet_loss_pct": 0.5, "window_seconds": 3,
	})
	ag.On("system.service_details", map[string]any{
		"services": []map[string]any{
			{"unit": "nginx.service", "active": "active", "sub": "running", "load_state": "loaded", "uptime_seconds": 3600},
			{"unit": "mariadb.service", "active": "failed", "sub": "failed", "load_state": "loaded"},
			{"unit": "jabali-stalwart.service", "active": "activating", "sub": "start", "load_state": "loaded"},
			{"unit": "jabali-webmail.service", "active": "inactive", "sub": "dead", "load_state": "loaded"},
			// docker present in the reply but not installed here → omitted.
			{"unit": "docker.service", "active": "inactive", "sub": "dead", "load_state": "not-found"},
		},
	})

	m := newAutomationMetrics(ag)
	w := doStatus(m)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	// Raw back-compat fields still present.
	for _, k := range []string{"system", "services", "cpu", "net"} {
		if _, ok := body[k]; !ok {
			t.Errorf("expected raw field %q in payload", k)
		}
	}

	// JAB-141: orderable release identity — version (SHA), commit (full SHA),
	// and build_time (RFC3339, sortable) must all be present for Sounder.
	for _, k := range []string{"version", "commit", "build_time"} {
		if _, ok := body[k]; !ok {
			t.Errorf("expected release field %q in payload", k)
		}
	}

	var health []automationServiceHealth
	if err := json.Unmarshal(body["service_health"], &health); err != nil {
		t.Fatalf("decode service_health: %v", err)
	}
	byName := map[string]automationServiceHealth{}
	for _, h := range health {
		byName[h.Name] = h
	}

	if got := byName["web"].Status; got != "healthy" {
		t.Errorf("web status = %q, want healthy", got)
	}
	if got := byName["web"].UptimeSeconds; got != 3600 {
		t.Errorf("web uptime = %d, want 3600", got)
	}
	if got := byName["database"].Status; got != "failed" {
		t.Errorf("database status = %q, want failed", got)
	}
	if got := byName["mail"].Status; got != "degraded" {
		t.Errorf("mail (activating) status = %q, want degraded", got)
	}
	if got := byName["webmail"].Status; got != "stopped" {
		t.Errorf("webmail (inactive) status = %q, want stopped", got)
	}
	if _, ok := byName["docker"]; ok {
		t.Error("docker is not-found on this host and must be omitted (capability-aware)")
	}
	if byName["web"].LastChecked == "" {
		t.Error("each health row must carry last_checked")
	}

	// net telemetry passthrough shape.
	var net map[string]any
	if err := json.Unmarshal(body["net"], &net); err != nil {
		t.Fatalf("decode net: %v", err)
	}
	if net["download_bps"] == nil || net["packet_loss_pct"] == nil {
		t.Errorf("net telemetry missing fields: %v", net)
	}
}

// TestAutomationStatus_MalformedServicesStillEmitsRaw ensures a bad
// service_details payload doesn't drop the raw field or panic — normalized
// health is simply omitted.
func TestAutomationStatus_MalformedServicesStillEmitsRaw(t *testing.T) {
	ag := agent.NewMockClient()
	ag.On("system.info", map[string]any{"hostname": "mx"})
	ag.On("system.cpu_usage", map[string]any{"percent": 1})
	ag.On("system.net_telemetry", map[string]any{"download_bps": 0})
	ag.On("system.service_details", "not-an-object")

	m := newAutomationMetrics(ag)
	w := doStatus(m)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var body map[string]json.RawMessage
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if _, ok := body["services"]; !ok {
		t.Error("raw services must still be emitted on malformed payload")
	}
	if _, ok := body["service_health"]; ok {
		t.Error("service_health must be omitted when the payload can't be normalized")
	}
}
