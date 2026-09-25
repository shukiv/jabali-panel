package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// JAB-365 — HTTP half of the upload-intake cap/budget/offset matrix. These run
// through the routes only (no staging-path helpers), so they pin the wire
// behaviour independently of where the staging logic lives.
//
// upload_max_size_mb = 1 → per-upload cap 1 MiB, per-owner staging budget 2 MiB.

const intakeMiB = 1 << 20

func intakeRouter(t *testing.T) http.Handler {
	t.Helper()
	return setupFilesRouterWithSettings(t, "user1",
		agentReply(map[string]any{"dest_path": "/home/alice/f"}),
		&models.ServerSettings{UploadMaxSizeMB: 1})
}

func chunkStatus(r http.Handler, uploadID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/upload-chunk-status?upload_id="+uploadID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func wantError(t *testing.T, w *httptest.ResponseRecorder, code int, errToken string) {
	t.Helper()
	if w.Code != code || !strings.Contains(w.Body.String(), `"`+errToken+`"`) {
		t.Fatalf("got %d %s, want %d %s", w.Code, w.Body.String(), code, errToken)
	}
}

func seedChunks(t *testing.T, r http.Handler, size int, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if w := doChunk(r, id, "/home/alice", "f", 0, false, strings.Repeat("x", size)); w.Code != http.StatusOK {
			t.Fatalf("seed %s: %d %s", id, w.Code, w.Body.String())
		}
	}
}

// A new chunked upload is refused once the owner's staged bytes reach the
// budget (pre-admit uses >=), and nothing is staged for it.
func TestUploadIntakeHTTP_ChunkPreAdmitBudget(t *testing.T) {
	r := intakeRouter(t)
	seedChunks(t, r, intakeMiB, "a", "b")
	wantError(t, doChunk(r, "c", "/home/alice", "f", 0, false, "x"), http.StatusRequestEntityTooLarge, "staging_budget_exceeded")
	wantError(t, chunkStatus(r, "c"), http.StatusNotFound, "not_found")
}

// A new single-shot upload is refused on the same shared budget.
func TestUploadIntakeHTTP_SinglePreAdmitBudget(t *testing.T) {
	r := intakeRouter(t)
	seedChunks(t, r, intakeMiB, "a", "b")
	body, ct := makeMultipart(t, "file", "s.txt", "hello")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files/upload?path=/home/alice", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	wantError(t, w, http.StatusRequestEntityTooLarge, "staging_budget_exceeded")
}

// A chunk that takes the owner past the budget is refused after the write, and
// that session's staging file is removed (post-write uses >). Other sessions
// survive.
func TestUploadIntakeHTTP_ChunkPostWriteBudgetRemovesSession(t *testing.T) {
	r := intakeRouter(t)
	seedChunks(t, r, intakeMiB*9/10, "a", "b")
	wantError(t, doChunk(r, "c", "/home/alice", "f", 0, false, strings.Repeat("y", intakeMiB/2)),
		http.StatusRequestEntityTooLarge, "staging_budget_exceeded")
	wantError(t, chunkStatus(r, "c"), http.StatusNotFound, "not_found")
	if w := chunkStatus(r, "a"); w.Code != http.StatusOK {
		t.Fatalf("another session must survive: %d %s", w.Code, w.Body.String())
	}
}

// An owner may reach the budget exactly: the post-write check refuses only above it.
func TestUploadIntakeHTTP_ChunkPostWriteBudgetBoundary(t *testing.T) {
	r := intakeRouter(t)
	seedChunks(t, r, intakeMiB, "a", "b")
}

// A chunked upload that grows past the per-upload cap is refused and removed.
func TestUploadIntakeHTTP_ChunkOverCapRemovesSession(t *testing.T) {
	r := intakeRouter(t)
	seedChunks(t, r, intakeMiB, "big")
	wantError(t, doChunk(r, "big", "/home/alice", "f", intakeMiB, false, "x"), http.StatusRequestEntityTooLarge, "file_too_large")
	wantError(t, chunkStatus(r, "big"), http.StatusNotFound, "not_found")
}

