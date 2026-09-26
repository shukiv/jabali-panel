package commands

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// pgExecRecorder swaps the exec seam for psql calls: records each -c SQL,
// prints listOut for a SELECT, and fails a statement containing failOn.
func pgExecRecorder(t *testing.T, listOut, failOn string) *[]string {
	t.Helper()
	var sqls []string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		sql := ""
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "-c" {
				sql = args[i+1]
			}
		}
		sqls = append(sqls, sql)
		if failOn != "" && strings.Contains(sql, failOn) {
			return exec.CommandContext(ctx, "false")
		}
		if strings.HasPrefix(sql, "SELECT datname") {
			return exec.CommandContext(ctx, "printf", "%s", listOut)
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })
	return &sqls
}

// A new database must not be open to every role: Postgres grants CONNECT
// and TEMPORARY to PUBLIC by default, so another tenant's role could connect
// and read the catalog.
func TestPgCreateDB_RevokesPublicAccess(t *testing.T) {
	sqls := pgExecRecorder(t, "", "")
	raw, _ := json.Marshal(dbPgCreateParams{DBName: "alice_shop", Owner: "alice_app"})
	if _, err := dbPgCreateHandler(context.Background(), raw); err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(*sqls) != 2 || !strings.HasPrefix((*sqls)[0], `CREATE DATABASE "alice_shop"`) ||
		(*sqls)[1] != `REVOKE CONNECT, TEMPORARY ON DATABASE "alice_shop" FROM PUBLIC` {
		t.Fatalf("SQL = %q, want CREATE then the PUBLIC revoke", *sqls)
	}
}

// When the revoke fails, the database is dropped again and the call fails:
// never hand out a database every role can connect to.
func TestPgCreateDB_FailedRevokeDropsTheDatabase(t *testing.T) {
	sqls := pgExecRecorder(t, "", "REVOKE CONNECT")
	raw, _ := json.Marshal(dbPgCreateParams{DBName: "alice_shop", Owner: "alice_app"})
	if _, err := dbPgCreateHandler(context.Background(), raw); err == nil {
		t.Fatal("create succeeded although the revoke failed")
	}
	if last := (*sqls)[len(*sqls)-1]; last != `DROP DATABASE IF EXISTS "alice_shop"` {
		t.Fatalf("last SQL = %q, want the database dropped", last)
	}
}

// The sweep revokes on every database PUBLIC can still reach and reports
// their names; the maintenance database and templates are excluded.
func TestPgRevokePublicAccess_RevokesWhatIsStillOpen(t *testing.T) {
	sqls := pgExecRecorder(t, "alice_shop\nbob_blog\n", "")
	out, err := dbPgRevokePublicHandler(context.Background(), nil)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := out.(dbPgRevokePublicResponse).Revoked; len(got) != 2 || got[0] != "alice_shop" || got[1] != "bob_blog" {
		t.Fatalf("revoked = %v", got)
	}
	if len(*sqls) != 2 {
		t.Fatalf("SQL = %q, want the list then one revoke block", *sqls)
	}
	for _, sql := range *sqls {
		if !strings.Contains(sql, "NOT datistemplate AND datname <> 'postgres'") {
			t.Fatalf("SQL without the template/maintenance exclusion: %s", sql)
		}
	}
	if !strings.Contains((*sqls)[1], "REVOKE CONNECT, TEMPORARY ON DATABASE %I FROM PUBLIC") {
		t.Fatalf("revoke block = %s", (*sqls)[1])
	}
}

// Nothing open: nothing to run.
func TestPgRevokePublicAccess_NothingOpenRunsNothing(t *testing.T) {
	sqls := pgExecRecorder(t, "", "")
	if _, err := dbPgRevokePublicHandler(context.Background(), nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(*sqls) != 1 {
		t.Fatalf("SQL = %q, want only the list", *sqls)
	}
}

// A failed revoke is reported so the panel retries.
func TestPgRevokePublicAccess_ReportsAFailedRevoke(t *testing.T) {
	pgExecRecorder(t, "alice_shop\n", "DO $$")
	if _, err := dbPgRevokePublicHandler(context.Background(), nil); err == nil {
		t.Fatal("failed revoke was swallowed")
	}
}
