package commands

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// TestDBPgRestoreHandler_Validation pins the input validation for
// db.postgres.restore — all cases fail BEFORE any psql runs, so they need no
// live Postgres.
func TestDBPgRestoreHandler_Validation(t *testing.T) {
	tests := []struct {
		name  string
		input dbPgRestoreParams
	}{
		{"empty db_name", dbPgRestoreParams{DBName: "", Path: "/var/lib/jabali/restore/x.sql"}},
		{"bad db_name (semicolon)", dbPgRestoreParams{DBName: "a;drop", Path: "/var/lib/jabali/restore/x.sql"}},
		{"bad db_name (quote)", dbPgRestoreParams{DBName: "a'b", Path: "/var/lib/jabali/restore/x.sql"}},
		{"bad owner_role", dbPgRestoreParams{DBName: "good", OwnerRole: "bad;role", Path: "/var/lib/jabali/restore/x.sql"}},
		{"bad grant role", dbPgRestoreParams{DBName: "good", GrantRoles: []string{"ok", "b\"ad"}, Path: "/var/lib/jabali/restore/x.sql"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, _ := json.Marshal(tt.input)
			_, err := dbPgRestoreHandler(context.Background(), params)
			var ae *agentwire.AgentError
			if !errors.As(err, &ae) {
				t.Fatalf("expected AgentError, got %T (%v)", err, err)
			}
			if ae.Code != agentwire.CodeInvalidArgument {
				t.Errorf("code = %q, want %q", ae.Code, agentwire.CodeInvalidArgument)
			}
		})
	}
}

// pgShadowRole must be deterministic per-db, pgValidIdent-safe, and comfortably
// under Postgres's 63-char NAMEDATALEN even for a maximal 63-char db name.
func TestPgShadowRole(t *testing.T) {
	long := strings.Repeat("d", 63)
	for _, db := range []string{"shop", "a", long, "tenant_db-1"} {
		r := pgShadowRole(db)
		if r != pgShadowRole(db) {
			t.Errorf("pgShadowRole(%q) not deterministic", db)
		}
		if len(r) >= 63 {
			t.Errorf("pgShadowRole(%q) = %q is %d chars, must be < 63", db, r, len(r))
		}
		if !pgValidIdent(r) {
			t.Errorf("pgShadowRole(%q) = %q is not a valid pg identifier", db, r)
		}
		if !strings.HasPrefix(r, "jbsr_") {
			t.Errorf("pgShadowRole(%q) = %q missing jbsr_ prefix", db, r)
		}
	}
	// Distinct dbs get distinct roles.
	if pgShadowRole("a") == pgShadowRole("b") {
		t.Error("distinct dbs collided on the same shadow role")
	}
}

// pgRestoreTmpDB (the staging database name for the load-then-swap) has the same
// bounds as the shadow role and must never collide with it.
func TestPgRestoreTmpDB(t *testing.T) {
	long := strings.Repeat("d", 63)
	for _, db := range []string{"shop", "a", long, "tenant_db-1"} {
		tmp := pgRestoreTmpDB(db)
		if tmp != pgRestoreTmpDB(db) {
			t.Errorf("pgRestoreTmpDB(%q) not deterministic", db)
		}
		if len(tmp) >= 63 {
			t.Errorf("pgRestoreTmpDB(%q) = %q is %d chars, must be < 63", db, tmp, len(tmp))
		}
		if !pgValidIdent(tmp) {
			t.Errorf("pgRestoreTmpDB(%q) = %q is not a valid pg identifier", db, tmp)
		}
		if tmp == pgShadowRole(db) {
			t.Errorf("staging db name collides with shadow role for %q", db)
		}
	}
}
