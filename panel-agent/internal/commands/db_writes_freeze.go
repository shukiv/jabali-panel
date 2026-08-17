package commands

// db.writes.set — JAB-243 DB-quota write freeze / restore.
//
// When a tenant's summed database footprint reaches the package disk
// quota (the panel reconciler decides this), the tenant's writes to that
// database are frozen: INSERT/UPDATE/CREATE/ALTER/INDEX are revoked from
// every grantee on the schema, while SELECT/DELETE/DROP survive so the
// tenant can read and free space. Dropping back under the low-water mark
// restores the EXACT pre-freeze grants.
//
// The mechanism is snapshot-and-replay, NOT privilege diffing — the same
// filesystem-state pattern as the malware breaker (JAB-248):
//
//   - freeze reads each grantee's live GRANT statement for the schema,
//     saves them verbatim to /var/lib/jabali/db-quota-freeze/<db>.json
//     (root 0600), then revokes the write set. Verbatim replay is what
//     makes "no privilege promotion" true by construction — a read-only
//     grantee is restored to read-only because its saved statement is
//     read-only.
//   - restore replays the saved GRANT statements and deletes the
//     snapshot. No snapshot = no-op success (restart mid-window, or a
//     DB frozen by an older path).
//
// Enumerating grantees from information_schema.SCHEMA_PRIVILEGES covers
// EVERY db user with access — CLI-, migration-, and app-install-granted
// users included — which a panel grant-table scan would miss. Per-user
// mysqladmin shadow accounts and root/system users are never frozen.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// dbFreezeSnapshotDir is a var (not const) only so tests can redirect it
// to a TempDir; production never reassigns it.
var dbFreezeSnapshotDir = "/var/lib/jabali/db-quota-freeze"

// dbWritesRevokeSet is the privilege set frozen at quota. SELECT, DELETE
// and DROP deliberately survive so the tenant can read + free space.
var dbWritesRevokeSet = []string{"INSERT", "UPDATE", "CREATE", "ALTER", "INDEX"}

type dbWritesSetParams struct {
	DBName string `json:"db_name"`
	Freeze bool   `json:"freeze"`
}

type dbWritesSetResponse struct {
	DBName  string `json:"db_name"`
	Frozen  bool   `json:"frozen"`
	Changed bool   `json:"changed"`
}

type dbFreezeSnapshot struct {
	DBName string            `json:"db_name"`
	Grants map[string]string `json:"grants"` // grantee -> verbatim GRANT stmt for this schema
}

// mysqlExec runs mysql with the given SQL; var so tests stub it (GH #994
// — no test touches the real database).
var mysqlExec = func(ctx context.Context, sql string) (string, error) {
	out, err := execCommandContext(ctx, "mysql", "-N", "-e", sql).CombinedOutput()
	return string(out), err
}

func dbFreezeSnapshotPath(dbName string) string {
	return filepath.Join(dbFreezeSnapshotDir, dbName+".json")
}

// isSystemGrantee excludes accounts the freeze must never touch: root,
// the mysql.* internal roles, and the per-user _mysqladmin shadow the
// panel uses to manage tenant databases.
func isSystemGrantee(grantee string) bool {
	// grantee is 'user'@'host'.
	user := grantee
	if i := strings.Index(grantee, "@"); i > 0 {
		user = grantee[:i]
	}
	user = strings.Trim(user, "'`\"")
	if user == "root" || user == "mysql" || strings.HasPrefix(user, "mysql.") {
		return true
	}
	if strings.HasSuffix(user, "_mysqladmin") || strings.HasSuffix(user, "_shadowadmin") {
		return true
	}
	return false
}

func dbWritesSetHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p dbWritesSetParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, mwInvalidArgDBW("malformed JSON: " + err.Error())
	}
	if !dbBackupNameRegex.MatchString(p.DBName) {
		return nil, mwInvalidArgDBW("invalid database name")
	}
	snapPath := dbFreezeSnapshotPath(p.DBName)

	if p.Freeze {
		return dbWritesFreeze(ctx, p.DBName, snapPath)
	}
	return dbWritesRestore(ctx, p.DBName, snapPath)
}

