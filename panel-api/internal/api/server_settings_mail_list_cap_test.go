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

// GH #1833: the admin caps how many outside addresses one distribution list
// may hold.

func patchMailListCap(t *testing.T, repo *mockServerSettingsRepo, v any) *httptest.ResponseRecorder {
	t.Helper()
	r := settingsRouter(true, repo, agent.NewMockClient())
	body, _ := json.Marshal(map[string]any{"mail_list_external_member_cap": v})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestServerSettingsPatch_MailListExternalMemberCap(t *testing.T) {
	t.Parallel()
	for _, v := range []int{0, 10, 1000} {
		repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1, SSHPort: 22, MailListExternalMemberCap: 25}}
		rec := patchMailListCap(t, repo, v)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, uint16(v), repo.getResult.MailListExternalMemberCap, "cap %d must be saved", v)
	}
}

func TestServerSettingsPatch_MailListExternalMemberCapOverMaxRefused(t *testing.T) {
	t.Parallel()
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1, SSHPort: 22, MailListExternalMemberCap: 25}}
	rec := patchMailListCap(t, repo, 1001)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "invalid_mail_list_external_member_cap")
	require.Equal(t, uint16(25), repo.getResult.MailListExternalMemberCap, "a refused cap must not be saved")
}

// A negative number does not fit the unsigned field: the bind refuses it.
func TestServerSettingsPatch_MailListExternalMemberCapNegativeRefused(t *testing.T) {
	t.Parallel()
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1, SSHPort: 22, MailListExternalMemberCap: 25}}
	rec := patchMailListCap(t, repo, -1)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, uint16(25), repo.getResult.MailListExternalMemberCap)
}
