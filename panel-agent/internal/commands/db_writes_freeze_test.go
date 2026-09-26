package commands

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// dbwEnv points the snapshot dir at a TempDir and stubs mysqlExec with a
// scriptable fake (GH #994: no test touches the real database).
type fakeMySQL struct {
	grantees        []string          // hold tenant_db under its plain name
	escapedGrantees []string          // hold it under the escaped name tenant\_db
	showGrants      map[string]string // grantee -> SHOW GRANTS output
	execd           []string          // every SQL statement run
}

func hexUpper(s string) string { return strings.ToUpper(hex.EncodeToString([]byte(s))) }

func dbwEnv(t *testing.T, fake *fakeMySQL) string {
	t.Helper()
	dir := t.TempDir()
	origDir, origExec := dbFreezeSnapshotDir, mysqlExec
	dbFreezeSnapshotDir = dir
	mysqlExec = func(_ context.Context, sql string) (string, error) {
		fake.execd = append(fake.execd, sql)
		switch {
		case strings.Contains(sql, "SCHEMA_PRIVILEGES"):
			var rows []string
			for _, g := range fake.grantees {
				rows = append(rows, g+"\t"+hexUpper("tenant_db"))
			}
			for _, g := range fake.escapedGrantees {
				rows = append(rows, g+"\t"+hexUpper(`tenant\_db`))
			}
			return strings.Join(rows, "\n"), nil
		case strings.HasPrefix(sql, "SHOW GRANTS FOR "):
			g := strings.TrimPrefix(sql, "SHOW GRANTS FOR ")
			return fake.showGrants[g], nil
		default:
			return "", nil
		}
	}
	t.Cleanup(func() { dbFreezeSnapshotDir, mysqlExec = origDir, origExec })
	return dir
}

func callWritesSet(t *testing.T, dbName string, freeze bool) dbWritesSetResponse {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"db_name": dbName, "freeze": freeze})
	got, err := dbWritesSetHandler(context.Background(), raw)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	return got.(dbWritesSetResponse)
}

func TestDBWrites_FreezeSnapshotsAndRevokes(t *testing.T) {
	fake := &fakeMySQL{
		grantees: []string{"'alice'@'localhost'", "'jq_mysqladmin'@'localhost'", "'root'@'localhost'"},
		showGrants: map[string]string{
			"'alice'@'localhost'": "GRANT USAGE ON *.* TO `alice`@`localhost`\nGRANT SELECT, INSERT, UPDATE ON `tenant_db`.* TO `alice`@`localhost`",
		},
	}
	dir := dbwEnv(t, fake)

	resp := callWritesSet(t, "tenant_db", true)
	if !resp.Frozen || !resp.Changed {
		t.Fatalf("freeze resp = %+v", resp)
	}
	// Snapshot written for alice only (system accounts excluded).
	body, err := os.ReadFile(filepath.Join(dir, "tenant_db.json"))
	if err != nil {
		t.Fatalf("snapshot missing: %v", err)
	}
	var snap dbFreezeSnapshot
	_ = json.Unmarshal(body, &snap)
	if _, ok := snap.Grants["'alice'@'localhost'"]; !ok {
		t.Fatalf("alice not snapshotted: %+v", snap.Grants)
	}
	if _, ok := snap.Grants["'jq_mysqladmin'@'localhost'"]; ok {
		t.Fatal("mysqladmin shadow was snapshotted — must be excluded")
	}
	// A REVOKE ran for alice, NOT for root or the shadow admin.
	var revokedAlice, revokedSystem bool
	for _, sql := range fake.execd {
		if strings.HasPrefix(sql, "REVOKE ") && strings.Contains(sql, "'alice'@'localhost'") {
			revokedAlice = true
			for _, keep := range []string{"SELECT", "DELETE", "DROP"} {
				if strings.Contains(sql, keep) {
					t.Fatalf("freeze revoked %s — must be preserved: %s", keep, sql)
				}
			}
		}
		if strings.HasPrefix(sql, "REVOKE ") && (strings.Contains(sql, "root") || strings.Contains(sql, "mysqladmin")) {
			revokedSystem = true
		}
	}
	if !revokedAlice {
		t.Fatal("alice writes not revoked")
	}
	if revokedSystem {
		t.Fatal("froze a system/shadow account")
	}
}

