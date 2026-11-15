package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #2501: the admin sets webmail's WOPI document editor URL. A valid https
// URL is saved and returned by GET; a plain http URL is refused 400 and the
// stored value is left unchanged; empty turns editing off.
func TestServerSettingsPatch_WebmailOfficeEditorURL(t *testing.T) {
	t.Parallel()

	mockRepo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1, SSHPort: 22}}
	r := settingsRouter(true, mockRepo, agent.NewMockClient())

	patch := func(payload map[string]any) *httptest.ResponseRecorder {
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	rec := patch(map[string]any{"webmail_office_editor_url": "https://office.example.com"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "https://office.example.com", mockRepo.getResult.WebmailOfficeEditorURL)

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
	getRec := httptest.NewRecorder()
	r.ServeHTTP(getRec, getReq)
	require.Equal(t, http.StatusOK, getRec.Code)
	var got models.ServerSettings
	require.NoError(t, json.Unmarshal(getRec.Body.Bytes(), &got))
	assert.Equal(t, "https://office.example.com", got.WebmailOfficeEditorURL)

	rec = patch(map[string]any{"webmail_office_editor_url": "http://office.example.com"})
	require.Equal(t, http.StatusBadRequest, rec.Code)
	var errBody map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errBody))
	assert.Equal(t, "invalid_settings", errBody["error"])
	assert.Equal(t, "https://office.example.com", mockRepo.getResult.WebmailOfficeEditorURL)

	rec = patch(map[string]any{"webmail_office_editor_url": ""})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "", mockRepo.getResult.WebmailOfficeEditorURL)
}
