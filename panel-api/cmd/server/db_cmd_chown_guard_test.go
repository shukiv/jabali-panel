package main

import (
	"os"
	"strings"
	"testing"
)

// TestCLIDBChown_RoutesThroughDbops pins the GH #1609 CLI parity for the REST
// admin change-of-owner (#1619). The operator CLI `jabali db chown` must route
// the whole reassignment — the shared-user / prefix / collision refusals, the
// engine guard, the rename + regrant fan-out — through the one canonical
// dbops.ReassignDatabaseOwner the REST handler also uses, so CLI and REST refuse
// and succeed identically. cmd/server has no DB/agent fixture, so this
// source-pins the delegation; the reassignment behaviour itself is tested in
// internal/dbops (dbops_chown_test.go).
func TestCLIDBChown_RoutesThroughDbops(t *testing.T) {
	src, err := os.ReadFile("db_cmd.go")
	if err != nil {
		t.Fatalf("read db_cmd.go: %v", err)
	}
	s := string(src)

	// It must delegate to the shared function with the shared deps + input —
	// not re-implement the rename/regrant transcript.
	if !strings.Contains(s, "dbops.ReassignDatabaseOwner(ctx, dbopsDeps(), dbops.ReassignInput{") {
		t.Fatal("CLI db chown must route through dbops.ReassignDatabaseOwner with dbopsDeps() (GH #1609)")
	}

	// The reassignment fans out multiple agent renames; a 60s cancel mid-run can
	// leave the box half-moved. The verb must use the 5-minute ceiling the REST
	// handler and the domain chown CLI use — NOT the 60s create/delete default.
	if !strings.Contains(s, "context.WithTimeout(cmd.Context(), 5*time.Minute)") {
		t.Fatal("db chown must use the 5-minute timeout (rename/regrant fan-out); a 60s cancel can half-move the box (GH #1609)")
	}

	// The audit subject on failure is the OLD owner, so the old owner id must be
	// captured before the move (the shared fn mutates the row to the new owner).
	if !strings.Contains(s, "oldOwnerID := db.UserID") {
		t.Fatal("db chown must capture the old owner id before the move for the failure-audit subject (GH #1609)")
	}

	// Both outcomes are audited under a single action, mirroring the REST
	// auditDBChown and the domain chown CLI sibling.
	for _, want := range []string{
		`cliAuditOK(ctx, "database.chown", "database", db.ID, &newOwner.ID)`,
		`cliAuditErr(ctx, "database.chown", "database", db.ID, &oldOwnerID)`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("db chown must audit both outcomes: missing %q (GH #1609)", want)
		}
	}

	// A refusal must surface a clean operator message, not a raw "dbops:" string,
	// so every reassign sentinel is mapped in mapDBopsErr.
	for _, sentinel := range []string{
		"dbops.ErrReassignSameOwner",
		"dbops.ErrReassignOwnerInvalid",
		"dbops.ErrReassignEngine",
		"dbops.ErrReassignPrefix",
		"dbops.ErrReassignSharedUser",
		"dbops.ErrReassignCollision",
	} {
		if !strings.Contains(s, sentinel) {
			t.Fatalf("mapDBopsErr must map %s so a refusal reads cleanly on the CLI (GH #1609)", sentinel)
		}
	}
}
