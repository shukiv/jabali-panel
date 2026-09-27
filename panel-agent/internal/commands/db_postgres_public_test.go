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
	out, err := dbPgRevokePublicHandler(context.Background(), json.RawMessage(`{"grants":{}}`))
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
	if _, err := dbPgRevokePublicHandler(context.Background(), json.RawMessage(`{"grants":{}}`)); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(*sqls) != 1 {
		t.Fatalf("SQL = %q, want only the list", *sqls)
	}
}

// A failed revoke is reported so the panel retries.
func TestPgRevokePublicAccess_ReportsAFailedRevoke(t *testing.T) {
	pgExecRecorder(t, "alice_shop\n", "REVOKE CONNECT, TEMPORARY ON DATABASE %I")
	if _, err := dbPgRevokePublicHandler(context.Background(), json.RawMessage(`{"grants":{}}`)); err == nil {
		t.Fatal("failed revoke was swallowed")
	}
}

// A restored database keeps its tenant roles' CONNECT. REASSIGN OWNED makes
// the first role the staging db's owner, and ALTER DATABASE ... OWNER TO
// postgres hands the owner's ACL entry to postgres. The database-level GRANT
// must therefore come after the owner change; before it, the role ended up
// with no CONNECT of its own (box-seen once PUBLIC was revoked).
func TestPgRestorePostPass_GrantsTheDatabaseAfterTheOwnerChange(t *testing.T) {
	sqls := pgExecRecorder(t, "", "")
	if aerr := pgRestorePostPass(context.Background(), "jbrt_alice_shop", "jbrs_alice_shop", "alice_app", []string{"alice_app", "alice_ro"}); aerr != nil {
		t.Fatalf("post-pass: %v", aerr)
	}
	ownerAt := -1
	grantAt := map[string]int{}
	for i, sql := range *sqls {
		if sql == `ALTER DATABASE "jbrt_alice_shop" OWNER TO postgres` {
			ownerAt = i
		}
		for _, role := range []string{"alice_app", "alice_ro"} {
			if sql == `GRANT ALL PRIVILEGES ON DATABASE "jbrt_alice_shop" TO "`+role+`"` {
				grantAt[role] = i
			}
		}
	}
	if ownerAt < 0 || len(grantAt) != 2 {
		t.Fatalf("SQL = %q, want the owner change and a database grant per role", *sqls)
	}
	for role, at := range grantAt {
		if at < ownerAt {
			t.Fatalf("database grant for %s ran before the owner change (SQL = %q)", role, *sqls)
		}
	}
}

// Each panel-granted role gets its own database grant before PUBLIC is
// revoked: a role that reached its database through PUBLIC alone (a restore
// before pgRestorePostPass was fixed) must not be locked out.
func TestPgRevokePublicAccess_RegrantsPanelRolesFirst(t *testing.T) {
	sqls := pgExecRecorder(t, "alice_shop\n", "")
	out, err := dbPgRevokePublicHandler(context.Background(), json.RawMessage(`{"grants":{"alice_shop":["alice_app","alice_ro"]}}`))
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := out.(dbPgRevokePublicResponse).Regranted; len(got) != 2 {
		t.Fatalf("regranted = %v, want both roles", got)
	}
	grants, revokeAt := 0, -1
	for i, sql := range *sqls {
		if strings.Contains(sql, `GRANT ALL PRIVILEGES ON DATABASE "alice_shop" TO "alice_`) {
			grants++
			if revokeAt >= 0 {
				t.Fatalf("a grant ran after the revoke: %q", *sqls)
			}
		}
		if strings.Contains(sql, "REVOKE CONNECT, TEMPORARY ON DATABASE %I FROM PUBLIC") {
			revokeAt = i
		}
	}
	if grants != 2 || revokeAt < 0 {
		t.Fatalf("SQL = %q, want two grants then the revoke", *sqls)
	}
}

// A failed grant stops the sweep before any revoke.
func TestPgRevokePublicAccess_FailedGrantRevokesNothing(t *testing.T) {
	sqls := pgExecRecorder(t, "alice_shop\n", "GRANT ALL PRIVILEGES")
	if _, err := dbPgRevokePublicHandler(context.Background(), json.RawMessage(`{"grants":{"alice_shop":["alice_app"]}}`)); err == nil {
		t.Fatal("a failed grant must be reported")
	}
	for _, sql := range *sqls {
		if strings.Contains(sql, "REVOKE") {
			t.Fatalf("revoked although a grant failed: %q", *sqls)
		}
	}
}

// Without the grants field there is nothing to re-grant from, so nothing is
// revoked; an invalid role name is refused before any SQL runs.
func TestPgRevokePublicAccess_RefusesMissingGrantsAndBadNames(t *testing.T) {
	for _, body := range []string{`{}`, `{"grants":{"alice_shop":["bad\"role"]}}`, `{"grants":{"bad;db":["alice_app"]}}`} {
		sqls := pgExecRecorder(t, "alice_shop\n", "")
		if _, err := dbPgRevokePublicHandler(context.Background(), json.RawMessage(body)); err == nil {
			t.Fatalf("%s: accepted", body)
		}
		if len(*sqls) != 0 {
			t.Fatalf("%s: SQL ran: %q", body, *sqls)
		}
	}
}
