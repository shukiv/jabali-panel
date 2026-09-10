package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// JAB-340 AC2: the HTTP file verbs that forward the agent's reply body verbatim
// (du, extract, extract.start, copy.start, job.status) must validate it first —
// a malformed agent *success* body must not reach the browser as a 200/202 that
// only fails client-side at response.json(). These tests drive each handler with
// a reply that decodes to a zero value (an error blob / JSON null) and assert the
// handler fails closed; a valid reply is forwarded unchanged.
//
// Reverting any DecodeX guard in files.go turns the malformed case back into a
// 200/202 and reddens the matching row here.

func rawAgent(raw string) *mockAgent {
	return &mockAgent{
		callFn: func(_ context.Context, _ string, _ any) (json.RawMessage, error) {
			return json.RawMessage(raw), nil
		},
	}
}

func TestFilesDu_FailsClosedOnMalformedReply(t *testing.T) {
	// du is the GH #1184 admin File Manager verb, so drive it through the admin
	// router (adminGate passes, admin_root=true).
	bad := setupAdminFilesRouter(t, rawAgent(`{"error":"boom"}`))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/files/du?path=/home/alice", nil)
	w := httptest.NewRecorder()
	bad.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Fatalf("du(error blob): got 200, want a fail-closed non-200; body=%s", w.Body.String())
	}

	// A du of an empty dir (total 0, no entries, but a real path) is a success.
	ok := setupAdminFilesRouter(t, rawAgent(`{"path":"/home/alice","total":0,"entries":[]}`))
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/files/du?path=/home/alice", nil)
	w = httptest.NewRecorder()
	ok.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("du(valid empty dir): got %d want 200; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"path":"/home/alice"`) {
		t.Fatalf("du(valid): reply not forwarded verbatim; body=%s", w.Body.String())
	}
}

func TestFilesJobStatus_FailsClosedOnMalformedReply(t *testing.T) {
	bad := setupFilesRouter(t, "user1", rawAgent(`null`))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/jobs/job-1", nil)
	w := httptest.NewRecorder()
	bad.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Fatalf("job.status(null): got 200, want a fail-closed non-200; body=%s", w.Body.String())
	}

	ok := setupFilesRouter(t, "user1", rawAgent(`{"job_id":"job-1","status":"running","done":0,"total":100,"started_at":"2026-01-02T03:04:05Z"}`))
	req = httptest.NewRequest(http.MethodGet, "/api/v1/files/jobs/job-1", nil)
	w = httptest.NewRecorder()
	ok.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("job.status(valid): got %d want 200; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"status":"running"`) {
		t.Fatalf("job.status(valid): reply not forwarded verbatim; body=%s", w.Body.String())
	}
}

func TestFilesExtractSync_FailsClosedOnMalformedReply(t *testing.T) {
	// Sync extract has no field-presence guard (a valid default-dest extract has
	// an empty dest), so the fail-closed case is a malformed/truncated body.
	bad := setupFilesRouter(t, "user1", rawAgent(`{"dest":`))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files/extract",
		strings.NewReader(`{"path":"/home/alice/a.tar.gz"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	bad.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Fatalf("extract(error blob): got 200, want a fail-closed non-200; body=%s", w.Body.String())
	}

	ok := setupFilesRouter(t, "user1", rawAgent(`{"dest":"/home/alice","extracted":1,"skipped":0}`))
	req = httptest.NewRequest(http.MethodPost, "/api/v1/files/extract",
		strings.NewReader(`{"path":"/home/alice/a.tar.gz"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	ok.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("extract(valid): got %d want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestFilesExtractStart_FailsClosedOnMalformedReply(t *testing.T) {
	bad := setupFilesRouter(t, "user1", rawAgent(`{"error":"boom"}`))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files/extract?async=1",
		strings.NewReader(`{"path":"/home/alice/a.tar.gz"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	bad.ServeHTTP(w, req)
	if w.Code == http.StatusAccepted {
		t.Fatalf("extract.start(error blob): got 202, want a fail-closed non-202; body=%s", w.Body.String())
	}

	ok := setupFilesRouter(t, "user1", rawAgent(`{"job_id":"job-9"}`))
	req = httptest.NewRequest(http.MethodPost, "/api/v1/files/extract?async=1",
		strings.NewReader(`{"path":"/home/alice/a.tar.gz"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	ok.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("extract.start(valid): got %d want 202; body=%s", w.Code, w.Body.String())
	}
}

func TestFilesCopyStart_FailsClosedOnMalformedReply(t *testing.T) {
	bad := setupFilesRouter(t, "user1", rawAgent(`{"error":"boom"}`))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files/copy?async=1",
		strings.NewReader(`{"path":"/home/alice/a.txt","dest_dir":"/home/alice/b"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	bad.ServeHTTP(w, req)
	if w.Code == http.StatusAccepted {
		t.Fatalf("copy.start(error blob): got 202, want a fail-closed non-202; body=%s", w.Body.String())
	}

	ok := setupFilesRouter(t, "user1", rawAgent(`{"job_id":"job-9"}`))
	req = httptest.NewRequest(http.MethodPost, "/api/v1/files/copy?async=1",
		strings.NewReader(`{"path":"/home/alice/a.txt","dest_dir":"/home/alice/b"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	ok.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("copy.start(valid): got %d want 202; body=%s", w.Code, w.Body.String())
	}
}