// A single-shot upload over the per-upload cap is refused.
func TestUploadIntakeHTTP_SingleOverCap(t *testing.T) {
	r := intakeRouter(t)
	body, ct := makeMultipart(t, "file", "big.bin", strings.Repeat("x", intakeMiB+1))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files/upload?path=/home/alice", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	wantError(t, w, http.StatusRequestEntityTooLarge, "file_too_large")
}

// Retrying the first chunk after a blip reopens the session instead of failing:
// an empty session accepts the retry; a non-empty one answers bad_offset with
// the size to resume from.
func TestUploadIntakeHTTP_FirstChunkRetry(t *testing.T) {
	r := intakeRouter(t)
	if w := doChunk(r, "e", "/home/alice", "f", 0, false, ""); w.Code != http.StatusOK {
		t.Fatalf("empty first chunk: %d %s", w.Code, w.Body.String())
	}
	if w := doChunk(r, "e", "/home/alice", "f", 0, false, "abc"); w.Code != http.StatusOK {
		t.Fatalf("retry into an empty session must be accepted: %d %s", w.Code, w.Body.String())
	}
	w := doChunk(r, "e", "/home/alice", "f", 0, false, "abc")
	wantError(t, w, http.StatusConflict, "bad_offset")
	if !strings.Contains(w.Body.String(), `"expected":3`) {
		t.Fatalf("bad_offset must carry the resume size: %s", w.Body.String())
	}
}

// The final chunk hands the assembled file to the agent's files.ingest with the
// owner's identity and the requested destination.
func TestUploadIntakeHTTP_FinalChunkIngests(t *testing.T) {
	var gotMethod string
	var gotParams any
	agent := &mockAgent{callFn: func(_ context.Context, method string, params any) (json.RawMessage, error) {
		gotMethod, gotParams = method, params
		return json.RawMessage(`{"dest_path":"/home/alice/f.txt"}`), nil
	}}
	r := setupFilesRouterWithSettings(t, "user1", agent, &models.ServerSettings{UploadMaxSizeMB: 1})
	if w := doChunk(r, "fin", "/home/alice", "f.txt", 0, true, "hello"); w.Code != http.StatusOK {
		t.Fatalf("final chunk: %d %s", w.Code, w.Body.String())
	}
	b, _ := json.Marshal(gotParams)
	s := string(b)
	if gotMethod != "files.ingest" || !strings.Contains(s, `"user_id":"user1"`) || !strings.Contains(s, `"username":"alice"`) ||
		!strings.Contains(s, `"dest_path":"/home/alice/f.txt"`) || !strings.Contains(s, `"tmp_path":"`) {
		t.Fatalf("ingest call = %s %s", gotMethod, s)
	}
}

// The File Manager upload handlers are the Upload Intake module's HTTP adapter
// (JAB-365 AC1): staging, the cap/budget checks and the ingest hand-off run in
// internal/uploadintake. A handler that opens a staging file, globs the staging
// dir or calls files.ingest itself has re-grown the parallel implementation.
func TestUploadIntakeHTTP_HandlersDelegateToTheModule(t *testing.T) {
	raw, err := os.ReadFile("files.go")
	if err != nil {
		t.Fatal(err)
	}
	src := withoutLineComments(string(raw))
	for _, need := range []string{"uploadintake.Stage(", "uploadintake.Append(", "uploadintake.Ingest(", "uploadintake.Written("} {
		if !strings.Contains(src, need) {
			t.Errorf("files.go must call %s", need)
		}
	}
	for _, banned := range []string{`"files.ingest"`, "O_EXCL", "filepath.Glob(", "jabali-upload-"} {
		if strings.Contains(src, banned) {
			t.Errorf("files.go contains %s — upload staging belongs to internal/uploadintake", banned)
		}
	}
}
