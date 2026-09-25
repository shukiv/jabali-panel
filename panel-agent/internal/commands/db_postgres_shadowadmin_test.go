package commands

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// captureSQL swaps the exec seam for one that records every `-c <sql>` argument
// and runs a harmless `true`, so a handler's psql calls succeed while the test
// inspects the SQL text. Non-parallel; restored on cleanup.
func captureSQL(t *testing.T) *[]string {
	t.Helper()
	var seen []string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "-c" {
				seen = append(seen, args[i+1])
			}
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })
	return &seen
}

func TestShadowadminGrantMembers_InvalidPanelUsername(t *testing.T) {
	_ = captureSQL(t)
	_, err := dbPostgresShadowadminGrantMembersHandler(context.Background(),
		json.RawMessage(`{"panel_username":"Bad Name","member_roles":["x"]}`))
	if err == nil {
		t.Fatal("expected invalid_argument for a bad panel username")
	}
}

func TestShadowadminGrantMembers_EmptyIsNoop(t *testing.T) {
	seen := captureSQL(t)
	_, err := dbPostgresShadowadminGrantMembersHandler(context.Background(),
		json.RawMessage(`{"panel_username":"alice","member_roles":[]}`))
	if err != nil {
		t.Fatalf("empty member list should be a no-op success, got %v", err)
	}
	if len(*seen) != 0 {
		t.Fatalf("no SQL should run for an empty member list, got %v", *seen)
	}
}

func TestShadowadminGrantMembers_GrantsExplicitListWithGuards(t *testing.T) {
	seen := captureSQL(t)
	// alice_pgadmin (self) and "bad;name" (invalid ident) must be filtered out;
	// alice_app and alice_web must be granted.
	_, err := dbPostgresShadowadminGrantMembersHandler(context.Background(),
		json.RawMessage(`{"panel_username":"alice","member_roles":["alice_app","alice_pgadmin","bad;name","alice_web"]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*seen) != 1 {
		t.Fatalf("expected exactly one GRANT statement, got %d: %v", len(*seen), *seen)
	}
	sql := (*seen)[0]

	// Explicit allow-list, not a pattern.
	if !strings.Contains(sql, `'alice_app'`) || !strings.Contains(sql, `'alice_web'`) {
		t.Fatalf("SQL must reference the passed roles explicitly:\n%s", sql)
	}
	if strings.Contains(sql, "LIKE") {
		t.Fatalf("membership must NOT use a LIKE pattern (cross-tenant risk):\n%s", sql)
	}
	// Self and invalid ident filtered out.
	if strings.Contains(sql, "'bad;name'") {
		t.Fatalf("invalid identifier leaked into SQL:\n%s", sql)
	}
	if strings.Count(sql, "'alice_pgadmin'") != 2 {
		// appears exactly twice: the `<> 'alice_pgadmin'` guard and the grantee —
		// never as a membership TARGET in the ARRAY.
		t.Fatalf("alice_pgadmin should appear only as the guard + grantee, not as a target:\n%s", sql)
	}
	// Defence-in-depth guards.
	for _, want := range []string{"NOT rolsuper", "rolcanlogin", "<> 'alice_pgadmin'", "GRANT %I TO %I"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("SQL missing guard %q:\n%s", want, sql)
		}
	}
}

// captureArgs records every psql argv (so a test can see -d <db> and -c <sql>)
// and fails any call whose -c SQL contains failOn.
func captureArgs(t *testing.T, failOn string) *[][]string {
	t.Helper()
	var calls [][]string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "-c" && failOn != "" && strings.Contains(args[i+1], failOn) {
				return exec.CommandContext(ctx, "false")
			}
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })
	return &calls
}

func sqlOf(call []string) string {
	for i := 0; i < len(call)-1; i++ {
		if call[i] == "-c" {
			return call[i+1]
		}
	}
	return ""
}

func dbOf(call []string) string {
	for i := 0; i < len(call)-1; i++ {
		if call[i] == "-d" {
			return call[i+1]
		}
	}
	return ""
}

// ensure no longer grants any database: the old LIKE '<user>\_%' matched a
// sibling tenant's databases (panel usernames may contain '_').
func TestShadowadminEnsure_GrantsNoDatabase(t *testing.T) {
	seen := captureSQL(t)
	if _, err := dbPostgresShadowadminEnsureHandler(context.Background(),
		json.RawMessage(`{"panel_username":"alice"}`)); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	for _, sql := range *seen {
		if strings.Contains(sql, "LIKE") || strings.Contains(sql, "ON DATABASE") {
			t.Fatalf("ensure must not grant databases by pattern:\n%s", sql)
		}
	}
}

func TestShadowadminGrantSchema_DatabaseGrantsAreExactAndStaleOnesRevoked(t *testing.T) {
	calls := captureArgs(t, "")
	if _, err := dbPostgresShadowadminGrantSchemaHandler(context.Background(),
		json.RawMessage(`{"panel_username":"alice","db_names":["alice_pg","bad;name"]}`)); err != nil {
		t.Fatalf("grant_schema: %v", err)
	}
	if len(*calls) != 2 {
		t.Fatalf("want 1 database-level call + 1 per-DB schema call, got %d: %v", len(*calls), *calls)
	}
	dbLevel := sqlOf((*calls)[0])
	if dbOf((*calls)[0]) != "" {
		t.Fatalf("the database-level grant runs on the maintenance DB, got -d %q", dbOf((*calls)[0]))
	}
	for _, want := range []string{
		"ARRAY['alice_pg']::text[]",
		"GRANT ALL PRIVILEGES ON DATABASE %I TO %I",
		"REVOKE ALL PRIVILEGES ON DATABASE %I FROM %I",
		"aclexplode(d.datacl)",
		"d.datdba <> a.grantee",
		"rolname = 'alice_pgadmin'",
	} {
		if !strings.Contains(dbLevel, want) {
			t.Fatalf("database-level SQL missing %q:\n%s", want, dbLevel)
		}
	}
	if strings.Contains(dbLevel, "LIKE") || strings.Contains(dbLevel, "bad;name") {
		t.Fatalf("no pattern and no invalid name in the database-level SQL:\n%s", dbLevel)
	}
	if dbOf((*calls)[1]) != "alice_pg" || !strings.Contains(sqlOf((*calls)[1]), "GRANT ALL ON SCHEMA public") {
		t.Fatalf("second call should be the schema grant on alice_pg: %v", (*calls)[1])
	}
}

func TestShadowadminGrantSchema_EmptyListStillRevokes(t *testing.T) {
	calls := captureArgs(t, "")
	if _, err := dbPostgresShadowadminGrantSchemaHandler(context.Background(),
		json.RawMessage(`{"panel_username":"alice","db_names":[]}`)); err != nil {
		t.Fatalf("grant_schema: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("want only the database-level call, got %d: %v", len(*calls), *calls)
	}
	sql := sqlOf((*calls)[0])
	if !strings.Contains(sql, "ARRAY[]::text[]") || !strings.Contains(sql, "REVOKE ALL PRIVILEGES ON DATABASE") {
		t.Fatalf("an empty list must still revoke stale database grants:\n%s", sql)
	}
}

func TestShadowadminGrantSchema_DatabaseLevelFailureFails(t *testing.T) {
	_ = captureArgs(t, "REVOKE ALL PRIVILEGES ON DATABASE")
	if _, err := dbPostgresShadowadminGrantSchemaHandler(context.Background(),
		json.RawMessage(`{"panel_username":"alice","db_names":["alice_pg"]}`)); err == nil {
		t.Fatal("a failed database-level grant/revoke must fail the call so the panel refuses the Adminer open")
	}
}
