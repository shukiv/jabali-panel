package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
)

// GH #1993: "Overwrite existing items with the backup" is off by default. A
// restore from an uploaded file then sends keep_existing to the agent, which
// adds only what the account is missing, and Apply leaves the account's
// existing rows alone. An agent that predates keep_existing would replace
// instead, so such a restore is refused on it (409 agent_update_required).

// keAgent answers agent.version with caps and records restore_from_tar.
func keAgent(caps string, restores *[]map[string]any) *mockAgent {
	return &mockAgent{callFn: func(_ context.Context, cmd string, params any) (json.RawMessage, error) {
		switch cmd {
		case "agent.version":
			return json.RawMessage(`{"version":"x","capabilities":[` + caps + `]}`), nil
		case "backup.inspect_uploaded_tar":
			return json.RawMessage(`{"allowlist_supported":true,"user":{"username":"alice"}}`), nil
		case "backup.restore_from_tar":
			*restores = append(*restores, params.(map[string]any))
			return json.RawMessage(`{"applied":["home → /home/alice"],"upload_confinement_enforced":true,"db_allowlist_enforced":true,"mail_allowlist_enforced":true}`), nil
		}
		return nil, fmt.Errorf("unexpected %s", cmd)
	}}
}

const (
	capsOld = `"restore_upload_confinement"`
	capsNew = `"restore_upload_confinement","restore_keep_existing"`
)

func keRequest(t *testing.T, handler gin.HandlerFunc, userID string, admin bool, body any) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/x", func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: userID, IsAdmin: admin})
		c.Next()
	}, handler)
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestRestoreUploadApply_KeepingNeedsAnAgentThatKeeps(t *testing.T) {
	for _, c := range []struct {
		name      string
		caps      string
		overwrite bool
		want      int
	}{
		{"keep on an old agent", capsOld, false, http.StatusConflict},
		{"overwrite on an old agent", capsOld, true, http.StatusAccepted},
		{"keep on a new agent", capsNew, false, http.StatusAccepted},
	} {
		t.Run(c.name, func(t *testing.T) {
			restoreUploadDir = t.TempDir()
			t.Cleanup(func() { restoreUploadDir = "/var/lib/jabali-uploads" })
			var restores []map[string]any
			cfg := ucConfig()
			cfg.Agent = keAgent(c.caps, &restores)
			cfg.Users = ubUsers{}
			h := &backupHandler{cfg: cfg}
			stage(t, "upload0001")

			w := keRequest(t, h.restoreUploadApply, ubAdmin, true,
				map[string]any{"upload_id": "upload0001", "target_username": "alice", "overwrite": c.overwrite})
			if w.Code != c.want {
				t.Fatalf("status %d body %s, want %d", w.Code, w.Body, c.want)
			}
			if c.want == http.StatusConflict && !strings.Contains(w.Body.String(), "agent_update_required") {
				t.Errorf("body %s, want agent_update_required", w.Body)
			}
			if c.want == http.StatusAccepted {
				waitUploadOutcome(t, restoreUploadOutcomePath(ubAdmin, "upload0001"))
				if len(restores) == 0 || restores[0]["keep_existing"] != !c.overwrite {
					t.Errorf("restore_from_tar params %v, want keep_existing=%v", restores, !c.overwrite)
				}
			}
		})
	}
}

// waitUploadOutcome waits for the detached restore to seal its outcome.
func waitUploadOutcome(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if o, err := readRestoreUploadOutcome(path); err == nil && o.Status != "restoring" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the restore did not finish")
}

func TestRestoreUploadedBackup_KeepingNeedsAnAgentThatKeeps(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		e := newUBEnv(t, &ubAgent{caps: capsOld})
		b := keep(t, e, "keep")
		w := e.do(t, http.MethodPost, "/api/v1/admin/uploaded-backups/"+b.ID+"/restore",
			map[string]any{"target_username": "alice", "overwrite": overwrite})
		want := http.StatusConflict
		if overwrite {
			want = http.StatusAccepted
		}
		if w.Code != want {
			t.Fatalf("overwrite=%v: status %d body %s, want %d", overwrite, w.Code, w.Body, want)
		}
		if overwrite {
			waitRestore(t, e, b.ID)
		}
	}
	// A new agent: the kept upload's restore carries the choice.
	for _, overwrite := range []bool{false, true} {
		a := &ubAgent{}
		e := newUBEnv(t, a)
		b := keep(t, e, "keep")
		if w := e.do(t, http.MethodPost, "/api/v1/admin/uploaded-backups/"+b.ID+"/restore",
			map[string]any{"target_username": "alice", "overwrite": overwrite}); w.Code != http.StatusAccepted {
			t.Fatalf("status %d body %s", w.Code, w.Body)
		}
		waitRestore(t, e, b.ID)
		if len(a.seen) == 0 || a.seen[0]["keep_existing"] != !overwrite {
			t.Errorf("overwrite=%v: restore_from_tar params %v", overwrite, a.seen)
		}
	}
}

