package cpanel

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dbops"
)

// cpanelAllPrivs is how cPanel's pkgacct (and SHOW GRANTS on MySQL) writes
// ALL PRIVILEGES on one database.
const cpanelAllPrivs = "SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, REFERENCES, INDEX, ALTER, " +
	"CREATE TEMPORARY TABLES, LOCK TABLES, EXECUTE, CREATE VIEW, SHOW VIEW, CREATE ROUTINE, ALTER ROUTINE, EVENT, TRIGGER"

func TestNormalisePrivs_SpelledOutAllPrivileges(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     string
		wantAll bool
	}{
		{"cpanel full list", cpanelAllPrivs, true},
		{"mariadb full list", cpanelAllPrivs + ", DELETE HISTORY", true},
		{"mariadb 11 full list", cpanelAllPrivs + ", DELETE HISTORY, SHOW CREATE ROUTINE", true},
		{"other case and spacing", strings.ToLower(strings.ReplaceAll(cpanelAllPrivs, "LOCK TABLES", "LOCK  TABLES")), true},
		{"one missing", strings.Replace(cpanelAllPrivs, ", EVENT", "", 1), false},
		{"plus a global privilege", cpanelAllPrivs + ", FILE", false},
		{"short list", "SELECT, INSERT", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := normalisePrivs(tc.raw)
			if isAll := len(got) == 1 && got[0] == "ALL"; isAll != tc.wantAll {
				t.Fatalf("normalisePrivs(%q) = %v, want ALL=%v", tc.raw, got, tc.wantAll)
			}
			if !tc.wantAll && len(got) != len(strings.Split(tc.raw, ",")) {
				t.Errorf("a partial list must keep every token: got %v", got)
			}
		})
	}
}

// strictGrantAgent is engineAgent with db_user.grant's privilege rule: ALL or
// the few privileges dbops knows; any other token refuses the whole grant.
type strictGrantAgent struct{ engineAgent }

func (a *strictGrantAgent) Call(ctx context.Context, command string, params any) (json.RawMessage, error) {
	if command == "db_user.grant" {
		p, _ := params.(map[string]any)
		privs, _ := p["privileges"].([]string)
		if _, _, err := dbops.CanonicalDBGrant(privs, ""); err != nil {
			return nil, &agent.AgentError{Code: agent.CodeInvalidArgument, Message: "invalid privileges: " + err.Error()}
		}
	}
	return a.engineAgent.Call(ctx, command, params)
}

