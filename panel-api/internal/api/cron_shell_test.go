package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/cronvalidate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #2290: Run now on a job that runs the account's shell script tells the
// agent the package includes SSH, and without SSH it is refused before the
// agent is asked.

const shellJobID = "01JR00TJ0B00000000000SHELL"

func shellRunNowRouter(t *testing.T, ssh bool) (*gin.Engine, *runNowAgent) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
		c.Next()
	})
	alice, pkg := "alice", "p1"
	jobs := &statusRecordingCronJobs{stubCronJobsForAdmin: &stubCronJobsForAdmin{rows: []*models.CronJob{
		{ID: shellJobID, UserID: "u1", Enabled: true, Command: "bash /home/alice/bin/backup.sh"},
	}}}
	ag := &runNowAgent{answer: `{"exit_code":0,"output":"done"}`}
	RegisterCronRoutes(v1, CronHandlerConfig{
		CronJobs: jobs,
		Users:    cronEditUsers{byID: map[string]*models.User{"u1": {ID: "u1", Username: &alice, PackageID: &pkg}}},
		Domains:  cronEditDomains{byUser: map[string][]models.Domain{}},
		Packages: &fakePkgRepo{pkg: &models.HostingPackage{ID: pkg, SSHEnabled: ssh}},
		Agent:    ag,
	})
	return r, ag
}

func TestCronRunNow_ShellScriptWithSSH(t *testing.T) {
	r, ag := shellRunNowRouter(t, true)
	w := sendJSON(r, http.MethodPost, "/api/v1/cron/"+shellJobID+"/run-now", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", w.Code, w.Body.String())
	}
	if ag.params["allow_shell_scripts"] != true {
		t.Fatalf("agent params lack allow_shell_scripts: %v", ag.params)
	}
}

func TestCronRunNow_ShellScriptWithoutSSH(t *testing.T) {
	r, ag := shellRunNowRouter(t, false)
	w := sendJSON(r, http.MethodPost, "/api/v1/cron/"+shellJobID+"/run-now", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Code != cronvalidate.ErrCodeShellNeedsSSH {
		t.Errorf("code: got %q want %q", body.Code, cronvalidate.ErrCodeShellNeedsSSH)
	}
	if ag.params != nil {
		t.Fatalf("the agent must not be asked; sent %v", ag.params)
	}
}