func TestTenantRestoreUploadApply_KeepingNeedsAnAgentThatKeeps(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		restoreUploadDir = t.TempDir()
		t.Cleanup(func() { restoreUploadDir = "/var/lib/jabali-uploads" })
		var restores []map[string]any
		h := &meBackupHandler{cfg: MeBackupsHandlerConfig{Agent: keAgent(capsOld, &restores), Users: ucUsers{}}}
		if err := os.WriteFile(restoreUploadTenantPath("T", "upload0001"), []byte("archive"), 0o600); err != nil {
			t.Fatal(err)
		}
		w := keRequest(t, h.restoreUploadApply, "T", false, map[string]any{"upload_id": "upload0001", "overwrite": overwrite})
		if !overwrite && (w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "agent_update_required")) {
			t.Errorf("keep on an old agent: status %d body %s, want 409 agent_update_required", w.Code, w.Body)
		}
		if overwrite && w.Code == http.StatusConflict {
			t.Errorf("overwrite on an old agent: refused %s", w.Body)
		}
	}
}

func TestRunUploadRestore_KeepsWhatIsThereUnlessOverwrite(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		h, a, calls := ucUploadPasses(t, func(int) string {
			return `{"stages":[{"name":"home"},{"name":"mail"}],"upload_confinement_enforced":true}`
		})
		a.overwrite = overwrite
		h.runUploadRestore(a)
		if len(*calls) != 2 {
			t.Fatalf("overwrite=%v: %d agent passes, want 2", overwrite, len(*calls))
		}
		for i, p := range *calls {
			if p["keep_existing"] != !overwrite {
				t.Errorf("overwrite=%v: pass %d keep_existing = %v", overwrite, i, p["keep_existing"])
			}
		}
	}
}

// The metadata rebuild gets the choice too: kept, an existing mailbox keeps
// its auto-reply (backupmetadata.Deps.KeepExisting).
func TestRunUploadRestore_TheRebuildKeepsWhatIsThereUnlessOverwrite(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		var got *uploadedData
		prev := applyUploadedMetadata
		applyUploadedMetadata = func(_ *backupHandler, _ context.Context, _ json.RawMessage, _ string, u uploadedData) []string {
			got = &u
			return nil
		}
		h, a, _ := ucUploadPasses(t, func(int) string { return `{"upload_confinement_enforced":true}` })
		a.overwrite = overwrite
		h.runUploadRestore(a)
		applyUploadedMetadata = prev
		if got == nil || got.keepExisting != !overwrite {
			t.Fatalf("overwrite=%v: the rebuild got %+v", overwrite, got)
		}
		if deps := h.restoreMetadataDeps(got); deps.KeepExisting != !overwrite || !deps.Untrusted {
			t.Errorf("overwrite=%v: rebuild deps KeepExisting=%v Untrusted=%v", overwrite, deps.KeepExisting, deps.Untrusted)
		}
	}
	if (&backupHandler{}).restoreMetadataDeps(nil).KeepExisting {
		t.Error("a restore from this server's own backups keeps nothing back")
	}
}

func TestRunTenantUploadRestore_KeepsWhatIsThereUnlessOverwrite(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		dir := t.TempDir()
		ag := &captureAgent{reply: `{"db_allowlist_enforced":true,"mail_allowlist_enforced":true}`}
		tenantRestoreHandler(ag).runTenantUploadRestore(tenantUploadRestoreArgs{
			path: dir + "/a.tar.zst", outcomePath: dir + "/o.json", username: "alice", overwrite: overwrite,
		})
		if ag.lastParams["keep_existing"] != !overwrite {
			t.Errorf("overwrite=%v: keep_existing = %v", overwrite, ag.lastParams["keep_existing"])
		}
	}
}
