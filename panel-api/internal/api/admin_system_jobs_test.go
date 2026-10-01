package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/systemjobs"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type fakeScheduleLister struct{ rows []models.BackupSchedule }

func (f fakeScheduleLister) List(context.Context) ([]models.BackupSchedule, error) {
	return f.rows, nil
}

func newSystemJobsRouter(t *testing.T, ag agent.AgentInterface, sched systemJobsScheduleLister, isAdmin bool, rateLimit gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "admin-1", IsAdmin: isAdmin})
		c.Next()
	})
	cfg := AdminSystemJobsHandlerConfig{Agent: ag, RunRateLimit: rateLimit}
	if sched != nil {
		cfg.Schedules = sched
	}
	RegisterAdminSystemJobsRoutes(v1, cfg)
	return r
}

func serveSystemJobs(r *gin.Engine, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w
}

func TestSystemJobsList_RowsFromCatalogAndAgentState(t *testing.T) {
	ag := agent.NewMockClient().On("system.jobs_list", systemjobs.ListResponse{Jobs: []systemjobs.State{
		{ID: "aide-check", TimerActive: "active", TimerEnabled: "enabled", ServiceActive: "inactive", Result: "success",
			LastStartedAt: "2026-10-01T04:40:06Z", LastFinishedAt: "2026-10-01T04:42:27Z", NextRunAt: "2026-10-02T04:32:07Z",
			Calendar: []string{"*-*-* 04:30:00 UTC"}},
		{ID: "panel-update", TimerActive: "inactive", TimerEnabled: "disabled", ServiceActive: "inactive", Result: "success",
			Calendar: []string{"*-*-* 04:30:00"}},
		{ID: "malware-scan", TimerActive: "active", ServiceActive: "activating", Result: "success", LastStartedAt: "2026-10-01T15:00:00Z"},
		{ID: "from-a-newer-agent", TimerActive: "active"},
	}})
	next := time.Date(2026, 10, 2, 2, 0, 0, 0, time.UTC)
	tenant := "tenant-1"
	sched := fakeScheduleLister{rows: []models.BackupSchedule{
		{ID: "S1", Kind: models.BackupScheduleKindSystem, CronExpr: "0 2 * * *", Enabled: true, NextRunAt: &next},
		{ID: "S2", Kind: models.BackupScheduleKindAccount, Cadence: "daily", Enabled: false, NextRunAt: &next},
		{ID: "S3", Kind: models.BackupScheduleKindAccount, UserID: &tenant, Cadence: "daily", Enabled: true},
	}}
	w := serveSystemJobs(newSystemJobsRouter(t, ag, sched, true, nil), http.MethodGet, "/v1/admin/system-jobs")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var body struct {
		Data  []systemJobRow `json:"data"`
		Total int            `json:"total"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	byID := map[string]systemJobRow{}
	for _, r := range body.Data {
		byID[r.ID] = r
	}
	assert.Equal(t, 5, body.Total, "3 known jobs + 2 server-wide schedules; unknown job and tenant schedule left out")
	assert.NotContains(t, byID, "from-a-newer-agent")
	assert.NotContains(t, byID, "backup-schedule-S3", "a tenant's own schedule is a tenant job")

	aide := byID["aide-check"]
	assert.Equal(t, "File integrity check (AIDE)", aide.Label)
	assert.Equal(t, "Daily at 04:30 UTC", aide.Schedule)
	assert.Equal(t, "scheduled", aide.Status)
	assert.Equal(t, "success", aide.LastResult)
	require.NotNil(t, aide.LastRunAt)
	assert.Equal(t, "2026-10-01T04:42:27Z", *aide.LastRunAt, "last run is when it finished")
	assert.True(t, aide.CanRunNow)
	assert.True(t, aide.HasLog)

	upd := byID["panel-update"]
	assert.Equal(t, "disabled", upd.Status)
	assert.False(t, upd.CanRunNow)
	assert.Equal(t, "updates", upd.ManagedBy)

	scan := byID["malware-scan"]
	assert.Equal(t, "running", scan.Status)
	assert.Equal(t, "running", scan.LastResult)

	sys := byID["backup-schedule-S1"]
	assert.Equal(t, "System backup", sys.Label)
	assert.Equal(t, "0 2 * * *", sys.Schedule)
	assert.Equal(t, "cron", sys.ScheduleFormat)
	assert.Equal(t, "scheduled", sys.Status)
	assert.Equal(t, "backups", sys.ManagedBy)
	assert.False(t, sys.CanRunNow)
	assert.False(t, sys.HasLog)
	require.NotNil(t, sys.NextRunAt)

	off := byID["backup-schedule-S2"]
	assert.Equal(t, "Daily", off.Schedule)
	assert.Equal(t, "disabled", off.Status)
	assert.Nil(t, off.NextRunAt, "a disabled schedule has no next run")
}

func TestSystemJobsRun_StartsAllowedJob(t *testing.T) {
	ag := agent.NewMockClient().On("system.job_run", systemjobs.RunResponse{Started: true})
	w := serveSystemJobs(newSystemJobsRouter(t, ag, nil, true, nil), http.MethodPost, "/v1/admin/system-jobs/retention-sweep/run")
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	calls := ag.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "system.job_run", calls[0].Command)
	assert.JSONEq(t, `{"id":"retention-sweep"}`, string(calls[0].Params))
}

func TestSystemJobsRun_RefusedBeforeTheAgent(t *testing.T) {
	cases := []struct {
		path string
		code int
		err  string
	}{
		{"/v1/admin/system-jobs/panel-update/run", http.StatusUnprocessableEntity, "run_now_not_allowed"},
		{"/v1/admin/system-jobs/os-updates/run", http.StatusUnprocessableEntity, "run_now_not_allowed"},
		{"/v1/admin/system-jobs/sso-cleanup/run", http.StatusUnprocessableEntity, "run_now_not_allowed"},
		{"/v1/admin/system-jobs/nope/run", http.StatusNotFound, "unknown_system_job"},
		{"/v1/admin/system-jobs/jabali-panel.service/run", http.StatusBadRequest, "invalid_system_job_id"},
	}
	for _, tc := range cases {
		ag := agent.NewMockClient().On("system.job_run", systemjobs.RunResponse{Started: true})
		w := serveSystemJobs(newSystemJobsRouter(t, ag, nil, true, nil), http.MethodPost, tc.path)
		assert.Equal(t, tc.code, w.Code, tc.path)
		assert.Contains(t, w.Body.String(), tc.err, tc.path)
		assert.Empty(t, ag.Calls(), "%s must not reach the agent", tc.path)
	}
}

func TestSystemJobsRun_AgentOutcomes(t *testing.T) {
	ag := agent.NewMockClient().On("system.job_run", systemjobs.RunResponse{AlreadyRunning: true})
	w := serveSystemJobs(newSystemJobsRouter(t, ag, nil, true, nil), http.MethodPost, "/v1/admin/system-jobs/malware-scan/run")
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "already_running")

	ag = agent.NewMockClient().OnError("system.job_run", &agent.AgentError{Code: agent.CodeNotFound, Message: "not installed"})
	w = serveSystemJobs(newSystemJobsRouter(t, ag, nil, true, nil), http.MethodPost, "/v1/admin/system-jobs/hostname-heartbeat/run")
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "system_job_not_installed")
}

func TestSystemJobsRun_RateLimitApplies(t *testing.T) {
	ag := agent.NewMockClient().On("system.job_run", systemjobs.RunResponse{Started: true})
	limit := func(c *gin.Context) {
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "rate_limited"})
	}
	w := serveSystemJobs(newSystemJobsRouter(t, ag, nil, true, limit), http.MethodPost, "/v1/admin/system-jobs/retention-sweep/run")
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Empty(t, ag.Calls())
}

func TestSystemJobsLog_ClampsLines(t *testing.T) {
	for query, want := range map[string]int{"": 200, "?lines=50": 50, "?lines=99999": 500, "?lines=abc": 200} {
		ag := agent.NewMockClient().On("system.job_log", systemjobs.LogResponse{Log: "ok\n", Lines: want})
		w := serveSystemJobs(newSystemJobsRouter(t, ag, nil, true, nil), http.MethodGet, "/v1/admin/system-jobs/aide-check/log"+query)
		require.Equal(t, http.StatusOK, w.Code, query)
		var p systemjobs.LogParams
		require.NoError(t, json.Unmarshal(ag.Calls()[0].Params, &p))
		assert.Equal(t, systemjobs.LogParams{ID: "aide-check", Lines: want}, p, query)
	}
}

func TestSystemJobs_AdminOnly(t *testing.T) {
	for _, req := range [][2]string{
		{http.MethodGet, "/v1/admin/system-jobs"},
		{http.MethodPost, "/v1/admin/system-jobs/retention-sweep/run"},
		{http.MethodGet, "/v1/admin/system-jobs/aide-check/log"},
	} {
		ag := agent.NewMockClient()
		w := serveSystemJobs(newSystemJobsRouter(t, ag, nil, false, nil), req[0], req[1])
		assert.Equal(t, http.StatusForbidden, w.Code, req[1])
		assert.Empty(t, ag.Calls(), req[1])
	}
}