// importAliceWP imports one database, alice_wp, from an archive whose
// mysql.sql holds grants, with source credentials preserved.
func importAliceWP(t *testing.T, grants string) (*DBImportResult, *strictGrantAgent) {
	t.Helper()
	dir, dumps := writeDumps(t, "alice_wp.sql")
	if err := os.MkdirAll(filepath.Join(dir, "cpmove-alice"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cpmove-alice", "mysql.sql"), []byte(grants), 0o644); err != nil {
		t.Fatal(err)
	}
	ag := &strictGrantAgent{engineAgent{accounts: map[string]string{}}}
	res, err := ImportDatabases(context.Background(), gh723DBRepo{}, &ownerRows{}, &grantRows{}, ag,
		&ParsedTarball{ExtractDir: dir, SourceUser: "alice", MySQLDumps: dumps}, ownerAlice, "alice", true)
	if err != nil {
		t.Fatalf("ImportDatabases: %v", err)
	}
	return res, ag
}

func grantLines(user, db, privs string) string {
	return "GRANT USAGE ON *.* TO '" + user + "'@'localhost' IDENTIFIED BY PASSWORD '" + sourceHash + "';\n" +
		"GRANT " + privs + " ON `" + strings.ReplaceAll(db, "_", "\\_") + "`.* TO '" + user + "'@'localhost';\n"
}

const aliceWPConfig = "<?php\ndefine( 'DB_NAME', 'alice_wp' );\ndefine( 'DB_USER', '%s' );\n" +
	"define( 'DB_PASSWORD', 'source-pw' );\ndefine( 'DB_HOST', 'localhost' );\n"

// cPanel's spelled-out ALL PRIVILEGES was refused whole by db_user.grant, so a
// source user other than the database's own got no grant and its app could
// not reach the database.
func TestImportDatabases_CPanelFullPrivilegeListIsGranted(t *testing.T) {
	res, ag := importAliceWP(t, grantLines("alice_old", "alice_wp", cpanelAllPrivs))

	if !ag.granted("alice_old") {
		t.Fatalf("alice_old got no grant on alice_wp: %v", res.Skipped)
	}
	if !skippedHas(res, "compat_user alice_old: created with original password hash + 1 grant(s)") {
		t.Errorf("no report of the grant: %v", res.Skipped)
	}
	if skippedHas(res, "db_user.grant on") {
		t.Errorf("a grant was refused: %v", res.Skipped)
	}
	out, _ := rewriteWordPress(strings.Replace(aliceWPConfig, "%s", "alice_old", 1), res.Credentials)
	if !strings.Contains(out, "'source-pw'") || !strings.Contains(out, "'alice_old'") {
		t.Errorf("the app config must keep alice_old and its source password:\n%s", out)
	}
}

// The database's own user (the panel-managed one this import made, spelled as
// the source user) takes the source hash. When its source grant is refused,
// its grant from this import still opens the database: the app config must
// keep the source password, not the temp one the hash just replaced.
func TestImportDatabases_RefusedGrantKeepsSourcePasswordOfPanelUser(t *testing.T) {
	res, ag := importAliceWP(t, grantLines("alice_wp", "alice_wp", "SELECT, LOCK TABLES"))

	if ag.accounts["alice_wp"] != sourceHash {
		t.Fatalf("alice_wp password = %q, want the source hash", ag.accounts["alice_wp"])
	}
	if !skippedHas(res, "compat_user alice_wp: db_user.grant on alice_wp") {
		t.Fatalf("the strict agent should refuse SELECT, LOCK TABLES: %v", res.Skipped)
	}
	if !res.Credentials["alice_wp"].preservesUser("alice_wp") {
		t.Errorf("alice_wp not kept: %+v", res.Credentials["alice_wp"])
	}
	if !skippedHas(res, "compat_user alice_wp: keeps its grant on alice_wp from this import") {
		t.Errorf("no report line: %v", res.Skipped)
	}
	out, _ := rewriteWordPress(strings.Replace(aliceWPConfig, "%s", "alice_wp", 1), res.Credentials)
	if !strings.Contains(out, "'source-pw'") {
		t.Errorf("wp-config lost the source password (the temp one no longer opens alice_wp):\n%s", out)
	}
}

// A refused grant on a separate source user leaves it with no access: its app
// config moves to the database's own user and that user's temp password,
// which still works because nothing replaced it.
func TestImportDatabases_RefusedGrantOfOtherUserFallsBackToPanelUser(t *testing.T) {
	res, ag := importAliceWP(t, grantLines("alice_old", "alice_wp", "SELECT, LOCK TABLES"))

	cred := res.Credentials["alice_wp"]
	if cred.preservesUser("alice_old") {
		t.Fatalf("alice_old kept though it can open nothing: %+v", cred)
	}
	if cred.Password == "" || ag.accounts["alice_wp"] != cred.Password {
		t.Fatalf("alice_wp temp password %q, account %q: want the same", cred.Password, ag.accounts["alice_wp"])
	}
	out, _ := rewriteWordPress(strings.Replace(aliceWPConfig, "%s", "alice_old", 1), res.Credentials)
	if strings.Contains(out, "'source-pw'") || !strings.Contains(out, "'alice_wp'") {
		t.Errorf("wp-config must move to alice_wp and its temp password:\n%s", out)
	}
}
