package commands

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// grantExecRecorder swaps the exec seam: every `mysql`/`mariadb -e <sql>` is
// recorded; a statement containing noSuchGrantOn fails with MariaDB error
// 1141, and one containing failOn fails with a generic error.
func grantExecRecorder(t *testing.T, noSuchGrantOn, failOn string) *[]string {
	t.Helper()
	var sqls []string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		sql := ""
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "-e" {
				sql = args[i+1]
			}
		}
		sqls = append(sqls, sql)
		switch {
		case noSuchGrantOn != "" && strings.Contains(sql, noSuchGrantOn):
			return exec.CommandContext(ctx, "sh", "-c", "echo \"ERROR 1141 (42000) at line 1: There is no such grant defined for user 'u' on host 'localhost'\" >&2; exit 1")
		case failOn != "" && strings.Contains(sql, failOn):
			return exec.CommandContext(ctx, "sh", "-c", "echo 'ERROR 1045: Access denied' >&2; exit 1")
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })
	return &sqls
}

// A database-level grant must name the database with `_` escaped. MariaDB
// reads an unescaped `_` as a wildcard, so a grant on ab_c_d also covered a
// sibling tenant's abxc_d (box-proven: read and write).
func TestDBUserGrant_EscapesTheWildcardInTheDatabaseName(t *testing.T) {
	sqls := grantExecRecorder(t, "", "")
	raw, _ := json.Marshal(dbUserGrantParams{DBName: "ab_c_d", DBUserName: "ab_app", Privileges: []string{"SELECT", "INSERT"}})
	if _, err := dbUserGrantHandler(context.Background(), raw); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if len(*sqls) != 1 || !strings.Contains((*sqls)[0], "ON `ab\\_c\\_d`.* TO 'ab_app'@'localhost'") {
		t.Fatalf("grant SQL = %v, want the escaped name ab\\_c\\_d", *sqls)
	}
}

// Revoke covers both forms: the escaped name db_user.grant writes now, and
// the plain name of a grant written before. "No such grant" on one form must
// not stop the other.
func TestDBUserRevoke_RevokesBothFormsOfTheName(t *testing.T) {
	for _, absent := range []string{"ON `ab\\_c\\_d`.*", "ON `ab_c_d`.*"} {
		sqls := grantExecRecorder(t, absent, "")
		raw, _ := json.Marshal(dbUserRevokeParams{DBName: "ab_c_d", DBUserName: "ab_app", Privileges: []string{"ALL"}})
		if _, err := dbUserRevokeHandler(context.Background(), raw); err != nil {
			t.Fatalf("absent %s: revoke: %v", absent, err)
		}
		var escaped, plain bool
		for _, sql := range *sqls {
			escaped = escaped || strings.Contains(sql, "REVOKE ALL PRIVILEGES ON `ab\\_c\\_d`.* FROM 'ab_app'@'localhost'")
			plain = plain || strings.Contains(sql, "REVOKE ALL PRIVILEGES ON `ab_c_d`.* FROM 'ab_app'@'localhost'")
		}
		if !escaped || !plain {
			t.Fatalf("absent %s: revokes = %v, want both forms", absent, *sqls)
		}
	}
}

// Any other revoke error is reported, not swallowed.
func TestDBUserRevoke_ReportsARealError(t *testing.T) {
	grantExecRecorder(t, "", "REVOKE")
	raw, _ := json.Marshal(dbUserRevokeParams{DBName: "ab_c_d", DBUserName: "ab_app", GrantLevel: "rw"})
	if _, err := dbUserRevokeHandler(context.Background(), raw); err == nil {
		t.Fatal("revoke error was swallowed")
	}
}

// The scoped restore account runs tenant dump content. Its grant must cover
// the one database, and the unescaped grant an earlier restore left on the
// long-lived account must be removed before the dump runs.
func TestProvisionScopedShadow_GrantsTheEscapedNameAndDropsTheOldGrant(t *testing.T) {
	sqls := grantExecRecorder(t, "", "")
	if err := provisionScopedShadow(context.Background(), "ab_c_d", "jb_s_ab_c_d", "00ff"); err != nil {
		t.Fatalf("provision: %v", err)
	}
	all := strings.Join(*sqls, "\n")
	if !strings.Contains(all, "GRANT ALL PRIVILEGES ON `ab\\_c\\_d`.* TO 'jb_s_ab_c_d'@'localhost'") {
		t.Fatalf("provision SQL = %s, want the escaped grant", all)
	}
	if !strings.Contains(all, "REVOKE ALL PRIVILEGES ON `ab_c_d`.* FROM 'jb_s_ab_c_d'@'localhost'") {
		t.Fatalf("provision SQL = %s, want the plain grant revoked", all)
	}
}

// No old grant to remove is the normal case after the first conversion.
func TestProvisionScopedShadow_NoOldGrantIsFine(t *testing.T) {
	grantExecRecorder(t, "REVOKE", "")
	if err := provisionScopedShadow(context.Background(), "ab_c_d", "jb_s_ab_c_d", "00ff"); err != nil {
		t.Fatalf("provision with no old grant: %v", err)
	}
}

// legacyGrantsFake scripts the legacy converter: the mysql.db rows (plain
// values; hex-encoded like the real query) and each grantee's SHOW GRANTS.
type legacyGrantsFake struct {
	rows       [][3]string       // user, host, db
	showGrants map[string]string // grantee -> SHOW GRANTS output
	failOn     string            // a statement containing this fails
	execd      []string
}

