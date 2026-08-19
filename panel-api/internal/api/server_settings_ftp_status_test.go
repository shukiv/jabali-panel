package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// ftpStatusStub returns a canned ftp.status effective state.
type ftpStatusStub struct{ resp map[string]any }

func (s *ftpStatusStub) Call(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
	if cmd == "ftp.status" {
		return json.Marshal(s.resp)
	}
	return json.RawMessage(`{}`), nil
}

// GET /admin/settings/modules/ftp/status computes exposure + tls drift from
// desired (server_settings) vs effective (agent ftp.status) — JAB-259/260 C.
func TestFtpStatusEndpoint_Drift(t *testing.T) {
	cases := []struct {
		name         string
		enabled      bool
		allowPlain   bool
		effActive    bool
		effEnforced  bool
		effPortsOpen bool
		wantExposure bool
		wantTLS      bool
		wantPorts    bool
	}{
		{"off but still active → exposure drift", false, false, true, false, true, true, false, false},
		{"off and inactive → clean", false, false, false, false, false, false, false, false},
		{"off, inactive, ports still open → ports drift (warn)", false, false, false, false, true, false, false, true},
		{"on+secure but plaintext live → tls drift", true, false, true, false, true, false, true, false},
		{"on+secure and enforced → clean", true, false, true, true, true, false, false, false},
		{"on+plaintext-allowed, plaintext live → clean", true, true, true, false, true, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{
				ID: 1, FTPEnabled: tc.enabled, FTPAllowPlaintext: tc.allowPlain,
			}}
			ag := &ftpStatusStub{resp: map[string]any{
				"conf_exists": true, "active": tc.effActive, "ssl_enforced": tc.effEnforced,
				"masked": !tc.effActive, "ports_open": tc.effPortsOpen,
			}}
			r := settingsRouter(true, repo, ag)

			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings/modules/ftp/status", nil))
			require.Equal(t, http.StatusOK, rec.Code)

			var body struct {
				Drift struct {
					Exposure bool `json:"exposure"`
					TLS      bool `json:"tls"`
					Ports    bool `json:"ports"`
				} `json:"drift"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, tc.wantExposure, body.Drift.Exposure, "exposure")
			assert.Equal(t, tc.wantTLS, body.Drift.TLS, "tls")
			assert.Equal(t, tc.wantPorts, body.Drift.Ports, "ports")
		})
	}
}

// A nil/unreachable agent yields effective=null + no drift (never a false secure).
func TestFtpStatusEndpoint_NoAgent(t *testing.T) {
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1, FTPEnabled: false}}
	r := settingsRouter(true, repo, nil)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings/modules/ftp/status", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Effective any `json:"effective"`
		Drift     struct {
			Exposure bool `json:"exposure"`
			TLS      bool `json:"tls"`
		} `json:"drift"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Nil(t, body.Effective)
	assert.False(t, body.Drift.Exposure || body.Drift.TLS)
}
