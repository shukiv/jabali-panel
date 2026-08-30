package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// GH #1392 — async extract + job-status polling.
//
// The security property under test is that the username the agent uses to gate
// job ownership comes from the caller's verified claims, never from the request:
// the panel forwards claims-derived "alice", so one tenant can't poll another's
// job by guessing a job id.

func TestExtractAsync_Returns202AndStartsJob(t *testing.T) {
	var gotCmd string
	var gotParams json.RawMessage
	agent := &mockAgent{callFn: func(_ context.Context, cmd string, params any) (json.RawMessage, error) {
		gotCmd = cmd
		gotParams, _ = json.Marshal(params)
		return json.RawMessage(`{"job_id":"abc123"}`), nil
	}}
	r := setupFilesRouter(t, "user1", agent)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files/extract?async=1",
		strings.NewReader(`{"path":"/home/alice/big.zip"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", w.Code, w.Body.String())
	}
	if gotCmd != "files.extract.start" {
		t.Fatalf("agent command = %q, want files.extract.start", gotCmd)
	}
	if !strings.Contains(string(gotParams), `"username":"alice"`) {
		t.Fatalf("start params must carry the claims username: %s", gotParams)
	}
	if !strings.Contains(w.Body.String(), `"job_id":"abc123"`) {
		t.Fatalf("body should relay the job id: %s", w.Body.String())
	}
}

func TestExtractSync_UsesBlockingVerb(t *testing.T) {
	agent := &mockAgent{callFn: func(_ context.Context, _ string, _ any) (json.RawMessage, error) {
		return json.RawMessage(`{"extracted":3}`), nil
	}}
	r := setupFilesRouter(t, "user1", agent)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files/extract",
		strings.NewReader(`{"path":"/home/alice/big.zip"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if agent.lastCommand != "files.extract" {
		t.Fatalf("without async the sync verb must be used, got %q", agent.lastCommand)
	}
}

func TestJobStatus_ForwardsClaimsUsername(t *testing.T) {
	var gotParams json.RawMessage
	agent := &mockAgent{callFn: func(_ context.Context, cmd string, params any) (json.RawMessage, error) {
		if cmd != "files.job.status" {
			t.Fatalf("command = %q, want files.job.status", cmd)
		}
		gotParams, _ = json.Marshal(params)
		return json.RawMessage(`{"job_id":"j1","status":"running","done":2,"total":10}`), nil
	}}
	r := setupFilesRouter(t, "user1", agent)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/jobs/j1", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	// The username is claims-derived (alice), and the job id comes from the path.
	if !strings.Contains(string(gotParams), `"username":"alice"`) {
		t.Fatalf("job.status must use the claims username, not a client value: %s", gotParams)
	}
	if !strings.Contains(string(gotParams), `"job_id":"j1"`) {
		t.Fatalf("job id must come from the path: %s", gotParams)
	}
}

func TestJobStatus_AgentNotFoundIs404(t *testing.T) {
	agent := &mockAgent{callFn: func(_ context.Context, _ string, _ any) (json.RawMessage, error) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: "job not found"}
	}}
	r := setupFilesRouter(t, "user1", agent)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/jobs/nope", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown/foreign job must be 404, got %d; body=%s", w.Code, w.Body.String())
	}
}

func TestJobStatus_RequiresAuth(t *testing.T) {
	agent := &mockAgent{callFn: func(_ context.Context, _ string, _ any) (json.RawMessage, error) {
		t.Fatal("agent must not be called without claims")
		return nil, nil
	}}
	r := setupFilesRouter(t, "", agent) // no claims

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/jobs/j1", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}
