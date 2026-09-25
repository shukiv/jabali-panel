package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// A root cron job (run_as_root) is owned by the admin who creates it. Before
// this guard an admin API call could combine run_as_root with a tenant's
// user_id: cronops then validated the command against that TENANT's docroots
// and the job ran as uid 0 — root executing a script the tenant can edit, on a
// job the tenant can also PATCH (fetchAndAuthorize matches on user_id). The
// admin UI never sent both fields; only a raw API call could.

// recordingCronJobs records Create so the tests can prove a refused request
// never persisted a row.
type recordingCronJobs struct {
	*stubCronJobsForAdmin
	created []*models.CronJob
}

func (r *recordingCronJobs) Create(_ context.Context, j *models.CronJob) error {
	r.created = append(r.created, j)
	r.rows = append(r.rows, j)
	return nil
}

func rootOwnerRouter(t *testing.T, callerID string, callerAdmin bool, rows []*models.CronJob) (*gin.Engine, *recordingCronJobs, *cronEditAgent) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: callerID, IsAdmin: callerAdmin})
		c.Next()
	})
	alice, admin, admin2 := "alice", "adminuser", "otheradmin"
	jobs := &recordingCronJobs{stubCronJobsForAdmin: &stubCronJobsForAdmin{rows: rows}}
	ag := &cronEditAgent{}
	RegisterCronRoutes(v1, CronHandlerConfig{
		CronJobs: jobs,
		Users: cronEditUsers{byID: map[string]*models.User{
			"u1":     {ID: "u1", Username: &alice},
			"admin1": {ID: "admin1", Username: &admin, IsAdmin: true},
			"admin2": {ID: "admin2", Username: &admin2, IsAdmin: true},
		}},
		Domains: cronEditDomains{byUser: map[string][]models.Domain{
			"u1":     {{Name: "example.com", DocRoot: "/home/alice/example.com/public_html"}},
			"admin1": {{Name: "admin.test", DocRoot: "/home/adminuser/admin.test/public_html"}},
			"admin2": {{Name: "other.test", DocRoot: "/home/otheradmin/other.test/public_html"}},
		}},
		Agent: ag,
	})
	return r, jobs, ag
}

func sendJSON(r *gin.Engine, method, path string, body map[string]any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return body.Error
}

func TestCronCreate_RunAsRootOwnerIsTheCallingAdmin(t *testing.T) {
	refused := []struct {
		name string
		body map[string]any
	}{
		{"root job for a tenant", map[string]any{
			"name": "esc", "schedule": "0 3 * * *", "run_as_root": true, "user_id": "u1",
			"command": "php /home/alice/example.com/public_html/x.php",
		}},
		{"root job for another admin", map[string]any{
			"name": "other", "schedule": "0 3 * * *", "run_as_root": true, "user_id": "admin2",
			"command": "php /home/otheradmin/other.test/public_html/x.php",
		}},
	}
	for _, tc := range refused {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			r, jobs, ag := rootOwnerRouter(t, "admin1", true, nil)
			w := sendJSON(r, http.MethodPost, "/api/v1/cron", tc.body)
			if w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status: got %d want 422; body=%s", w.Code, w.Body.String())
			}
			if got := errorCode(t, w); got != "run_as_root_owner_mismatch" {
				t.Errorf("error: got %q want run_as_root_owner_mismatch", got)
			}
			if len(jobs.created) != 0 || ag.method != "" {
				t.Fatalf("refused request must not persist or reach the agent (created=%d agent=%q)", len(jobs.created), ag.method)
			}
		})
	}

	accepted := []struct {
		name string
		body map[string]any
	}{
		{"no user_id", map[string]any{
			"name": "sys", "schedule": "0 3 * * *", "run_as_root": true,
			"command": "php /home/adminuser/admin.test/public_html/x.php",
		}},
		{"user_id is the caller", map[string]any{
			"name": "sys", "schedule": "0 3 * * *", "run_as_root": true, "user_id": "admin1",
			"command": "php /home/adminuser/admin.test/public_html/x.php",
		}},
	}
	for _, tc := range accepted {
		t.Run("accepts "+tc.name, func(t *testing.T) {
			r, jobs, ag := rootOwnerRouter(t, "admin1", true, nil)
			w := sendJSON(r, http.MethodPost, "/api/v1/cron", tc.body)
			if w.Code != http.StatusCreated {
				t.Fatalf("status: got %d want 201; body=%s", w.Code, w.Body.String())
			}
			if len(jobs.created) != 1 || jobs.created[0].UserID != "admin1" || !jobs.created[0].RunAsRoot {
				t.Fatalf("want one root job owned by admin1, got %+v", jobs.created)
			}
			if ag.method != "cron.apply" || !ag.params.RunAsRoot {
				t.Errorf("root job must apply as root: method=%q run_as_root=%v", ag.method, ag.params.RunAsRoot)
			}
		})
	}
}

// A tenant-owned root row created before the guard is frozen against the
// tenant's own PATCH (the cronops owner invariant runs on Update too).
func TestCronUpdate_LegacyTenantOwnedRootJobIsFrozen(t *testing.T) {
	legacy := &models.CronJob{
		ID: "j1", UserID: "u1", Name: "esc", Schedule: "0 3 * * *",
		Command: "php /home/alice/example.com/public_html/x.php", Enabled: true, RunAsRoot: true,
	}
	r, _, ag := rootOwnerRouter(t, "u1", false, []*models.CronJob{legacy})
	w := sendJSON(r, http.MethodPatch, "/api/v1/cron/j1", map[string]any{"enabled": false})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status: got %d want 422; body=%s", w.Code, w.Body.String())
	}
	if got := errorCode(t, w); got != "root_cron_owner_not_admin" {
		t.Errorf("error: got %q want root_cron_owner_not_admin", got)
	}
	if !legacy.Enabled || ag.method != "" {
		t.Fatalf("refused update must not mutate or reach the agent (enabled=%v agent=%q)", legacy.Enabled, ag.method)
	}
}
