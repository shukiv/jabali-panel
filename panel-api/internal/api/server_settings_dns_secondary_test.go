package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #2131: PATCH /admin/settings takes dns_secondary_ips, stores it in one
// form, and refuses an entry that is not a plain, routable IP address.

func patchSecondaries(t *testing.T, value string, upsertErr error) (*httptest.ResponseRecorder, *mockServerSettingsRepo) {
	t.Helper()
	mockRepo := &mockServerSettingsRepo{
		getResult: &models.ServerSettings{ID: 1, Hostname: "example.com", SSHPort: 22, DNSSecondaryIPs: "198.51.100.9"},
		upsertErr: upsertErr,
	}
	r := settingsRouter(true, mockRepo, agent.NewMockClient())
	body, _ := json.Marshal(map[string]any{"dns_secondary_ips": value})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec, mockRepo
}

func TestServerSettingsPatch_DNSSecondaryIPs_Normalized(t *testing.T) {
	rec, repo := patchSecondaries(t, " 192.0.2.53,  2001:DB8::0:53\n10.0.0.5 ", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "192.0.2.53,2001:db8::53,10.0.0.5", repo.getResult.DNSSecondaryIPs)
	var resp models.ServerSettings
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "192.0.2.53,2001:db8::53,10.0.0.5", resp.DNSSecondaryIPs)
}

func TestServerSettingsPatch_DNSSecondaryIPs_Invalid(t *testing.T) {
	// A save would fail with 500 here, so a 400 means the list was refused
	// before anything was persisted.
	rec, _ := patchSecondaries(t, "192.0.2.53, 127.0.0.1", errors.New("must not persist"))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "invalid_settings", resp["error"])
	assert.Contains(t, resp["detail"], "dns_secondary_ips")
}

func TestServerSettingsPatch_DNSSecondaryIPs_Cleared(t *testing.T) {
	rec, repo := patchSecondaries(t, "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "", repo.getResult.DNSSecondaryIPs)
}
