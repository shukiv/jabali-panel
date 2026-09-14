package main

// JAB-364 AC4: when the agent teardown (docker_app.delete) fails, the CLI delete
// must leave an explicit retryable panel state ("failed") instead of the app's
// stale prior status (e.g. "running"), which would read as a false-OK in
// `docker-app list`. This exercises markDockerAppTeardownFailed — the helper the
// delete RunE calls on teardown failure — the same way docker_app_domain_cleanup_test
// exercises cleanupDockerAppDomains: through a tiny injectable fake.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type fakeStatusRepo struct {
	called     bool
	lastID     string
	lastStatus string
	lastMsg    *string
}

func (f *fakeStatusRepo) UpdateStatus(_ context.Context, id, status string, lastError *string) error {
	f.called = true
	f.lastID, f.lastStatus, f.lastMsg = id, status, lastError
	return nil
}

func TestMarkDockerAppTeardownFailed_WritesRetryableFailedState(t *testing.T) {
	repo := &fakeStatusRepo{}
	cause := errors.New("compose down: exit 1\nstderr: container still running")

	err := markDockerAppTeardownFailed(context.Background(), repo, "app-x", cause)

	// The agent failure must still surface to the operator (nonzero CLI exit).
	if err == nil {
		t.Fatal("expected a non-nil error return so the CLI exits nonzero on teardown failure")
	}
	// The row must be marked failed (retryable), NOT left at its stale prior status.
	if !repo.called {
		t.Fatal("teardown failure did not write any status — row keeps its stale prior status (false-OK)")
	}
	if repo.lastStatus != models.DockerAppStatusFailed {
		t.Fatalf("row must be marked %q (retryable); got %q", models.DockerAppStatusFailed, repo.lastStatus)
	}
	if repo.lastID != "app-x" {
		t.Fatalf("status written for the wrong app id: got %q, want %q", repo.lastID, "app-x")
	}
	// Message parity with the HTTP delete door: "teardown failed: <first line>".
	if repo.lastMsg == nil {
		t.Fatal("expected a last_error message on the failed row, got nil")
	}
	if !strings.HasPrefix(*repo.lastMsg, "teardown failed: ") {
		t.Fatalf("expected a 'teardown failed: ' message, got %q", *repo.lastMsg)
	}
	// Only the first line of the agent error — no multi-line stderr leak into the
	// status column.
	if strings.Contains(*repo.lastMsg, "\n") {
		t.Fatalf("status message must be a single line, got %q", *repo.lastMsg)
	}
	if strings.Contains(*repo.lastMsg, "stderr") {
		t.Fatalf("status message leaked past the first line: %q", *repo.lastMsg)
	}
}
