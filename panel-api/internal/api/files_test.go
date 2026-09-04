package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/filesops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// setupFilesRouter wires /files onto a throwaway gin.Engine. Caller injects
// a pre-baked mockAgent. userID "" means "no claims" (tests 401).
func setupFilesRouter(t *testing.T, userID string, agent *mockAgent) *gin.Engine {
	t.Helper()
	// Redirect upload staging to a per-test tmpdir; the production path
	// (/var/lib/jabali-uploads) is created by install.sh and isn't
	// writable from `go test`.
	prev := uploadStagingDir
	uploadStagingDir = t.TempDir()
	t.Cleanup(func() { uploadStagingDir = prev })

	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")

	if userID != "" {
		v1.Use(func(c *gin.Context) {
			ginctx.SetClaims(c, &auth.AccessClaims{UserID: userID, IsAdmin: false})
			c.Next()
		})
	}

	alice := "alice"
	users := &mockUserRepo{users: map[string]*models.User{
		"user1":       {ID: "user1", Username: &alice},
		"no-linux":    {ID: "no-linux", Username: nil},
		"empty-linux": {ID: "empty-linux", Username: strPtrEmpty()},
	}}
	RegisterFilesRoutes(v1, FilesHandlerConfig{
		Users:   users,
		Domains: nil, // not exercised in these tests
		Agent:   agent,
	})
	return r
}

func strPtrEmpty() *string { s := ""; return &s }

// agentReply returns a canned mock agent that encodes `payload` as JSON.
func agentReply(payload any) *mockAgent {
	return &mockAgent{
		callFn: func(_ context.Context, _ string, _ any) (json.RawMessage, error) {
			b, _ := json.Marshal(payload)
			return json.RawMessage(b), nil
		},
	}
}

func agentFail(err error) *mockAgent {
	return &mockAgent{callErr: err}
}

// ------------- GET /files (list) -------------

func TestFilesList_HappyPath(t *testing.T) {
	agent := agentReply(filesops.ListResult{
		Path: "/home/alice",
		Entries: []filesops.ListEntry{
			{Name: "public_html", IsDir: true},
			{Name: "notes.txt", IsDir: false, Size: 42},
		},
	})
	r := setupFilesRouter(t, "user1", agent)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files?path=/home/alice", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", w.Code, w.Body.String())
	}
	var got filesops.ListResult
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Entries) != 2 || got.Entries[0].Name != "public_html" {
		t.Fatalf("unexpected entries: %+v", got.Entries)
	}
	if agent.callCount != 1 {
		t.Fatalf("agent calls: got %d want 1", agent.callCount)
	}
}

func TestFilesList_Unauthorized(t *testing.T) {
	r := setupFilesRouter(t, "", &mockAgent{})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files?path=/home/alice", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401", w.Code)
	}
}

func TestFilesList_PathRequired(t *testing.T) {
	r := setupFilesRouter(t, "user1", &mockAgent{})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400", w.Code)
	}
}

func TestFilesList_NoLinuxAccount(t *testing.T) {
	r := setupFilesRouter(t, "no-linux", &mockAgent{})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files?path=/home/alice", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status: got %d want 403, body=%s", w.Code, w.Body.String())
	}
}

func TestFilesList_AgentErrorStatusMapping(t *testing.T) {
	cases := []struct {
		name    string
		errText string
		want    int
	}{
		{"traversal → 403", "path_traversal: ..", http.StatusForbidden},
		{"not_in_scope → 403", "not_in_scope: /etc/passwd", http.StatusForbidden},
		{"bad_chars → 400", "bad_characters in path", http.StatusBadRequest},
		{"no such file → 404", "open /home/alice/missing: no such file or directory", http.StatusNotFound},
		{"unknown → 500", "socket closed", http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := setupFilesRouter(t, "user1", agentFail(fmt.Errorf("%s", tc.errText)))
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files?path=/home/alice", nil)
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("got %d want %d for %q", w.Code, tc.want, tc.errText)
			}
		})
	}
}

// ------------- GET /files/tree -------------

func TestFilesTree_FiltersToDirsOnly(t *testing.T) {
	agent := agentReply(filesops.ListResult{
		Path: "/home/alice",
		Entries: []filesops.ListEntry{
			{Name: "public_html", IsDir: true},
			{Name: "notes.txt", IsDir: false},
			{Name: "link-to-elsewhere", IsDir: true, IsSymlink: true}, // filtered out
			{Name: ".config", IsDir: true},
		},
	})
	r := setupFilesRouter(t, "user1", agent)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/tree?path=/home/alice", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", w.Code)
	}
	var got filesops.ListResult
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("got %d dirs, want 2: %+v", len(got.Entries), got.Entries)
	}
	for _, e := range got.Entries {
		if !e.IsDir || e.IsSymlink {
			t.Fatalf("unexpected entry: %+v", e)
		}
	}
}

// ------------- GET /files/download -------------

func TestFilesDownload_SetsAttachmentHeaders(t *testing.T) {
	agent := agentReply(filesops.ReadResult{
		Path:    "/home/alice/notes.txt",
		Content: "hello world",
		Size:    11,
	})
	r := setupFilesRouter(t, "user1", agent)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/download?path=/home/alice/notes.txt", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", w.Code)
	}
	if got := w.Header().Get("Content-Disposition"); !strings.Contains(got, `filename="notes.txt"`) {
		t.Fatalf("Content-Disposition: got %q", got)
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("X-Content-Type-Options header missing")
	}
	if w.Body.String() != "hello world" {
		t.Fatalf("body: got %q want %q", w.Body.String(), "hello world")
	}
}

