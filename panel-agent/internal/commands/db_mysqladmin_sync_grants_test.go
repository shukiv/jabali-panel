package commands

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// shadowMySQL swaps the exec seam: a `mysql -N -B -e <select>` call prints the
// stored mysql.db Db values (hex-encoded, as the handler asks for), every other
// `mysql -e <sql>` call is recorded and succeeds — unless its SQL contains
// failOn. Non-parallel; restored on cleanup.
func shadowMySQL(t *testing.T, stored []string, failOn string) *[]string {
	t.Helper()
	var execs []string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		sql := ""
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "-e" {
				sql = args[i+1]
			}
		}
		if strings.HasPrefix(sql, "SELECT HEX(Db)") {
			lines := make([]string, 0, len(stored))
			for _, s := range stored {
				lines = append(lines, strings.ToUpper(hex.EncodeToString([]byte(s))))
			}
			return exec.CommandContext(ctx, "printf", "%s", strings.Join(lines, "\n"))
		}
		execs = append(execs, sql)
		if failOn != "" && strings.Contains(sql, failOn) {
			return exec.CommandContext(ctx, "false")
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })
	return &execs
}

func TestMysqladminEnsure_GrantsNoDatabase(t *testing.T) {
	execs := shadowMySQL(t, nil, "")
	if _, err := dbMysqladminEnsureHandler(context.Background(), json.RawMessage(`{"panel_username":"alice"}`)); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	for _, sql := range *execs {
		if strings.Contains(sql, "GRANT") || strings.Contains(sql, `\_%`) {
			t.Fatalf("ensure must not grant any database (the old alice\\_%% wildcard matched sibling tenants):\n%s", sql)
		}
	}
}

func TestMysqladminSyncGrants_GrantsExactNamesAndRevokesEverythingElse(t *testing.T) {
	// Stored: the old wildcard, a database that is still the tenant's, and a
	// database that no longer is.
	execs := shadowMySQL(t, []string{`alice\_%`, `alice\_shop`, `alice\_gone`}, "")
	out, err := dbMysqladminSyncGrantsHandler(context.Background(),
		json.RawMessage(`{"panel_username":"alice","db_names":["alice_shop","alice_my_db","bad;name"]}`))
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	all := strings.Join(*execs, "\n")

	for _, want := range []string{
		"REVOKE ALL PRIVILEGES ON `alice\\_%`.* FROM 'alice_mysqladmin'@'localhost';",
		"REVOKE ALL PRIVILEGES ON `alice\\_gone`.* FROM 'alice_mysqladmin'@'localhost';",
		"GRANT ALL PRIVILEGES ON `alice\\_shop`.* TO 'alice_mysqladmin'@'localhost';",
		"GRANT ALL PRIVILEGES ON `alice\\_my\\_db`.* TO 'alice_mysqladmin'@'localhost';",
		"FLUSH PRIVILEGES;",
	} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %q in:\n%s", want, all)
		}
	}
	if strings.Contains(all, "REVOKE ALL PRIVILEGES ON `alice\\_shop`") {
		t.Fatalf("a listed database must not be revoked:\n%s", all)
	}
	if strings.Contains(all, "bad;name") {
		t.Fatalf("an invalid database name leaked into SQL:\n%s", all)
	}
	if strings.Contains(all, "`alice_shop`") || strings.Contains(all, "`alice_my_db`") {
		t.Fatalf("a granted name must escape '_' (GRANT treats it as a wildcard):\n%s", all)
	}
	// Revoke runs before grant.
	if strings.Index(all, "REVOKE") > strings.Index(all, "GRANT ALL") {
		t.Fatalf("stale grants must be revoked before new ones are granted:\n%s", all)
	}
	resp := out.(dbMysqladminSyncGrantsResponse)
	if resp.Granted != 2 || resp.Revoked != 2 {
		t.Fatalf("granted=%d revoked=%d, want 2/2", resp.Granted, resp.Revoked)
	}
}

