package main

import (
	"os"
	"strings"
	"testing"
)

// TestCLIDBDelete_RoutesThroughDbops pins the JAB-275 fix. The operator CLI
// `jabali db delete` routes the whole deletion path — attachment refusal,
// engine-correct grant revoke, schema drop, metadata teardown — through the
// one canonical dbops.Delete the REST handler also uses, and wires every
// delete-path collaborator so dbops does not fail closed on a nil dep.
// cmd/server has no DB/agent fixture, so this source-pins the delegation; the
// deletion behaviour itself is tested in internal/dbops.
func TestCLIDBDelete_RoutesThroughDbops(t *testing.T) {
	src, err := os.ReadFile("db_cmd.go")
	if err != nil {
		t.Fatalf("read db_cmd.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "dbops.Delete(ctx, dbopsDeps(), dbops.DeleteInput{ID: id})") {
		t.Fatal("CLI db delete must route through dbops.Delete (JAB-275)")
	}
	// The CLI must not hand-roll the drop — dbops owns engine dispatch so a
	// postgres row never reaches MariaDB (GH #1013).
	for _, banned := range []string{`"db.drop"`, `"db.postgres.drop_db"`, `"db_user.revoke"`} {
		if strings.Contains(s, banned) {
			t.Fatalf("CLI must not issue %s itself — dbops.Delete owns the deletion transcript (JAB-275)", banned)
		}
	}
	// It must wire the delete-path collaborators dbops requires.
	for _, want := range []string{"DatabaseGrants:", "DatabaseUsers:", "Installs:"} {
		if !strings.Contains(s, want) {
			t.Fatalf("dbopsDeps missing %q — dbops.Delete fails closed without it (JAB-275)", want)
		}
	}
}
