package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fullRestoreUsers: "alice" exists, everyone else is missing.
type fullRestoreUsers struct {
	repository.UserRepository
}

func (fullRestoreUsers) FindByUsername(_ context.Context, name string) (*models.User, error) {
	if name == "alice" {
		u := "alice"
		return &models.User{ID: "UA", Username: &u}, nil
	}
	return nil, repository.ErrNotFound
}

func fullMarker(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "marker.json")
}

func readFullOutcome(t *testing.T, path string) fullBackupOutcome {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	var o fullBackupOutcome
	if err := json.Unmarshal(b, &o); err != nil {
		t.Fatalf("marker json: %v", err)
	}
	return o
}

// GH #1408 slice 2: an agent that predates extract_uploaded must FAIL CLOSED —
// never silently fall back to the no-metadata legacy path.
func TestRunFullRestore_FailClosedOnOldAgent(t *testing.T) {
	ag := &mockAgent{callFn: func(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
		if cmd == "system.fullbackup.extract_uploaded" {
			return nil, &agentwire.AgentError{Code: agentwire.CodeUnknownCommand, Message: "unknown command"}
		}
		return nil, fmt.Errorf("unexpected agent call %q", cmd)
	}}
	h := &backupHandler{cfg: BackupHandlerConfig{Agent: ag, Users: fullRestoreUsers{}}}
	marker := fullMarker(t)
	h.runFullRestore("/x/container.tar.zst", marker, fullRestoreApplyRequest{})

	o := readFullOutcome(t, marker)
	if o.Status != "failed" || !strings.Contains(o.Error, "updated agent") {
		t.Fatalf("want failed + 'updated agent', got %+v", o)
	}
}

// A selected user that doesn't exist is SKIPPED (not restored) when
// create-missing is off; the extraction stage is always cleaned up.
func TestRunFullRestore_MissingUserGateAndCleanup(t *testing.T) {
	cleaned := false
	restored := false
	ag := &mockAgent{callFn: func(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
		switch cmd {
		case "system.fullbackup.extract_uploaded":
			return json.RawMessage(`{"stage":"/var/lib/jabali-uploads/fullrestore-X","users":[` +
				`{"username":"alice","inner_path":"/var/lib/jabali-uploads/fullrestore-X/users/alice.tar.zst"},` +
				`{"username":"bob","inner_path":"/var/lib/jabali-uploads/fullrestore-X/users/bob.tar.zst"}]}`), nil
		case "backup.restore_from_tar":
			restored = true
			return json.RawMessage(`{"applied":["home → /home/alice"]}`), nil
		case "system.fullbackup.cleanup_stage":
			cleaned = true
			return json.RawMessage(`{"cleaned":"x"}`), nil
		}
		return nil, fmt.Errorf("unexpected agent call %q", cmd)
	}}
	h := &backupHandler{cfg: BackupHandlerConfig{Agent: ag, Users: fullRestoreUsers{}}}
	marker := fullMarker(t)
	// Only bob selected, create-missing OFF → bob is skipped-not-created, and no
	// account is ever created (Packages nil would 501 the create path anyway).
	h.runFullRestore("/x/container.tar.zst", marker, fullRestoreApplyRequest{
		Usernames: []string{"bob"}, CreateMissing: false,
	})

	o := readFullOutcome(t, marker)
	if o.Status != "done" {
		t.Fatalf("want done, got %+v", o)
	}
	joined := strings.Join(o.Packed, " | ")
	if !strings.Contains(joined, "bob: user does not exist") {
		t.Errorf("bob must be reported as missing-not-created, got %q", joined)
	}
	// alice not selected → skipped, never restored.
	if restored {
		t.Error("no user was selected for restore; restore_from_tar must not run")
	}
	sk := strings.Join(o.Skipped, ",")
	if !strings.Contains(sk, "alice") {
		t.Errorf("unselected alice must be skipped, got skipped=%q", sk)
	}
	if !cleaned {
		t.Error("the extraction stage must be cleaned up")
	}
}
