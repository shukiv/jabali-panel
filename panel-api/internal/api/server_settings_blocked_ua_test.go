package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func patchBlockedUA(t *testing.T, repo *mockServerSettingsRepo, list string) *httptest.ResponseRecorder {
	t.Helper()
	r := settingsRouter(true, repo, agent.NewMockClient())
	body, _ := json.Marshal(map[string]string{"blocked_user_agents": list})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// The list is stored one entry per line, trimmed, without blank lines or
// case-insensitive duplicates.
func TestServerSettingsPatch_BlockedUserAgentsNormalized(t *testing.T) {
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1, SSHPort: 22}}
	rec := patchBlockedUA(t, repo, "  python-requests \n\nzgrab\nPython-Requests\n")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "python-requests\nzgrab", repo.getResult.BlockedUserAgents)

	rec = patchBlockedUA(t, repo, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "", repo.getResult.BlockedUserAgents)
}

// An entry that could break the nginx map is refused and nothing is saved.
func TestServerSettingsPatch_BlockedUserAgentsRefused(t *testing.T) {
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1, SSHPort: 22, BlockedUserAgents: "zgrab"}}
	rec := patchBlockedUA(t, repo, "ok-bot\nx\" 1; } server {")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "invalid_blocked_user_agents")
	require.Equal(t, "zgrab", repo.getResult.BlockedUserAgents)
}

// An entry that would also refuse Let's Encrypt or every browser is refused,
// with the reason, and nothing is saved.
func TestServerSettingsPatch_BlockedUserAgentsRefusesEssentialClients(t *testing.T) {
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1, SSHPort: 22, BlockedUserAgents: "zgrab"}}
	rec := patchBlockedUA(t, repo, "AhrefsBot\ncompatible")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "invalid_blocked_user_agents")
	require.Contains(t, rec.Body.String(), "would also block")
	require.Equal(t, "zgrab", repo.getResult.BlockedUserAgents)
}
