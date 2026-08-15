package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func TestDBBackupHandler(t *testing.T) {
	tests := []struct {
		name      string
		input     dbBackupParams
		wantError bool
		wantCode  string
	}{
		{
			name: "invalid: empty db_name",
			input: dbBackupParams{
				DBName: "",
				Path:   "",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: starts with number",
			input: dbBackupParams{
				DBName: "1alice",
				Path:   "",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: starts with dash",
			input: dbBackupParams{
				DBName: "-bad",
				Path:   "",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: contains slash",
			input: dbBackupParams{
				DBName: "alice/wp",
				Path:   "",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: contains semicolon",
			input: dbBackupParams{
				DBName: "alice;drop",
				Path:   "",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: contains space",
			input: dbBackupParams{
				DBName: "alice wp",
				Path:   "",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: path not under staging dir",
			input: dbBackupParams{
				DBName: "alice",
				Path:   "/tmp/backup.sql",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: old backups dir is no longer accepted",
			input: dbBackupParams{
				DBName: "alice",
				Path:   "/var/lib/jabali/backups/x.sql",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: path with directory traversal",
			input: dbBackupParams{
				DBName: "alice",
				Path:   dbBackupStagingDir + "/../../etc/passwd",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, _ := json.Marshal(tt.input)

			_, err := dbBackupHandler(context.Background(), params)

			if (err != nil) != tt.wantError {
				t.Errorf("dbBackupHandler: expected error = %v, got %v", tt.wantError, err)
			}

			if tt.wantError && tt.wantCode != "" {
				var ae *agentwire.AgentError
				if !isAgentError(err, &ae) {
					t.Errorf("expected AgentError, got %T", err)
				} else if ae.Code != tt.wantCode {
					t.Errorf("expected code %q, got %q", tt.wantCode, ae.Code)
				}
			}
		})
	}
}

// TestReownBackupForPanel pins the GH #1045 fix: a dump written by the agent
// (root) is re-owned to the panel service user so panel-api can read it back.
// The download 500'd because the file landed root:root 0640, unreadable by the
// unprivileged panel user. Seams are injected so the test runs on CI hosts with
// no "jabali" account and without needing root to chown.
func TestReownBackupForPanel(t *testing.T) {
	origLookup := dbBackupLookupUser
	origChown := dbBackupChown
	t.Cleanup(func() {
		dbBackupLookupUser = origLookup
		dbBackupChown = origChown
	})

	t.Run("chowns to the service user and pins 0640", func(t *testing.T) {
		dbBackupLookupUser = func() (*user.User, error) {
			return &user.User{Uid: "996", Gid: "989"}, nil
		}
		var gotPath string
		var gotUID, gotGID int
		dbBackupChown = func(path string, uid, gid int) error {
			gotPath, gotUID, gotGID = path, uid, gid
			return nil // real chown needs root; assert intent instead
		}

		path := filepath.Join(t.TempDir(), "dump.sql")
		if err := os.WriteFile(path, []byte("-- dump\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := reownBackupForPanel(path); err != nil {
			t.Fatalf("reownBackupForPanel: %v", err)
		}
		if gotPath != path {
			t.Errorf("chown path = %q, want %q", gotPath, path)
		}
		if gotUID != 996 || gotGID != 989 {
			t.Errorf("chown target = %d:%d, want 996:989", gotUID, gotGID)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0640 {
			t.Errorf("mode = %o, want 640", perm)
		}
	})

	t.Run("surfaces a lookup failure", func(t *testing.T) {
		dbBackupLookupUser = func() (*user.User, error) {
			return nil, errors.New("unknown user jabali")
		}
		dbBackupChown = func(string, int, int) error {
			t.Fatal("chown must not run when the service user cannot be resolved")
			return nil
		}
		if err := reownBackupForPanel(filepath.Join(t.TempDir(), "x.sql")); err == nil {
			t.Fatal("expected error when service user lookup fails")
		}
	})
}

// JAB-244: with both global dump slots held, a new backup must answer
// retryable-unavailable BEFORE any validation of staging or exec of
// mysqldump.
func TestDBBackup_GlobalSlotsExhausted_Unavailable(t *testing.T) {
	rel1, ok1 := dbBackupSlots.TryAcquire("heldA")
	rel2, ok2 := dbBackupSlots.TryAcquire("heldB")
	if !ok1 || !ok2 {
		t.Fatal("precondition: could not hold both global slots")
	}
	defer rel1()
	defer rel2()

	params, _ := json.Marshal(map[string]string{"db_name": "tenant_db"})
	_, err := dbBackupHandler(context.Background(), params)
	if err == nil {
		t.Fatal("backup admitted with zero free slots")
	}
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeUnavailable {
		t.Fatalf("want CodeUnavailable, got %v", err)
	}
}

// JAB-244: a second dump of the SAME database is refused even with a
// global slot free (per-key cap 1 — duplicate work, duplicate disk).
func TestDBBackup_SameDBAlreadyDumping_Unavailable(t *testing.T) {
	rel, ok := dbBackupSlots.TryAcquire("tenant_db")
	if !ok {
		t.Fatal("precondition: could not hold the db slot")
	}
	defer rel()

	params, _ := json.Marshal(map[string]string{"db_name": "tenant_db"})
	_, err := dbBackupHandler(context.Background(), params)
	var ae *agentwire.AgentError
	if err == nil || !errors.As(err, &ae) || ae.Code != agentwire.CodeUnavailable {
		t.Fatalf("want CodeUnavailable for same-db double dump, got %v", err)
	}
}
