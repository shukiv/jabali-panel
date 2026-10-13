package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #2122: Run now waits about 20 s. A job still running then keeps running
// in its unit; the panel answers 202 and leaves Last run alone, because the
// reconciler's harvest only writes a NEWER timestamp — a Last run written now
// (exit 0, the run not ended) would never be replaced by the real result.

// runNowAgent answers cron.run_now with a canned body and records what the
// handler sent.
type runNowAgent struct {
	answer      string
	params      map[string]any
	hadDeadline bool
	deadlineIn  time.Duration
}

func (a *runNowAgent) Call(ctx context.Context, _ string, params any) (json.RawMessage, error) {
	raw, _ := json.Marshal(params)
	_ = json.Unmarshal(raw, &a.params)
	if d, ok := ctx.Deadline(); ok {
		a.hadDeadline, a.deadlineIn = true, time.Until(d)
	}
	return json.RawMessage(a.answer), nil
}

type statusRecordingCronJobs struct {
	*stubCronJobsForAdmin
	statusCalls int
	exitCode    int
	lastError   string
}

func (s *statusRecordingCronJobs) UpdateStatus(_ context.Context, _ string, _ time.Time, exitCode int, lastError string) error {
	s.statusCalls++
	s.exitCode, s.lastError = exitCode, lastError
	return nil
}

func runNowRouter(t *testing.T, answer string) (*gin.Engine, *statusRecordingCronJobs, *runNowAgent) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
		c.Next()
	})
	alice := "alice"
	jobs := &statusRecordingCronJobs{stubCronJobsForAdmin: &stubCronJobsForAdmin{rows: []*models.CronJob{{
		ID: "01JRVNNMW0000000000000TEST", UserID: "u1", Enabled: true,
		Command: "php /home/alice/example.com/public_html/import.php",
	}}}}
	ag := &runNowAgent{answer: answer}
	RegisterCronRoutes(v1, CronHandlerConfig{
		CronJobs: jobs,
		Users:    cronEditUsers{byID: map[string]*models.User{"u1": {ID: "u1", Username: &alice}}},
		Domains: cronEditDomains{byUser: map[string][]models.Domain{
			"u1": {{Name: "example.com", DocRoot: "/home/alice/example.com/public_html"}},
		}},
		Agent: ag,
	})
	return r, jobs, ag
}

const runNowPath = "/api/v1/cron/01JRVNNMW0000000000000TEST/run-now"

func TestCronRunNow_StillRunningAnswers202AndLeavesLastRunAlone(t *testing.T) {
	r, jobs, _ := runNowRouter(t, `{"exit_code":0,"running":true}`)
	w := sendJSON(r, http.MethodPost, runNowPath, nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["running"] != true {
		t.Errorf("body must say the job is still running; got %s", w.Body.String())
	}
	if jobs.statusCalls != 0 {
		t.Fatalf("Last run must wait for the run to end (the harvest records it); UpdateStatus was called %d time(s)", jobs.statusCalls)
	}
}

func TestCronRunNow_AlreadyRunningIs409(t *testing.T) {
	r, jobs, _ := runNowRouter(t, `{"exit_code":0,"already_running":true}`)
	w := sendJSON(r, http.MethodPost, runNowPath, nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("status: got %d want 409; body=%s", w.Code, w.Body.String())
	}
	if got := errorCode(t, w); got != "already_running" {
		t.Errorf("error: got %q want already_running", got)
	}
	if jobs.statusCalls != 0 {
		t.Fatal("nothing ran, so Last run must not change")
	}
}

func TestCronRunNow_FinishedRunRecordsLastRun(t *testing.T) {
	r, jobs, ag := runNowRouter(t, `{"exit_code":3,"output":"importing\nTraceback: boom"}`)
	w := sendJSON(r, http.MethodPost, runNowPath, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["output"] != "importing\nTraceback: boom" {
		t.Errorf("the run's output must reach the caller; got %s", w.Body.String())
	}
	if jobs.statusCalls != 1 || jobs.exitCode != 3 {
		t.Fatalf("UpdateStatus: calls=%d exit=%d, want one call with exit 3", jobs.statusCalls, jobs.exitCode)
	}
	if !strings.Contains(jobs.lastError, "Traceback: boom") {
		t.Errorf("a failed run's output must become its last error; got %q", jobs.lastError)
	}
	// The handler asks for the 20 s wait and bounds the call itself, so the
	// answer beats the HTTP server's 30 s write timeout.
	if ag.params["wait_seconds"] != float64(20) {
		t.Errorf("wait_seconds: got %v want 20", ag.params["wait_seconds"])
	}
	if !ag.hadDeadline || ag.deadlineIn > 28*time.Second {
		t.Errorf("agent call deadline: had=%v in=%s, want one under 28 s", ag.hadDeadline, ag.deadlineIn)
	}
}

func TestCronRunNow_LastErrorFitsTheColumn(t *testing.T) {
	long := strings.Repeat("é", 3000) + "the end"
	raw, _ := json.Marshal(map[string]any{"exit_code": 1, "stderr": long})
	r, jobs, _ := runNowRouter(t, string(raw))
	w := sendJSON(r, http.MethodPost, runNowPath, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d; body=%s", w.Code, w.Body.String())
	}
	if n := len(jobs.lastError); n > 1024 {
		t.Fatalf("last_error is VARCHAR(1024); got %d bytes", n)
	}
	if !strings.HasSuffix(jobs.lastError, "the end") {
		t.Errorf("keep the END of the output, where the error is; got %q", jobs.lastError)
	}
	if !utf8.ValidString(jobs.lastError) {
		t.Error("trim on a character boundary; got invalid UTF-8")
	}
}
