package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1993: a restore from an uploaded backup shows its progress by step,
// with what the agent is doing as the step's detail.

func TestAgentRestoreProgressDescribe(t *testing.T) {
	for _, tc := range []struct {
		in     agentRestoreProgress
		detail string
		pct    int
	}{
		{agentRestoreProgress{Phase: "unpacking", BytesDone: 450, BytesTotal: 1000}, "Unpacking the backup — 45%", 45},
		{agentRestoreProgress{Phase: "unpacking"}, "Unpacking the backup", 0},
		{agentRestoreProgress{Phase: "applying", Stage: "home", Index: 1, Count: 4}, "Restoring the home directory (1 of 4)", 0},
		{agentRestoreProgress{Phase: "applying", Stage: "db", Item: "alice_wp", Index: 3, Count: 4}, "Restoring database alice_wp (3 of 4)", 50},
		{agentRestoreProgress{Phase: "applying", Stage: "docker", Item: "n8n", Index: 4, Count: 4}, "Restoring Docker app n8n (4 of 4)", 75},
		{agentRestoreProgress{Phase: "done"}, "Finishing", 100},
	} {
		if d, p := tc.in.describe(); d != tc.detail || p != tc.pct {
			t.Errorf("%+v: got %q %d, want %q %d", tc.in, d, p, tc.detail, tc.pct)
		}
	}
}

// progressAgent blocks each restore_from_tar pass until the test releases it,
// and reports a database stage to backup.restore_progress.
type progressAgent struct {
	pass1, pass2 chan struct{}
}

// Call is safe for the restore and its progress watcher to call at once.
func (a *progressAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	switch cmd {
	case "agent.version":
		return json.RawMessage(`{"version":"x","capabilities":["restore_upload_confinement"]}`), nil
	case "backup.restore_progress":
		return json.RawMessage(`{"phase":"applying","stage":"db","item":"alice_wp","index":2,"count":3}`), nil
	case "backup.restore_from_tar":
		p, _ := params.(map[string]any)
		if comps, _ := p["components"].([]string); len(comps) == 1 && comps[0] == "mail" {
			<-a.pass2
			return json.RawMessage(`{"applied":["mail → alice"],"upload_confinement_enforced":true}`), nil
		}
		<-a.pass1
		return json.RawMessage(`{"applied":["home → /home/alice"],"upload_confinement_enforced":true,"stages":[{"name":"home"},{"name":"mail"}]}`), nil
	}
	return nil, nil
}

func TestUploadedBackupRestore_ShowsProgressByStep(t *testing.T) {
	restoreProgressPoll = 5 * time.Millisecond
	t.Cleanup(func() { restoreProgressPoll = 2 * time.Second })
	pa := &progressAgent{pass1: make(chan struct{}), pass2: make(chan struct{})}
	e := newUBEnv(t, &ubAgent{})
	// Swap in the blocking agent behind the same routes.
	cfg := ucConfig()
	cfg.Agent = pa
	cfg.Users = ubUsers{}
	cfg.UploadedBackups = e.repo
	e.r = gin.New()
	v1 := e.r.Group("/api/v1", func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: ubAdmin, IsAdmin: true})
		c.Next()
	})
	(&backupHandler{cfg: cfg}).registerUploadedBackupRoutes(v1.Group("/admin"))
	b := keep(t, e, models.UploadedBackupKeep)

	if w := e.do(t, http.MethodPost, "/api/v1/admin/uploaded-backups/"+b.ID+"/restore", map[string]any{"target_username": "alice"}); w.Code != http.StatusAccepted {
		t.Fatalf("restore: %d %s", w.Code, w.Body)
	}
	progressOf := func() *restoreProgress {
		var out struct {
			Data struct {
				RestoreProgress *restoreProgress `json:"restore_progress"`
			} `json:"data"`
		}
		_ = json.Unmarshal(e.do(t, http.MethodGet, "/api/v1/admin/uploaded-backups/"+b.ID, nil).Body.Bytes(), &out)
		return out.Data.RestoreProgress
	}
	waitFor := func(want restoreProgress) {
		t.Helper()
		var got *restoreProgress
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if got = progressOf(); got != nil && *got == want {
				return
			}
		}
		t.Fatalf("progress %+v, want %+v", got, want)
	}

	waitFor(restoreProgress{Step: 1, Steps: 4, Label: restoreStepFilesLabel, Detail: "Restoring database alice_wp (2 of 3)", Percent: 33})
	close(pa.pass1)
	waitFor(restoreProgress{Step: 3, Steps: 4, Label: restoreStepMailLabel, Detail: "Restoring database alice_wp (2 of 3)", Percent: 33})
	close(pa.pass2)
	if got := waitRestore(t, e, b.ID); got.RestoreStatus != models.UploadedBackupDone {
		t.Fatalf("restore ended %q", got.RestoreStatus)
	}
	if p := progressOf(); p != nil {
		t.Errorf("a finished restore still reports progress %+v", p)
	}
	if uploadRestoreProgressFor(uploadedRestoreProgressKey(b.ID)) != nil {
		t.Error("the finished restore's progress was not cleared")
	}
}