func TestMysqladminSyncGrants_EmptyListRevokesAll(t *testing.T) {
	execs := shadowMySQL(t, []string{`alice\_%`}, "")
	if _, err := dbMysqladminSyncGrantsHandler(context.Background(),
		json.RawMessage(`{"panel_username":"alice","db_names":[]}`)); err != nil {
		t.Fatalf("sync: %v", err)
	}
	all := strings.Join(*execs, "\n")
	if !strings.Contains(all, "REVOKE ALL PRIVILEGES ON `alice\\_%`.*") || strings.Contains(all, "GRANT ALL") {
		t.Fatalf("an empty list must revoke the stored wildcard and grant nothing:\n%s", all)
	}
}

func TestMysqladminSyncGrants_NothingToDoRunsNothing(t *testing.T) {
	execs := shadowMySQL(t, []string{`alice\_shop`}, "")
	if _, err := dbMysqladminSyncGrantsHandler(context.Background(),
		json.RawMessage(`{"panel_username":"alice","db_names":["alice_shop"]}`)); err != nil {
		t.Fatalf("sync: %v", err)
	}
	// The grant is re-issued (idempotent) but nothing is revoked.
	for _, sql := range *execs {
		if strings.Contains(sql, "REVOKE") {
			t.Fatalf("nothing should be revoked:\n%s", sql)
		}
	}
}

func TestMysqladminSyncGrants_RevokeFailureFails(t *testing.T) {
	execs := shadowMySQL(t, []string{`alice\_%`}, "REVOKE")
	if _, err := dbMysqladminSyncGrantsHandler(context.Background(),
		json.RawMessage(`{"panel_username":"alice","db_names":["alice_shop"]}`)); err == nil {
		t.Fatal("a failed revoke must fail the sync so the panel refuses the phpMyAdmin open")
	}
	for _, sql := range *execs {
		if strings.Contains(sql, "GRANT ALL") {
			t.Fatalf("nothing may be granted after a failed revoke:\n%s", sql)
		}
	}
}

func TestMysqladminSyncGrants_RejectsBadPanelUsername(t *testing.T) {
	_ = shadowMySQL(t, nil, "")
	if _, err := dbMysqladminSyncGrantsHandler(context.Background(),
		json.RawMessage(`{"panel_username":"Bad Name","db_names":["x"]}`)); err == nil {
		t.Fatal("expected invalid_argument")
	}
}

// A user rename no longer re-grants a <new>\_% wildcard; it clears the renamed
// shadow account's database grants (the next phpMyAdmin open re-grants the
// renamed databases by exact name).
func TestRenameUser_ShadowRoleGetsNoWildcard(t *testing.T) {
	execs := shadowMySQL(t, []string{`old\_%`, `old\_shop`}, "")
	if _, err := dbRenameUserHandler(context.Background(), json.RawMessage(
		`{"old_name":"old_mysqladmin","new_name":"new_mysqladmin","old_prefix":"old","new_prefix":"new"}`)); err != nil {
		t.Fatalf("rename: %v", err)
	}
	all := strings.Join(*execs, "\n")
	if strings.Contains(all, "GRANT ALL") || strings.Contains(all, "`new\\_%`") {
		t.Fatalf("rename must not grant a wildcard:\n%s", all)
	}
	for _, want := range []string{
		"REVOKE ALL PRIVILEGES ON `old\\_%`.* FROM 'new_mysqladmin'@'localhost';",
		"REVOKE ALL PRIVILEGES ON `old\\_shop`.* FROM 'new_mysqladmin'@'localhost';",
	} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %q in:\n%s", want, all)
		}
	}
}

func TestMariaDBGrantPattern_EscapesWildcards(t *testing.T) {
	for in, want := range map[string]string{
		"alice_shop": `alice\_shop`,
		"alice-blog": `alice-blog`,
		"a_b_c":      `a\_b\_c`,
		"weird%name": `weird\%name`,
		`back\slash`: `back\\slash`,
	} {
		if got := mariaDBGrantPattern(in); got != want {
			t.Fatalf("mariaDBGrantPattern(%q) = %q, want %q", in, got, want)
		}
	}
}
