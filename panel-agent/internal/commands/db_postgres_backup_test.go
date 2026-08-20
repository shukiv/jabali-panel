package commands

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// TestDBPgBackupHandler pins the input validation for db.postgres.backup. Like
// TestDBBackupHandler, these cases fail BEFORE pg_dump is ever exec'd, so they
// run without a live Postgres.
func TestDBPgBackupHandler(t *testing.T) {
	tests := []struct {
		name     string
		input    dbPgBackupParams
		wantCode string
	}{
		{"invalid: empty db_name", dbPgBackupParams{DBName: ""}, agentwire.CodeInvalidArgument},
		{"invalid: starts with number", dbPgBackupParams{DBName: "1alice"}, agentwire.CodeInvalidArgument},
		{"invalid: starts with dash", dbPgBackupParams{DBName: "-bad"}, agentwire.CodeInvalidArgument},
		{"invalid: contains slash", dbPgBackupParams{DBName: "alice/wp"}, agentwire.CodeInvalidArgument},
		{"invalid: contains semicolon", dbPgBackupParams{DBName: "alice;drop"}, agentwire.CodeInvalidArgument},
		{"invalid: contains space", dbPgBackupParams{DBName: "alice wp"}, agentwire.CodeInvalidArgument},
		{"invalid: contains quote", dbPgBackupParams{DBName: "alice'wp"}, agentwire.CodeInvalidArgument},
		{"invalid: contains backslash", dbPgBackupParams{DBName: `alice\wp`}, agentwire.CodeInvalidArgument},
		{"invalid: path not under staging", dbPgBackupParams{DBName: "alice", Path: "/tmp/x.sql"}, agentwire.CodeInvalidArgument},
		{"invalid: old backups dir rejected", dbPgBackupParams{DBName: "alice", Path: "/var/lib/jabali/backups/x.sql"}, agentwire.CodeInvalidArgument},
		{"invalid: directory traversal", dbPgBackupParams{DBName: "alice", Path: dbBackupStagingDir + "/../../etc/passwd"}, agentwire.CodeInvalidArgument},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, _ := json.Marshal(tt.input)
			_, err := dbPgBackupHandler(context.Background(), params)
			var ae *agentwire.AgentError
			if !errors.As(err, &ae) {
				t.Fatalf("expected AgentError, got %T (%v)", err, err)
			}
			if ae.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", ae.Code, tt.wantCode)
			}
		})
	}
}

// The pg backup shares dbBackupSlots with db.backup / db.postgres.dump, keyed
// "pg:<db>". With both host-wide slots held a new pg backup answers retryable-
// unavailable before any pg_dump exec.
func TestDBPgBackup_GlobalSlotsExhausted_Unavailable(t *testing.T) {
	rel1, ok1 := dbBackupSlots.TryAcquire("heldA")
	rel2, ok2 := dbBackupSlots.TryAcquire("heldB")
	if !ok1 || !ok2 {
		t.Fatal("precondition: could not hold both global slots")
	}
	defer rel1()
	defer rel2()

	params, _ := json.Marshal(dbPgBackupParams{DBName: "tenant_db"})
	_, err := dbPgBackupHandler(context.Background(), params)
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeUnavailable {
		t.Fatalf("want CodeUnavailable, got %v", err)
	}
}

// A second pg backup of the same db is refused even with a global slot free
// (per-key cap 1) — the key is "pg:<db>", distinct from the MariaDB "<db>" key.
func TestDBPgBackup_SameDBAlreadyDumping_Unavailable(t *testing.T) {
	rel, ok := dbBackupSlots.TryAcquire("pg:tenant_db")
	if !ok {
		t.Fatal("precondition: could not hold the pg db slot")
	}
	defer rel()

	params, _ := json.Marshal(dbPgBackupParams{DBName: "tenant_db"})
	_, err := dbPgBackupHandler(context.Background(), params)
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeUnavailable {
		t.Fatalf("want CodeUnavailable for same-db double dump, got %v", err)
	}
}
