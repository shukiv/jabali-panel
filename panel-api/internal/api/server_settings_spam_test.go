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

// GH #2017: the admin sets the mail server's spam thresholds in Server
// Settings → Email. The three are checked together, after the PATCH is
// merged with the stored values.

func spamPatch(t *testing.T, stored *models.ServerSettings, body map[string]any) (*httptest.ResponseRecorder, *mockServerSettingsRepo) {
	t.Helper()
	repo := &mockServerSettingsRepo{getResult: stored}
	r := settingsRouter(true, repo, agent.NewMockClient())
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec, repo
}

func spamStored() *models.ServerSettings {
	return &models.ServerSettings{ID: 1, SSHPort: 22, SpamJunkScore: 5, SpamRejectScore: 15, SpamDiscardScore: 20}
}

func TestServerSettingsPatch_SpamScores_Saved(t *testing.T) {
	t.Parallel()
	rec, repo := spamPatch(t, spamStored(), map[string]any{"spam_junk_score": 8.5})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := repo.getResult
	require.Equal(t, []float64{8.5, 15, 20}, []float64{got.SpamJunkScore, got.SpamRejectScore, got.SpamDiscardScore})

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 8.5, resp["spam_junk_score"])
	require.Equal(t, 15.0, resp["spam_reject_score"])
	require.Equal(t, 20.0, resp["spam_discard_score"])
}

// 0 turns rejecting (or discarding) off.
func TestServerSettingsPatch_SpamScores_TurnOff(t *testing.T) {
	t.Parallel()
	rec, repo := spamPatch(t, spamStored(), map[string]any{"spam_reject_score": 0, "spam_discard_score": 0})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Zero(t, repo.getResult.SpamRejectScore)
	require.Zero(t, repo.getResult.SpamDiscardScore)
}

// Raising all three at once is checked as a whole: a field-by-field check
// against the stored values would refuse a junk threshold above the old
// reject threshold.
func TestServerSettingsPatch_SpamScores_MovedTogether(t *testing.T) {
	t.Parallel()
	rec, repo := spamPatch(t, spamStored(), map[string]any{"spam_junk_score": 20, "spam_reject_score": 30, "spam_discard_score": 40})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []float64{20, 30, 40}, []float64{repo.getResult.SpamJunkScore, repo.getResult.SpamRejectScore, repo.getResult.SpamDiscardScore})
}

func TestServerSettingsPatch_SpamScores_Refused(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]map[string]any{
		"junk zero":                {"spam_junk_score": 0},
		"junk negative":            {"spam_junk_score": -2},
		"junk above 100":           {"spam_junk_score": 101},
		"junk above stored reject": {"spam_junk_score": 16},
		"reject below junk":        {"spam_reject_score": 4},
		"discard above 100":        {"spam_discard_score": 150},
		"discard equal to junk":    {"spam_discard_score": 5},
	} {
		rec, repo := spamPatch(t, spamStored(), body)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "%s: %s", name, rec.Body.String())
		var resp map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), name)
		require.Equal(t, "validation_failed", resp["error"], name)
		got := repo.getResult
		require.Equal(t, []float64{5, 15, 20}, []float64{got.SpamJunkScore, got.SpamRejectScore, got.SpamDiscardScore}, "%s: nothing saved", name)
	}
}

// A PATCH that does not name the thresholds leaves them alone.
func TestServerSettingsPatch_SpamScores_Untouched(t *testing.T) {
	t.Parallel()
	rec, repo := spamPatch(t, spamStored(), map[string]any{"dkim2_signing_enabled": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := repo.getResult
	require.Equal(t, []float64{5, 15, 20}, []float64{got.SpamJunkScore, got.SpamRejectScore, got.SpamDiscardScore})
}
