package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/uploadintake"
)

// The staging-path and per-owner stats rules are unit-tested in
// internal/uploadintake; these tests drive them through the HTTP adapter.

func doChunk(r http.Handler, uploadID, dir, name string, offset int, final bool, body string) *httptest.ResponseRecorder {
	url := fmt.Sprintf("/api/v1/files/upload-chunk?upload_id=%s&offset=%d&path=%s&name=%s",
		uploadID, offset, dir, name)
	if final {
		url += "&final=1"
	}
	req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestUploadChunk_HappyTwoChunks(t *testing.T) {
	r := setupFilesRouter(t, "user1", agentReply(map[string]any{"dest_path": "/home/alice/f.txt"}))
	if w := doChunk(r, "u1", "/home/alice", "f.txt", 0, false, "hello"); w.Code != http.StatusOK {
		t.Fatalf("chunk 0: got %d body=%s", w.Code, w.Body.String())
	}
	if w := doChunk(r, "u1", "/home/alice", "f.txt", 5, true, "world"); w.Code != http.StatusOK {
		t.Fatalf("final chunk: got %d body=%s", w.Code, w.Body.String())
	}
}

// #426: offset must equal the current staging file size — no holes, no mid-file
// overwrite (the cross-session content-injection vector).
func TestUploadChunk_BadOffsetRejected(t *testing.T) {
	r := setupFilesRouter(t, "user1", &mockAgent{})
	if w := doChunk(r, "u2", "/home/alice", "f.txt", 0, false, "hello"); w.Code != http.StatusOK {
		t.Fatalf("chunk 0: got %d", w.Code)
	}
	// size is now 5; sending offset 2 must be rejected, not silently overwrite.
	w := doChunk(r, "u2", "/home/alice", "f.txt", 2, false, "X")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "bad_offset") {
		t.Fatalf("bad offset: got %d body=%s, want 409 bad_offset", w.Code, w.Body.String())
	}
}

// #426: a non-first chunk for an upload that doesn't exist (e.g. a wrong/cross
// user upload_id) must be rejected, never auto-created.
func TestUploadChunk_UnknownUploadRejected(t *testing.T) {
	r := setupFilesRouter(t, "user1", &mockAgent{})
	w := doChunk(r, "ghost", "/home/alice", "f.txt", 10, false, "x")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "upload_not_found") {
		t.Fatalf("unknown upload: got %d body=%s, want 409 upload_not_found", w.Code, w.Body.String())
	}
}

// #425: a single tenant cannot keep more than uploadintake.MaxInFlight chunked
// staging files open at once (host-disk-fill DoS cap).
func TestUploadChunk_ConcurrencyCap(t *testing.T) {
	r := setupFilesRouter(t, "user1", &mockAgent{})
	for i := 0; i < uploadintake.MaxInFlight; i++ {
		if w := doChunk(r, fmt.Sprintf("cap%d", i), "/home/alice", "f.txt", 0, false, "a"); w.Code != http.StatusOK {
			t.Fatalf("in-flight upload %d should be accepted, got %d", i, w.Code)
		}
	}
	w := doChunk(r, "capN", "/home/alice", "f.txt", 0, false, "a")
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "too_many_uploads") {
		t.Fatalf("over-cap upload: got %d body=%s, want 429 too_many_uploads", w.Code, w.Body.String())
	}
}

// #425: the single-shot /upload path shares the per-user concurrency cap (it is
// owner-tagged), so once a tenant holds uploadintake.MaxInFlight staging files a
// further single-shot upload is rejected.
func TestUpload_SingleShotRespectsConcurrencyCap(t *testing.T) {
	r := setupFilesRouter(t, "user1", agentReply(map[string]any{"path": "/home/alice/x"}))
	for i := 0; i < uploadintake.MaxInFlight; i++ {
		if w := doChunk(r, fmt.Sprintf("c%d", i), "/home/alice", "f.txt", 0, false, "a"); w.Code != http.StatusOK {
			t.Fatalf("chunked in-flight %d should be accepted, got %d", i, w.Code)
		}
	}
	body, ct := makeMultipart(t, "file", "single.txt", "hello")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files/upload?path=/home/alice", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "too_many_uploads") {
		t.Fatalf("single-shot over cap: got %d body=%s, want 429 too_many_uploads", w.Code, w.Body.String())
	}
}