func TestDBWrites_SecondFreezeDoesNotResnapshot(t *testing.T) {
	fake := &fakeMySQL{
		grantees:   []string{"'alice'@'localhost'"},
		showGrants: map[string]string{"'alice'@'localhost'": "GRANT SELECT, INSERT ON `tenant_db`.* TO `alice`@`localhost`"},
	}
	dir := dbwEnv(t, fake)
	callWritesSet(t, "tenant_db", true)
	before, _ := os.ReadFile(filepath.Join(dir, "tenant_db.json"))

	// Simulate the frozen grants now being what SHOW GRANTS returns.
	fake.showGrants["'alice'@'localhost'"] = "GRANT SELECT ON `tenant_db`.* TO `alice`@`localhost`"
	resp := callWritesSet(t, "tenant_db", true)
	if resp.Changed {
		t.Fatal("second freeze reported changed=true — must re-assert only")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "tenant_db.json"))
	if string(before) != string(after) {
		t.Fatal("snapshot was overwritten with the frozen grants — restore would be bricked")
	}
}

func TestDBWrites_RestoreReplaysVerbatimAndClears(t *testing.T) {
	fake := &fakeMySQL{
		grantees:   []string{"'alice'@'localhost'"},
		showGrants: map[string]string{"'alice'@'localhost'": "GRANT SELECT, INSERT, UPDATE ON `tenant_db`.* TO `alice`@`localhost`"},
	}
	dir := dbwEnv(t, fake)
	callWritesSet(t, "tenant_db", true)
	fake.execd = nil

	resp := callWritesSet(t, "tenant_db", false)
	if resp.Frozen || !resp.Changed {
		t.Fatalf("restore resp = %+v", resp)
	}
	// The verbatim pre-freeze GRANT was replayed.
	var replayed bool
	for _, sql := range fake.execd {
		if sql == "GRANT SELECT, INSERT, UPDATE ON `tenant_db`.* TO `alice`@`localhost`" {
			replayed = true
		}
	}
	if !replayed {
		t.Fatalf("pre-freeze grant not replayed verbatim: %v", fake.execd)
	}
	if _, err := os.Stat(filepath.Join(dir, "tenant_db.json")); !os.IsNotExist(err) {
		t.Fatal("snapshot not cleared after restore")
	}
}

func TestDBWrites_RestoreWithoutSnapshotIsNoop(t *testing.T) {
	fake := &fakeMySQL{}
	dbwEnv(t, fake)
	resp := callWritesSet(t, "never_frozen", false)
	if resp.Changed {
		t.Fatalf("restore of never-frozen db reported changed: %+v", resp)
	}
	for _, sql := range fake.execd {
		if strings.HasPrefix(sql, "GRANT ") {
			t.Fatalf("no-op restore ran a GRANT: %s", sql)
		}
	}
}

// No-promotion by construction: a read-only grantee's snapshot is its
// read-only statement, so restore never adds write privileges.
func TestDBWrites_ReadOnlyGranteeStaysReadOnly(t *testing.T) {
	fake := &fakeMySQL{
		grantees:   []string{"'ro'@'localhost'"},
		showGrants: map[string]string{"'ro'@'localhost'": "GRANT SELECT ON `tenant_db`.* TO `ro`@`localhost`"},
	}
	dbwEnv(t, fake)
	callWritesSet(t, "tenant_db", true)
	fake.execd = nil
	callWritesSet(t, "tenant_db", false)
	for _, sql := range fake.execd {
		if strings.HasPrefix(sql, "GRANT ") && (strings.Contains(sql, "INSERT") || strings.Contains(sql, "UPDATE")) {
			t.Fatalf("read-only grantee was promoted on restore: %s", sql)
		}
	}
}

