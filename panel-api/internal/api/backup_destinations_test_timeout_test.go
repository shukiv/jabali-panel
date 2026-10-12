package api

// GH #2077: testing an SFTP destination came back as Cloudflare's "invalid or
// incomplete response". The test waited up to 60 s for the agent, but the
// panel's HTTP server gives a response 30 s (WriteTimeout): a test that ran
// longer (a first restic init over a slow link, an unreachable host) lost its
// answer and the connection was closed.
//
// A test that fails, or runs out of time, answers 422, not 502 or 504:
// Cloudflare replaces an origin's 502 or 504 with its own error page, so a
// panel behind it showed Cloudflare's "invalid or incomplete response" instead
// of the SFTP or restic error (GH #2077, after the first fix).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// slowTestAgent answers backup.dest.test after delay, or gives up when the
// caller's context ends (as the real client does on its socket deadline).
type slowTestAgent struct {
	delay time.Duration
	resp  string
}

func (a *slowTestAgent) Call(ctx context.Context, _ string, _ any) (json.RawMessage, error) {
	select {
	case <-time.After(a.delay):
		return json.RawMessage(a.resp), nil
	case <-ctx.Done():
		return nil, fmt.Errorf("agent: read response: i/o timeout")
	}
}

type destTestRepo struct {
	repository.BackupDestinationRepository
	d *models.BackupDestination
}

func (r *destTestRepo) Get(_ context.Context, _ string) (*models.BackupDestination, error) {
	return r.d, nil
}

// serveDestTest runs the test handler behind a real HTTP server whose
// WriteTimeout is writeTimeout, like the panel's.
func serveDestTest(t *testing.T, ag *slowTestAgent, writeTimeout time.Duration) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &backupDestinationHandler{
		repo:  &destTestRepo{d: &models.BackupDestination{ID: "dst-1", Kind: models.BackupDestinationKindSFTP, URL: "sftp:u@backup.example:/srv/jabali"}},
		agent: ag,
	}
	r := gin.New()
	r.POST("/admin/backup-destinations/:id/test", h.test)
	srv := httptest.NewUnstartedServer(r)
	srv.Config.WriteTimeout = writeTimeout
	srv.Start()
	t.Cleanup(srv.Close)
	return srv.URL + "/admin/backup-destinations/dst-1/test"
}

func TestBackupDestTest_AnswersATestThatOutlastsTheServerWriteTimeout(t *testing.T) {
	url := serveDestTest(t, &slowTestAgent{delay: 600 * time.Millisecond, resp: `{"status":"ok","stdout_preview":"reachable — restic repo initialized at destination"}`}, 300*time.Millisecond)

	resp, err := http.Post(url, "application/json", nil)
	if err != nil {
		t.Fatalf("the test's answer was lost (the server closed the connection): %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "initialized at destination") {
		t.Fatalf("got %d %s, want 200 with the agent's result", resp.StatusCode, body)
	}
}

func TestBackupDestTest_TimeoutSaysTheDestinationDidntAnswer(t *testing.T) {
	prev := backupDestTestBudget
	backupDestTestBudget = 200 * time.Millisecond
	t.Cleanup(func() { backupDestTestBudget = prev })
	url := serveDestTest(t, &slowTestAgent{delay: 5 * time.Second, resp: `{"status":"ok"}`}, 100*time.Millisecond)

	resp, err := http.Post(url, "application/json", nil)
	if err != nil {
		t.Fatalf("no answer: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status %d %s, want 422 (a proxy like Cloudflare replaces a 504's body)", resp.StatusCode, body)
	}
	var out map[string]string
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out["error"] != "test_timeout" || !strings.Contains(out["detail"], "didn't answer within") {
		t.Fatalf("body %s, want test_timeout with a reason the admin can act on", body)
	}
}

func TestBackupDestTest_FailedTestKeepsItsReasonPastAProxy(t *testing.T) {
	url := serveDestTest(t, &slowTestAgent{resp: `{"status":"error","detail":"init_failed: exit status 1","stderr":"ssh: connect to host backup.example port 22: Connection refused"}`}, time.Second)

	resp, err := http.Post(url, "application/json", nil)
	if err != nil {
		t.Fatalf("no answer: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status %d %s, want 422 (a proxy like Cloudflare replaces a 502's body)", resp.StatusCode, body)
	}
	var out map[string]string
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out["error"] != "restic_test_failed" || out["detail"] != "init_failed: exit status 1" || !strings.Contains(out["stderr"], "Connection refused") {
		t.Fatalf("body %s, want restic_test_failed with the agent's detail and stderr", body)
	}
}
