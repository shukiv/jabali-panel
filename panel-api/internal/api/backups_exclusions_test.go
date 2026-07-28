package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// exclAgent records the last files.read/files.write dispatch and returns canned
// results — enough to assert the /me/backups/exclusions handlers are owner-scoped.
type exclAgent struct {
	cmd      string
	params   map[string]any
	readResp string
	readErr  error
	writeErr error
}

func (f *exclAgent) Call(_ context.Context, command string, params any) (json.RawMessage, error) {
	f.cmd = command
	if m, ok := params.(map[string]any); ok {
		f.params = m
	}
	switch command {
	case "files.read":
		if f.readErr != nil {
			return nil, f.readErr
		}
		b, _ := json.Marshal(map[string]any{"content": f.readResp})
		return b, nil
	case "files.write":
		if f.writeErr != nil {
			return nil, f.writeErr
		}
	}
	return json.RawMessage(`{}`), nil
}

func exclHandler(ag *exclAgent) *meBackupHandler {
	return &meBackupHandler{cfg: MeBackupsHandlerConfig{
		Agent: ag,
		Users: &fakeUserRepo{user: &models.User{ID: "u1", Username: uname("alice")}},
	}}
}

func exclCtx(t *testing.T, method, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(method, "/api/v1/me/backups/exclusions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	// Session user is "u1" (fakeUserRepo resolves it to username "alice").
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", IsAdmin: false})
	return c, rec
}

func TestMeBackupExclusions_GetReturnsContent(t *testing.T) {
	ag := &exclAgent{readResp: "cache/\nnode_modules/\n"}
	c, rec := exclCtx(t, http.MethodGet, "")
	exclHandler(ag).getExclusions(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Patterns string `json:"patterns"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Patterns != "cache/\nnode_modules/\n" {
		t.Errorf("patterns = %q", resp.Patterns)
	}
	// Owner-scoped: the read targets the SESSION user's own home.
	if ag.cmd != "files.read" {
		t.Errorf("cmd = %q, want files.read", ag.cmd)
	}
	if ag.params["username"] != "alice" || ag.params["path"] != "/home/alice/.backupignore" {
		t.Errorf("read params = %v; want username=alice path=/home/alice/.backupignore", ag.params)
	}
}

func TestMeBackupExclusions_GetMissingFileIsEmpty(t *testing.T) {
	ag := &exclAgent{readErr: errors.New("failed to open file: open /home/alice/.backupignore: no such file or directory")}
	c, rec := exclCtx(t, http.MethodGet, "")
	exclHandler(ag).getExclusions(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (absent file = empty); body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Patterns string `json:"patterns"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Patterns != "" {
		t.Errorf("absent .backupignore must yield empty patterns, got %q", resp.Patterns)
	}
}

func TestMeBackupExclusions_PutIsOwnerScoped(t *testing.T) {
	ag := &exclAgent{}
	// A user_id/username smuggled in the body MUST be ignored — the handler
	// binds only `patterns`, and the write is scoped to the session user.
	c, rec := exclCtx(t, http.MethodPut, `{"patterns":"logs/\n","user_id":"victim","username":"victim"}`)
	exclHandler(ag).putExclusions(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ag.cmd != "files.write" {
		t.Fatalf("cmd = %q, want files.write", ag.cmd)
	}
	if ag.params["username"] != "alice" || ag.params["path"] != "/home/alice/.backupignore" {
		t.Errorf("write went to %v — must be the session user's own home, not the body's", ag.params)
	}
	if ag.params["content"] != "logs/\n" {
		t.Errorf("content = %v, want %q", ag.params["content"], "logs/\n")
	}
	if ag.params["mode"] != "overwrite" {
		t.Errorf("mode = %v, want overwrite", ag.params["mode"])
	}
}

func TestMeBackupExclusions_PutTooLargeRejected(t *testing.T) {
	ag := &exclAgent{}
	big := strings.Repeat("x", 64*1024+1)
	c, rec := exclCtx(t, http.MethodPut, `{"patterns":"`+big+`"}`)
	exclHandler(ag).putExclusions(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (over 64 KiB)", rec.Code)
	}
	if ag.cmd == "files.write" {
		t.Error("oversized exclusions must not reach the agent")
	}
}