// A grant written by db_user.grant is held under the escaped name
// (tenant\_db). The freeze must find it, snapshot its exact line and revoke
// on that form; a lookup on the plain name alone froze nothing for it.
func TestDBWrites_FreezeCoversAnEscapedGrant(t *testing.T) {
	escapedLine := "GRANT SELECT, INSERT, UPDATE ON `tenant\\_db`.* TO `alice`@`localhost`"
	fake := &fakeMySQL{
		escapedGrantees: []string{"'alice'@'localhost'"},
		showGrants:      map[string]string{"'alice'@'localhost'": "GRANT USAGE ON *.* TO `alice`@`localhost`\n" + escapedLine},
	}
	dir := dbwEnv(t, fake)
	callWritesSet(t, "tenant_db", true)

	var query string
	for _, sql := range fake.execd {
		if strings.Contains(sql, "SCHEMA_PRIVILEGES") {
			query = sql
		}
	}
	if !strings.Contains(query, "'tenant_db'") || !strings.Contains(query, `'tenant\\_db'`) {
		t.Fatalf("grantee query must look up both names, got: %s", query)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "tenant_db.json"))
	var snap dbFreezeSnapshot
	_ = json.Unmarshal(body, &snap)
	if len(snap.Grants) != 1 {
		t.Fatalf("snapshot = %v, want the escaped grant", snap.Grants)
	}
	for _, stmt := range snap.Grants {
		if stmt != escapedLine {
			t.Fatalf("snapshot stmt = %q, want %q", stmt, escapedLine)
		}
	}
	var revoked bool
	for _, sql := range fake.execd {
		if strings.HasPrefix(sql, "REVOKE ") && strings.Contains(sql, "ON `tenant\\_db`.* FROM 'alice'@'localhost'") {
			revoked = true
		}
	}
	if !revoked {
		t.Fatalf("escaped grant not revoked: %v", fake.execd)
	}
}

// A grantee that holds both forms (an old grant plus a new one) has each
// revoked on its own name and both lines kept for restore.
func TestDBWrites_FreezeCoversBothFormsOfOneGrantee(t *testing.T) {
	fake := &fakeMySQL{
		grantees:        []string{"'alice'@'localhost'"},
		escapedGrantees: []string{"'alice'@'localhost'"},
		showGrants: map[string]string{"'alice'@'localhost'": "GRANT SELECT ON `tenant_db`.* TO `alice`@`localhost`\n" +
			"GRANT INSERT ON `tenant\\_db`.* TO `alice`@`localhost`"},
	}
	dir := dbwEnv(t, fake)
	callWritesSet(t, "tenant_db", true)
	body, _ := os.ReadFile(filepath.Join(dir, "tenant_db.json"))
	var snap dbFreezeSnapshot
	_ = json.Unmarshal(body, &snap)
	if len(snap.Grants) != 2 {
		t.Fatalf("snapshot = %v, want both lines", snap.Grants)
	}
	var plain, escaped int
	for _, sql := range fake.execd {
		if !strings.HasPrefix(sql, "REVOKE ") {
			continue
		}
		if strings.Contains(sql, "ON `tenant_db`.*") {
			plain++
		}
		if strings.Contains(sql, "ON `tenant\\_db`.*") {
			escaped++
		}
	}
	if plain != 1 || escaped != 1 {
		t.Fatalf("revokes plain=%d escaped=%d, want one each: %v", plain, escaped, fake.execd)
	}
}

// The mysql client doubles every backslash in its default output. The freeze
// replays SHOW GRANTS lines verbatim, so it must read them raw (-r): a doubled
// `tenant\\_db` would be replayed as a different pattern.
func TestDBWrites_MysqlExecReadsRawOutput(t *testing.T) {
	var args []string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, a ...string) *exec.Cmd {
		args = append([]string{name}, a...)
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })
	if _, err := mysqlExec(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("mysqlExec: %v", err)
	}
	if !slices.Contains(args, "-r") {
		t.Fatalf("mysql args = %v, want -r (raw output)", args)
	}
}
