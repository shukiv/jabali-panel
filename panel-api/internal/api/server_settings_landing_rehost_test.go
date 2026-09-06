package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// JAB-389: a hostname change must re-point the GH#135 :443 landing vhost's
// server_name to the new FQDN via the agent, alongside system.set_hostname —
// otherwise https://<new-fqdn>/ falls to the return-444 default block. The
// dispatch is an async best-effort goroutine, so wait for it.
func TestServerSettingsPatch_HostnameChange_DispatchesLandingRehost(t *testing.T) {
	existing := &models.ServerSettings{
		ID:              1,
		Hostname:        "old.example.com",
		PublicIPv4:      "192.0.2.1",
		SSHPort:         22,
		SSHPasswordAuth: false,
	}
	mockRepo := &mockServerSettingsRepo{getResult: existing}
	mockAgent := agent.NewMockClient()
	mockAgent.On("system.set_hostname", map[string]any{"status": "ok"})
	mockAgent.On("nginx.panel_landing_rehost", map[string]any{"status": "ok"})

	r := settingsRouter(true, mockRepo, mockAgent)

	body, _ := json.Marshal(map[string]any{"hostname": "new.example.com"})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var params map[string]any
	require.Eventually(t, func() bool {
		for _, c := range mockAgent.Calls() {
			if c.Command == "nginx.panel_landing_rehost" {
				require.NoError(t, json.Unmarshal(c.Params, &params))
				return true
			}
		}
		return false
	}, 2*time.Second, 5*time.Millisecond, "nginx.panel_landing_rehost was never dispatched")

	require.Equal(t, "new.example.com", params["hostname"], "landing rehost must carry the new hostname (the DB truth)")
}

// A PATCH that does not change the hostname must NOT dispatch the landing
// rehost — the gate is the hostname transition, same as system.set_hostname.
func TestServerSettingsPatch_NoHostnameChange_SkipsLandingRehost(t *testing.T) {
	existing := &models.ServerSettings{
		ID:              1,
		Hostname:        "keep.example.com",
		PublicIPv4:      "192.0.2.1",
		SSHPort:         22,
		SSHPasswordAuth: false,
	}
	mockRepo := &mockServerSettingsRepo{getResult: existing}
	mockAgent := agent.NewMockClient()

	r := settingsRouter(true, mockRepo, mockAgent)

	// Same hostname, different benign field → hostname gate stays closed.
	body, _ := json.Marshal(map[string]any{"hostname": "keep.example.com"})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	// Give any (erroneous) async dispatch time to land, then assert it did not.
	time.Sleep(50 * time.Millisecond)
	for _, c := range mockAgent.Calls() {
		if c.Command == "nginx.panel_landing_rehost" {
			t.Fatalf("landing rehost dispatched on a no-op hostname change")
		}
	}
}
