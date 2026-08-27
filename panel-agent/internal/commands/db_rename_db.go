package commands

// db.rename_db — rename a MariaDB database by moving its base tables into a new
// schema (GH #1238 DB re-prefix on user rename). MariaDB has no RENAME DATABASE,
// so this does CREATE DATABASE <new> + RENAME TABLE <old>.t TO <new>.t for every
// base table (a metadata move on the same filesystem: instant, no data copy) +
// DROP DATABASE <old>.
//
// v1 is base-tables-only: RENAME TABLE does not move triggers, and views/routines/
// events are schema-bound and would need fragile SQL-text recreation. If the
// source DB holds any of those, the rename is REFUSED with a clear message rather
// than risk losing an object on the DROP DATABASE. Idempotent: re-running after a
// partial move (old gone, new present) is a no-op success.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type dbRenameDBParams struct {
	OldDB string `json:"old_db"`
	NewDB string `json:"new_db"`
}

type dbRenameDBResponse struct {
	OK          bool `json:"ok"`
	MovedTables int  `json:"moved_tables"`
	// AlreadyRenamed reports the idempotent path (old gone, new present).
	AlreadyRenamed bool `json:"already_renamed,omitempty"`
}

func invalidArg(msg string) *agentwire.AgentError {
	return &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: msg}
}

func internalErr(msg string) *agentwire.AgentError {
	return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: msg}
}

// mysqlQueryLines runs a query and returns its rows as trimmed lines (mysql -N
// -B = no header, tab-separated). An empty result is a zero-length slice.
func mysqlQueryLines(ctx context.Context, sql string) ([]string, error) {
	out, err := execCommandContext(ctx, "mysql", "-N", "-B", "-e", sql).Output()
	if err != nil {
		return nil, err
	}
	s := strings.TrimRight(string(out), "\n")
	if s == "" {
		return nil, nil
	}
	return strings.Split(s, "\n"), nil
}

func dbRenameDBHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbRenameDBParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, invalidArg(fmt.Sprintf("failed to parse params: %v", err))
	}
	if !dbNameRegex.MatchString(p.OldDB) || !dbNameRegex.MatchString(p.NewDB) {
		return nil, invalidArg("invalid database name")
	}
	if p.OldDB == p.NewDB {
		return nil, invalidArg("old_db and new_db are the same")
	}

	oldIdent, err := EscapeMariaDBIdentifier(p.OldDB)
	if err != nil {
		return nil, invalidArg("invalid database name")
	}
	newIdent, err := EscapeMariaDBIdentifier(p.NewDB)
	if err != nil {
		return nil, invalidArg("invalid database name")
	}
	oldLit, err := EscapeMariaDBLiteral(p.OldDB)
	if err != nil {
		return nil, invalidArg("invalid database name")
	}

	// Existence: if old is gone but new is present, a prior run already moved it.
	exist, err := mysqlQueryLines(ctx, fmt.Sprintf(
		"SELECT schema_name FROM information_schema.schemata WHERE schema_name IN (%s, %s)",
		oldLit, mustLit(p.NewDB)))
	if err != nil {
		return nil, internalErr("failed to inspect schemas")
	}
	hasOld, hasNew := false, false
	for _, s := range exist {
		if s == p.OldDB {
			hasOld = true
		}
		if s == p.NewDB {
			hasNew = true
		}
	}
	if !hasOld {
		if hasNew {
			return dbRenameDBResponse{OK: true, AlreadyRenamed: true}, nil
		}
		return nil, invalidArg(fmt.Sprintf("database %q does not exist", p.OldDB))
	}
	if hasNew {
		return nil, invalidArg(fmt.Sprintf("destination database %q already exists", p.NewDB))
	}

	// Refuse anything RENAME TABLE can't safely carry (v1 base-tables-only).
	nonTable, err := mysqlQueryLines(ctx, fmt.Sprintf(
		"SELECT "+
			"(SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=%[1]s AND table_type<>'BASE TABLE') + "+
			"(SELECT COUNT(*) FROM information_schema.routines WHERE routine_schema=%[1]s) + "+
			"(SELECT COUNT(*) FROM information_schema.events WHERE event_schema=%[1]s) + "+
			"(SELECT COUNT(*) FROM information_schema.triggers WHERE trigger_schema=%[1]s)",
		oldLit))
	if err != nil {
		return nil, internalErr("failed to inspect database objects")
	}
	if len(nonTable) == 1 && strings.TrimSpace(nonTable[0]) != "0" {
		return nil, invalidArg(fmt.Sprintf(
			"database %q contains views, triggers, stored routines, or events, which this rename does not yet move — leave it on its current name",
			p.OldDB))
	}

	// Preserve the source charset/collation on the new schema.
	cc, err := mysqlQueryLines(ctx, fmt.Sprintf(
		"SELECT default_character_set_name, default_collation_name FROM information_schema.schemata WHERE schema_name=%s",
		oldLit))
	if err != nil || len(cc) != 1 {
		return nil, internalErr("failed to read source charset")
	}
	fields := strings.Split(cc[0], "\t")
	if len(fields) != 2 {
		return nil, internalErr("unexpected schema metadata")
	}
	charsetLit, err := EscapeMariaDBLiteral(fields[0])
	if err != nil {
		return nil, internalErr("invalid source charset")
	}
	collationLit, err := EscapeMariaDBLiteral(fields[1])
	if err != nil {
		return nil, internalErr("invalid source collation")
	}

	// Enumerate base tables.
	tables, err := mysqlQueryLines(ctx, fmt.Sprintf(
		"SELECT table_name FROM information_schema.tables WHERE table_schema=%s AND table_type='BASE TABLE'",
		oldLit))
	if err != nil {
		return nil, internalErr("failed to list tables")
	}

	// CREATE the destination schema.
	if err := execCommandContext(ctx, "mysql", "-e", fmt.Sprintf(
		"CREATE DATABASE %s CHARACTER SET %s COLLATE %s", newIdent, charsetLit, collationLit)).Run(); err != nil {
		return nil, internalErr("failed to create destination database")
	}

	// Move every base table in one atomic RENAME TABLE.
	if len(tables) > 0 {
		var moves []string
		for _, tbl := range tables {
			tIdent, terr := EscapeMariaDBIdentifier(tbl)
			if terr != nil {
				return nil, internalErr(fmt.Sprintf("invalid table name %q", tbl))
			}
			moves = append(moves, fmt.Sprintf("%s.%s TO %s.%s", oldIdent, tIdent, newIdent, tIdent))
		}
		if err := execCommandContext(ctx, "mysql", "-e",
			"RENAME TABLE "+strings.Join(moves, ", ")).Run(); err != nil {
			return nil, internalErr("failed to move tables")
		}
	}

	// Drop the now-empty source schema.
	if err := execCommandContext(ctx, "mysql", "-e",
		fmt.Sprintf("DROP DATABASE %s", oldIdent)).Run(); err != nil {
		return nil, internalErr("moved tables but failed to drop the old database")
	}

	return dbRenameDBResponse{OK: true, MovedTables: len(tables)}, nil
}

// mustLit escapes a MariaDB string literal, returning a safe empty-match literal
// on the (regex-precluded) error path.
func mustLit(s string) string {
	lit, err := EscapeMariaDBLiteral(s)
	if err != nil {
		return "''"
	}
	return lit
}

func init() {
	Default.Register("db.rename_db", dbRenameDBHandler)
}