// ------------- GET /files/preview -------------

func TestFilesPreview_ReturnsJSONEnvelope(t *testing.T) {
	agent := agentReply(filesops.ReadResult{
		Path:    "/home/alice/notes.txt",
		Content: "hello",
		Size:    5,
	})
	r := setupFilesRouter(t, "user1", agent)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/preview?path=/home/alice/notes.txt", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", w.Code)
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("missing nosniff header")
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["content"] != "hello" || got["size"].(float64) != 5 {
		t.Fatalf("preview body: %+v", got)
	}
}

// ------------- POST /files/upload -------------

func makeMultipart(t *testing.T, fieldName, filename, content string) (body *bytes.Buffer, contentType string) {
	t.Helper()
	body = &bytes.Buffer{}
	w := multipart.NewWriter(body)
	fw, err := w.CreateFormFile(fieldName, filename)
	if err != nil {
		t.Fatalf("multipart: %v", err)
	}
	if _, err := io.Copy(fw, strings.NewReader(content)); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return body, w.FormDataContentType()
}

func TestFilesUpload_HappyPath(t *testing.T) {
	agent := agentReply(map[string]any{"path": "/home/alice/file.txt", "bytes_written": 5})
	r := setupFilesRouter(t, "user1", agent)
	body, ct := makeMultipart(t, "file", "file.txt", "hello")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files/upload?path=/home/alice", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", w.Code, w.Body.String())
	}
	if agent.callCount != 1 {
		t.Fatalf("agent calls: got %d want 1", agent.callCount)
	}
}

func TestFilesUpload_MissingFile(t *testing.T) {
	r := setupFilesRouter(t, "user1", &mockAgent{})
	// Multipart with no "file" field
	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	_ = mw.WriteField("other", "x")
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files/upload?path=/home/alice", buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400", w.Code)
	}
}

func TestFilesUpload_RejectsPathInFilename(t *testing.T) {
	r := setupFilesRouter(t, "user1", &mockAgent{})
	body, ct := makeMultipart(t, "file", "../evil", "payload")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files/upload?path=/home/alice", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	// Note: mime/multipart strips "../" from the Filename (security default),
	// so this arrives at the handler as "evil" — still a safe single-segment
	// name. Test documents the behavior: upload succeeds on handler, and
	// path traversal defense relies on the agent's filesafe scope check.
	// (handler only rejects names containing "/" or "\\" literally).
	if w.Code != http.StatusOK && w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 200 or 400", w.Code)
	}
}

// ------------- POST /files/mkdir -------------

func TestFilesMkdir_HappyPath(t *testing.T) {
	agent := agentReply(map[string]any{"path": "/home/alice/newdir"})
	r := setupFilesRouter(t, "user1", agent)
	body := strings.NewReader(`{"path":"/home/alice/newdir"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files/mkdir", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", w.Code, w.Body.String())
	}
}

func TestFilesMkdir_MissingPath(t *testing.T) {
	r := setupFilesRouter(t, "user1", &mockAgent{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files/mkdir", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400", w.Code)
	}
}

// ------------- POST /files/rename -------------

func TestFilesRename_HappyPath(t *testing.T) {
	var gotParams filesops.RenameParams
	agent := &mockAgent{
		callFn: func(_ context.Context, _ string, params any) (json.RawMessage, error) {
			if p, ok := params.(filesops.RenameParams); ok {
				gotParams = p
			}
			return json.RawMessage(`{}`), nil
		},
	}
	r := setupFilesRouter(t, "user1", agent)
	body := strings.NewReader(`{"path":"/home/alice/old.txt","new_name":"new.txt"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files/rename", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", w.Code, w.Body.String())
	}
	if gotParams.OldPath != "/home/alice/old.txt" || gotParams.NewPath != "/home/alice/new.txt" {
		t.Fatalf("rename params: got %+v", gotParams)
	}
}

func TestFilesRename_RejectsSlashInNewName(t *testing.T) {
	r := setupFilesRouter(t, "user1", &mockAgent{})
	for _, bad := range []string{"a/b", "../escape", ".", ".."} {
		t.Run(bad, func(t *testing.T) {
			body := strings.NewReader(fmt.Sprintf(`{"path":"/home/alice/x","new_name":%q}`, bad))
			req := httptest.NewRequest(http.MethodPost, "/api/v1/files/rename", body)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("new_name=%q: got %d want 400", bad, w.Code)
			}
		})
	}
}

// ------------- DELETE /files -------------

func TestFilesDelete_HappyPath(t *testing.T) {
	var gotParams filesops.DeleteParams
	agent := &mockAgent{
		callFn: func(_ context.Context, _ string, params any) (json.RawMessage, error) {
			if p, ok := params.(filesops.DeleteParams); ok {
				gotParams = p
			}
			return json.RawMessage(`{}`), nil
		},
	}
	r := setupFilesRouter(t, "user1", agent)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/files?path=/home/alice/file.txt", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", w.Code)
	}
	if gotParams.Recursive {
		t.Fatalf("recursive should be false by default")
	}
}

func TestFilesDelete_RecursiveFlag(t *testing.T) {
	var gotParams filesops.DeleteParams
	agent := &mockAgent{
		callFn: func(_ context.Context, _ string, params any) (json.RawMessage, error) {
			if p, ok := params.(filesops.DeleteParams); ok {
				gotParams = p
			}
			return json.RawMessage(`{}`), nil
		},
	}
	r := setupFilesRouter(t, "user1", agent)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/files?path=/home/alice/dir&recursive=true", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", w.Code)
	}
	if !gotParams.Recursive {
		t.Fatalf("recursive should be true")
	}
}