func dbWritesFreeze(ctx context.Context, dbName, snapPath string) (any, error) {
	alreadyFrozen := false
	if _, err := os.Stat(snapPath); err == nil {
		// Snapshot exists: re-assert the revokes ONLY. Re-snapshotting
		// now would capture the already-frozen grants and make a later
		// restore replay the frozen state — the bug that bricks restore.
		alreadyFrozen = true
	}

	grantees, err := dbSchemaGrantees(ctx, dbName)
	if err != nil {
		return nil, err
	}

	if !alreadyFrozen {
		snap := dbFreezeSnapshot{DBName: dbName, Grants: map[string]string{}}
		for _, g := range grantees {
			stmt, gerr := dbSchemaGrantStmt(ctx, g, dbName)
			if gerr != nil || stmt == "" {
				continue
			}
			snap.Grants[g] = stmt
		}
		if err := os.MkdirAll(dbFreezeSnapshotDir, 0o700); err != nil {
			return nil, dbwInternal("mkdir snapshot dir: " + err.Error())
		}
		body, _ := json.Marshal(snap)
		if err := os.WriteFile(snapPath, body, 0o600); err != nil {
			return nil, dbwInternal("write snapshot: " + err.Error())
		}
	}

	revokeCols := strings.Join(dbWritesRevokeSet, ", ")
	for _, g := range grantees {
		// REVOKE of a not-held privilege is harmless in MariaDB, so this
		// is safe to run on every grantee including read-only ones.
		sql := fmt.Sprintf("REVOKE %s ON `%s`.* FROM %s", revokeCols, dbName, g)
		if out, rerr := mysqlExec(ctx, sql); rerr != nil {
			return nil, dbwInternal(fmt.Sprintf("revoke on %s: %v: %s", g, rerr, strings.TrimSpace(out)))
		}
	}
	if _, err := mysqlExec(ctx, "FLUSH PRIVILEGES"); err != nil {
		return nil, dbwInternal("flush privileges: " + err.Error())
	}
	return dbWritesSetResponse{DBName: dbName, Frozen: true, Changed: !alreadyFrozen}, nil
}

func dbWritesRestore(ctx context.Context, dbName, snapPath string) (any, error) {
	body, err := os.ReadFile(snapPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Never frozen (or already restored) — success no-op.
			return dbWritesSetResponse{DBName: dbName, Frozen: false, Changed: false}, nil
		}
		return nil, dbwInternal("read snapshot: " + err.Error())
	}
	var snap dbFreezeSnapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		return nil, dbwInternal("decode snapshot: " + err.Error())
	}
	for _, stmt := range snap.Grants {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if out, gerr := mysqlExec(ctx, stmt); gerr != nil {
			return nil, dbwInternal(fmt.Sprintf("replay grant: %v: %s", gerr, strings.TrimSpace(out)))
		}
	}
	if _, err := mysqlExec(ctx, "FLUSH PRIVILEGES"); err != nil {
		return nil, dbwInternal("flush privileges: " + err.Error())
	}
	if err := os.Remove(snapPath); err != nil && !os.IsNotExist(err) {
		return nil, dbwInternal("remove snapshot: " + err.Error())
	}
	return dbWritesSetResponse{DBName: dbName, Frozen: false, Changed: true}, nil
}

// dbSchemaGrantees lists non-system grantees with any privilege on the
// schema.
func dbSchemaGrantees(ctx context.Context, dbName string) ([]string, error) {
	out, err := mysqlExec(ctx, fmt.Sprintf(
		"SELECT DISTINCT grantee FROM information_schema.SCHEMA_PRIVILEGES WHERE table_schema = '%s'",
		dbName))
	if err != nil {
		return nil, dbwUnavailable("schema_privileges query: " + err.Error())
	}
	var out2 []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		g := strings.TrimSpace(line)
		if g == "" || isSystemGrantee(g) {
			continue
		}
		out2 = append(out2, g)
	}
	return out2, nil
}

// dbSchemaGrantStmt returns the grantee's GRANT statement scoped to the
// schema, verbatim from SHOW GRANTS (the only replay-safe source).
func dbSchemaGrantStmt(ctx context.Context, grantee, dbName string) (string, error) {
	out, err := mysqlExec(ctx, "SHOW GRANTS FOR "+grantee)
	if err != nil {
		return "", nil // grantee may have been dropped mid-sweep; skip
	}
	needle := "`" + dbName + "`.*"
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, needle) && strings.HasPrefix(line, "GRANT ") {
			return strings.TrimRight(line, ";"), nil
		}
	}
	return "", nil
}

func mwInvalidArgDBW(msg string) *agentwire.AgentError {
	return &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: msg}
}
func dbwInternal(msg string) *agentwire.AgentError {
	return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: msg}
}
func dbwUnavailable(msg string) *agentwire.AgentError {
	return &agentwire.AgentError{Code: agentwire.CodeUnavailable, Message: msg}
}

func init() {
	Default.Register("db.writes.set", dbWritesSetHandler)
}