// The staged-upload status poll carries the progress while it restores, and
// not after.
func TestRestoreUploadStatus_IncludesProgressWhileRestoring(t *testing.T) {
	gin.SetMode(gin.TestMode)
	restoreUploadDir = t.TempDir()
	t.Cleanup(func() { restoreUploadDir = "/var/lib/jabali-uploads" })
	r := gin.New()
	r.GET("/status", func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: ubAdmin, IsAdmin: true})
		c.Next()
	}, (&backupHandler{cfg: ucConfig()}).restoreUploadStatus)
	path := restoreUploadOutcomePath(ubAdmin, "upload-0001")
	setUploadRestoreProgress(path, restoreProgress{Step: 2, Steps: 3, Label: restoreStepRowsLabel})
	t.Cleanup(func() { clearUploadRestoreProgress(path) })

	get := func() string {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/status?upload_id=upload-0001", nil))
		return w.Body.String()
	}
	writeRestoreUploadOutcome(path, "restoring", nil, nil, "")
	if body := get(); !strings.Contains(body, `"progress":{"step":2,"steps":3,"label":"`+restoreStepRowsLabel+`"}`) {
		t.Errorf("restoring status %s carries no progress", body)
	}
	writeRestoreUploadOutcome(path, "done", []string{"home"}, nil, "")
	if body := get(); strings.Contains(body, "progress") {
		t.Errorf("finished status %s still carries progress", body)
	}
}

// A tenant's own restore status carries the progress too.
func TestTenantRestoreUploadStatus_IncludesProgressWhileRestoring(t *testing.T) {
	gin.SetMode(gin.TestMode)
	restoreUploadDir = t.TempDir()
	t.Cleanup(func() { restoreUploadDir = "/var/lib/jabali-uploads" })
	const tenant = "01KTENANT00000000000000000"
	r := gin.New()
	r.GET("/status", func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: tenant})
		c.Next()
	}, (&meBackupHandler{}).restoreUploadStatus)
	path := restoreUploadTenantOutcomePath(tenant, "upload-0001")
	setUploadRestoreProgress(path, restoreProgress{Step: 1, Steps: 1, Label: restoreStepOwnLabel, Detail: "Unpacking the backup — 10%", Percent: 10})
	t.Cleanup(func() { clearUploadRestoreProgress(path) })
	writeRestoreUploadOutcome(path, "restoring", nil, nil, "")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/status?upload_id=upload-0001", nil))
	if !strings.Contains(w.Body.String(), `"detail":"Unpacking the backup — 10%","percent":10`) {
		t.Errorf("tenant restoring status %s carries no progress", w.Body)
	}
}

// The DNS records are the last step: a restored domain's zone can take a
// minute to appear before its records are added.
func TestRestoreUploadedAccount_ReportsTheDNSStepLast(t *testing.T) {
	for _, c := range []struct {
		components []string
		want       []string
	}{
		{nil, []string{restoreStepFilesLabel, restoreStepRowsLabel, restoreStepMailLabel, restoreStepDNSLabel}},
		{[]string{"home"}, []string{restoreStepFilesLabel, restoreStepRowsLabel, restoreStepDNSLabel}},
	} {
		h, a, _ := ucUploadPasses(t, func(int) string {
			return `{"stages":[{"name":"home"},{"name":"mail"}],"upload_confinement_enforced":true}`
		})
		var labels []string
		steps := map[int]bool{}
		report := func(p restoreProgress) {
			if len(labels) == 0 || labels[len(labels)-1] != p.Label {
				labels = append(labels, p.Label)
			}
			steps[p.Steps] = true
		}
		if _, err := h.restoreUploadedAccount(context.Background(), a.path, "alice", "T", c.components, report); err != nil {
			t.Fatal(err)
		}
		if strings.Join(labels, "|") != strings.Join(c.want, "|") || len(steps) != 1 || !steps[len(c.want)] {
			t.Errorf("components %v: steps %v of %v, want %v of %d", c.components, labels, steps, c.want, len(c.want))
		}
	}
}