func legacyGrantsEnv(t *testing.T, f *legacyGrantsFake) {
	t.Helper()
	prevExec, prevMy := execCommandContext, mysqlExec
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		var lines []string
		for _, r := range f.rows {
			lines = append(lines, hexUpper(r[0])+"\t"+hexUpper(r[1])+"\t"+hexUpper(r[2]))
		}
		return exec.CommandContext(ctx, "printf", "%s", strings.Join(lines, "\n"))
	}
	mysqlExec = func(_ context.Context, sql string) (string, error) {
		f.execd = append(f.execd, sql)
		if strings.HasPrefix(sql, "SHOW GRANTS FOR ") {
			return f.showGrants[strings.TrimPrefix(sql, "SHOW GRANTS FOR ")], nil
		}
		if f.failOn != "" && strings.Contains(sql, f.failOn) {
			return "ERROR 1045: denied", exec.ErrNotFound
		}
		return "", nil
	}
	t.Cleanup(func() { execCommandContext, mysqlExec = prevExec, prevMy })
}

// An existing plain-name grant is replayed under the escaped name with the
// same privileges, and only then is the plain row revoked.
func TestEscapeLegacyGrants_ConvertsAPlainNameGrant(t *testing.T) {
	f := &legacyGrantsFake{
		rows: [][3]string{{"ab_app", "localhost", "ab_c_d"}},
		showGrants: map[string]string{"'ab_app'@'localhost'": "GRANT USAGE ON *.* TO `ab_app`@`localhost`\n" +
			"GRANT SELECT, INSERT ON `ab_c_d`.* TO `ab_app`@`localhost`"},
	}
	legacyGrantsEnv(t, f)
	out, err := dbUserEscapeLegacyGrantsHandler(context.Background(), nil)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if got := out.(dbUserEscapeLegacyGrantsResponse).Converted; len(got) != 1 {
		t.Fatalf("converted = %v, want one", got)
	}
	grantAt, revokeAt := -1, -1
	for i, sql := range f.execd {
		if sql == "GRANT SELECT, INSERT ON `ab\\_c\\_d`.* TO `ab_app`@`localhost`" {
			grantAt = i
		}
		if sql == "REVOKE ALL PRIVILEGES ON `ab_c_d`.* FROM 'ab_app'@'localhost'" {
			revokeAt = i
		}
	}
	if grantAt < 0 || revokeAt < 0 || grantAt > revokeAt {
		t.Fatalf("statements = %v, want the escaped GRANT before the plain REVOKE", f.execd)
	}
	if f.execd[len(f.execd)-1] != "FLUSH PRIVILEGES" {
		t.Fatalf("last statement = %q, want FLUSH PRIVILEGES", f.execd[len(f.execd)-1])
	}
}

// WITH GRANT OPTION is carried over, and the plain row's grant option is
// revoked too (ALL PRIVILEGES does not include it).
func TestEscapeLegacyGrants_CarriesTheGrantOption(t *testing.T) {
	f := &legacyGrantsFake{
		rows:       [][3]string{{"ab_app", "localhost", "ab_c_d"}},
		showGrants: map[string]string{"'ab_app'@'localhost'": "GRANT ALL PRIVILEGES ON `ab_c_d`.* TO `ab_app`@`localhost` WITH GRANT OPTION"},
	}
	legacyGrantsEnv(t, f)
	if _, err := dbUserEscapeLegacyGrantsHandler(context.Background(), nil); err != nil {
		t.Fatalf("convert: %v", err)
	}
	all := strings.Join(f.execd, "\n")
	if !strings.Contains(all, "GRANT ALL PRIVILEGES ON `ab\\_c\\_d`.* TO `ab_app`@`localhost` WITH GRANT OPTION") ||
		!strings.Contains(all, "REVOKE GRANT OPTION ON `ab_c_d`.* FROM 'ab_app'@'localhost'") {
		t.Fatalf("statements = %s", all)
	}
}

// Rows that are not a plain-name grant, or belong to a system account, are
// left alone.
func TestEscapeLegacyGrants_LeavesOtherRowsAlone(t *testing.T) {
	f := &legacyGrantsFake{rows: [][3]string{
		{"ab_app", "localhost", `ab\_c\_d`},     // already escaped
		{"ab_mysqladmin", "localhost", `ab\_%`}, // intentional pattern
		{"ab_app", "localhost", "abshop"},       // no wildcard
		{"root", "localhost", "some_db"},        // system account
		{"ab_mysqladmin", "localhost", "ab_x"},  // phpMyAdmin shadow
		{"a_role", "", "ab_c_d"},                // role: no host
	}}
	legacyGrantsEnv(t, f)
	out, err := dbUserEscapeLegacyGrantsHandler(context.Background(), nil)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if got := out.(dbUserEscapeLegacyGrantsResponse).Converted; len(got) != 0 {
		t.Fatalf("converted = %v, want none", got)
	}
	if len(f.execd) != 0 {
		t.Fatalf("statements = %v, want none", f.execd)
	}
}

// When the escaped grant cannot be written, the plain row stays (access is
// never lost) and the verb reports an error so the panel retries.
func TestEscapeLegacyGrants_FailedGrantKeepsThePlainRow(t *testing.T) {
	f := &legacyGrantsFake{
		rows:       [][3]string{{"ab_app", "localhost", "ab_c_d"}},
		showGrants: map[string]string{"'ab_app'@'localhost'": "GRANT SELECT ON `ab_c_d`.* TO `ab_app`@`localhost`"},
		failOn:     "GRANT SELECT ON `ab\\_c\\_d`",
	}
	legacyGrantsEnv(t, f)
	if _, err := dbUserEscapeLegacyGrantsHandler(context.Background(), nil); err == nil {
		t.Fatal("a failed conversion must be reported")
	}
	for _, sql := range f.execd {
		if strings.HasPrefix(sql, "REVOKE ") {
			t.Fatalf("plain row revoked although the escaped grant failed: %v", f.execd)
		}
	}
}
