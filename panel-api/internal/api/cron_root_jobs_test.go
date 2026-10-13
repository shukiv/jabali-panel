package api

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// JAB-293 root jobs run as root through their system unit. Run now and the
// log used the owning admin's own Linux account instead (usually none: 409,
// or a 502 from the agent), so neither ever reached the job. A root row owned
// by a tenant (from before cronops held the admin-owner invariant) must never
// run as root: its command is one the tenant can edit.

const (
	adminRootJobID  = "01JR00TJ0B00000000000ADM1N"
	tenantRootJobID = "01JR00TJ0B0000000000TENANT"
)

func rootJobsRouter(t *testing.T, callerID string, admin bool, answer string) (*gin.Engine, *runNowAgent) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: callerID, IsAdmin: admin})
		c.Next()
	})
	alice := "alice"
	jobs := &statusRecordingCronJobs{stubCronJobsForAdmin: &stubCronJobsForAdmin{rows: []*models.CronJob{
		{ID: adminRootJobID, UserID: "admin1", Enabled: true, RunAsRoot: true, Command: "python3 /root/report.py"},
		{ID: tenantRootJobID, UserID: "u1", Enabled: true, RunAsRoot: true, Command: "php /home/alice/example.com/public_html/x.php"},
	}}}
	ag := &runNowAgent{answer: answer}
	RegisterCronRoutes(v1, CronHandlerConfig{
		CronJobs: jobs,
		Users: cronEditUsers{byID: map[string]*models.User{
			// The admin has no Linux account: a root job needs none.
			"admin1": {ID: "admin1", IsAdmin: true},
			"u1":     {ID: "u1", Username: &alice},
		}},
		Domains: cronEditDomains{byUser: map[string][]models.Domain{
			"u1": {{Name: "example.com", DocRoot: "/home/alice/example.com/public_html"}},
		}},
		Agent: ag,
	})
	return r, ag
}

func TestCronRunNow_AdminRootJobRunsAsRoot(t *testing.T) {
	r, ag := rootJobsRouter(t, "admin1", true, `{"exit_code":0,"output":"report written"}`)
	w := sendJSON(r, http.MethodPost, "/api/v1/cron/"+adminRootJobID+"/run-now", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", w.Code, w.Body.String())
	}
	if ag.params["username"] != "root" || ag.params["run_as_root"] != true {
		t.Fatalf("a root job must reach the agent as root with run_as_root; got %v", ag.params)
	}
}

func TestCronLog_AdminRootJobReadsTheRootLog(t *testing.T) {
	r, ag := rootJobsRouter(t, "admin1", true, `{"log":"report written"}`)
	w := sendJSON(r, http.MethodGet, "/api/v1/cron/"+adminRootJobID+"/log", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", w.Code, w.Body.String())
	}
	if ag.params["username"] != "root" || ag.params["run_as_root"] != true {
		t.Fatalf("a root job's log must be asked for as root with run_as_root; got %v", ag.params)
	}
}

func TestCronRunNowAndLog_TenantOwnedRootRowNeverRunsAsRoot(t *testing.T) {
	for _, tc := range []struct {
		name, method, path string
	}{
		{"run-now", http.MethodPost, "/api/v1/cron/" + tenantRootJobID + "/run-now"},
		{"log", http.MethodGet, "/api/v1/cron/" + tenantRootJobID + "/log"},
	} {
		for _, caller := range []struct {
			id    string
			admin bool
		}{{"u1", false}, {"admin1", true}} {
			t.Run(tc.name+"/"+caller.id, func(t *testing.T) {
				r, ag := rootJobsRouter(t, caller.id, caller.admin, `{"exit_code":0}`)
				w := sendJSON(r, tc.method, tc.path, nil)
				if w.Code != http.StatusUnprocessableEntity {
					t.Fatalf("status: got %d want 422; body=%s", w.Code, w.Body.String())
				}
				if got := errorCode(t, w); got != "root_cron_owner_not_admin" {
					t.Errorf("error: got %q", got)
				}
				if ag.params != nil {
					t.Fatalf("the agent must not be called for a tenant-owned root row; sent %v", ag.params)
				}
			})
		}
	}
}
