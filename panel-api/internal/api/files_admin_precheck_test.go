package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// setupAdminFilesRouter wires /admin/files with an admin session and the admin
// File Manager setting ON, so adminGate passes and every handler carries
// admin_root=true. The mock agent must NOT be reached when the API-side
// pre-check refuses a mutation — callCount is the proof.
func setupAdminFilesRouter(t *testing.T, agent *mockAgent) *gin.Engine {
	t.Helper()
	prev := uploadStagingDir
	uploadStagingDir = t.TempDir()
	t.Cleanup(func() { uploadStagingDir = prev })

	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "admin1", Email: "admin@example.com", IsAdmin: true})
		c.Next()
	})
	RegisterFilesRoutes(v1, FilesHandlerConfig{
		Agent:          agent,
		ServerSettings: &mockServerSettingsRepo{getResult: &models.ServerSettings{AdminFileManagerEnabled: true}},
	})
	return r
}

// JAB-367 (criterion 4): the API refuses an admin_root mutation outside the
// write allow-list BEFORE it reaches the agent — layered enforcement.
func TestAdminFiles_APISidePreCheck_RejectsBeforeAgent(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"mkdir outside roots", http.MethodPost, "/api/v1/admin/files/mkdir", `{"path":"/etc/cron.d/jab367"}`},
		{"write outside roots", http.MethodPost, "/api/v1/admin/files/write", `{"path":"/usr/lib/x.so","content":"x"}`},
		{"chmod deny-list", http.MethodPost, "/api/v1/admin/files/chmod", `{"path":"/etc/jabali/config.toml","mode":"0777"}`},
		{"copy bad source", http.MethodPost, "/api/v1/admin/files/copy", `{"path":"/etc/shadow","dest_dir":"/home/alice"}`},
		{"delete outside roots", http.MethodDelete, "/api/v1/admin/files?path=/etc/passwd", ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A plain recording agent: if the pre-check works, it is never
			// called (callCount stays 0), which is the assertion below.
			rec := &mockAgent{}
			r := setupAdminFilesRouter(t, rec)

			w := httptest.NewRecorder()
			var req *http.Request
			if tc.body == "" {
				req = httptest.NewRequest(tc.method, tc.path, nil)
			} else {
				req = httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
				req.Header.Set("Content-Type", "application/json")
			}
			r.ServeHTTP(w, req)

			if w.Code != http.StatusForbidden {
				t.Fatalf("status: got %d want 403, body=%s", w.Code, w.Body.String())
			}
			var body struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &body)
			if body.Error != "read_only" && body.Error != "path_denied" {
				t.Errorf("error code: got %q want read_only|path_denied", body.Error)
			}
			if rec.callCount != 0 {
				t.Errorf("agent was called %d times — the pre-check must reject BEFORE the agent", rec.callCount)
			}
		})
	}
}

// A valid in-allow-list admin mutation passes the pre-check and reaches the agent.
func TestAdminFiles_APISidePreCheck_PassesThroughInAllowList(t *testing.T) {
	agent := agentReply(gin.H{"path": "/home/alice/newdir"})
	r := setupAdminFilesRouter(t, agent)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/files/mkdir",
		strings.NewReader(`{"path":"/home/alice/newdir"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", w.Code, w.Body.String())
	}
	if agent.callCount != 1 {
		t.Fatalf("agent calls: got %d want 1 (an allow-list path must reach the agent)", agent.callCount)
	}
}

// The pre-check is admin_root-ONLY: a tenant mutation is never lexically
// filtered by the API (the tenant scope is the agent's job); the agent is
// reached and enforces the per-tenant docroot.
func TestAdminFiles_APISidePreCheck_TenantNotChecked(t *testing.T) {
	agent := agentReply(gin.H{"path": "/etc/cron.d/jab367"})
	r := setupFilesRouter(t, "user1", agent) // IsAdmin:false, tenant /files mount

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files/mkdir",
		strings.NewReader(`{"path":"/etc/cron.d/jab367"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	// The API does not pre-check tenant paths, so the request reaches the agent
	// (the mock replies OK). The real agent would enforce the tenant docroot.
	if agent.callCount != 1 {
		t.Fatalf("tenant mkdir must reach the agent (no API-side admin pre-check); calls=%d", agent.callCount)
	}
}
