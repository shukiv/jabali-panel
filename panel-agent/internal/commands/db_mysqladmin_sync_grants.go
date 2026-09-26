package commands

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// db.mysqladmin.sync_grants — the phpMyAdmin shadow user's database grants.
//
// The shadow user <panel_username>_mysqladmin used to be granted
// ALL ON `<panel_username>\_%`.* once, at provisioning. Panel usernames may
// contain '_', so that pattern also matched every database of a sibling tenant
// whose username starts with "<panel_username>_" — tenant "alice" could open
// tenant "alice_x"'s "alice_x_shop" in phpMyAdmin with ALL privileges.
//
// This verb makes the shadow's database grants exactly the tenant's own
// databases: db_names is the EXPLICIT list from the control-plane (databases
// WHERE user_id = ? AND engine = 'mariadb'), never a name pattern. It grants
// each listed database by exact name and revokes every other database-level
// grant the shadow holds, which also removes the old wildcard grant on
// existing boxes. The panel runs it on every phpMyAdmin open, so a database
// created later is picked up and a stale grant cannot survive an open.
//
// In a GRANT, the database name is a pattern: '_' matches any one character
// and '%' any run. Each name is therefore escaped ('_' → '\_', '%' → '\%')
// so "alice_my_shop" cannot also match "alicezmy_shop".

type dbMysqladminSyncGrantsParams struct {
	PanelUsername string   `json:"panel_username"`
	DBNames       []string `json:"db_names"`
}

type dbMysqladminSyncGrantsResponse struct {
	OK      bool `json:"ok"`
	Granted int  `json:"granted"`
	Revoked int  `json:"revoked"`
}

// mariaDBGrantPattern escapes the GRANT wildcards in a database name so the
// grant matches that one database only.
func mariaDBGrantPattern(db string) string {
	return strings.NewReplacer(`\`, `\\`, "_", `\_`, "%", `\%`).Replace(db)
}

// quoteMariaDBPatternIdent backtick-quotes a stored mysql.db Db value for a
// REVOKE. Inside backticks only a backtick is special; it is doubled. The
// value is reproduced byte for byte, so a stored wildcard pattern (alice\_%)
// is revoked exactly as it was granted.
func quoteMariaDBPatternIdent(v string) (string, error) {
	if v == "" || len(v) > 64 || strings.ContainsRune(v, 0) {
		return "", fmt.Errorf("unexpected stored database pattern")
	}
	return "`" + strings.ReplaceAll(v, "`", "``") + "`", nil
}

func dbMysqladminSyncGrantsHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbMysqladminSyncGrantsParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	if !panelUsernameRegex.MatchString(p.PanelUsername) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid panel username"}
	}
	shadowUser := p.PanelUsername + "_mysqladmin"
	userLit, err := EscapeMariaDBLiteral(shadowUser)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid shadow username"}
	}
	granted, revoked, aerr := syncMysqladminShadowGrants(ctx, userLit, "'localhost'", p.DBNames)
	if aerr != nil {
		return nil, aerr
	}
	return dbMysqladminSyncGrantsResponse{OK: true, Granted: granted, Revoked: revoked}, nil
}

// syncMysqladminShadowGrants makes the database-level grants of the account
// userLit@hostLit (both already-escaped SQL literals) exactly dbNames: it
// revokes every other mysql.db grant the account holds, then grants each valid
// listed name by exact, escaped pattern. A nil or empty dbNames revokes all of
// the account's database grants.
func syncMysqladminShadowGrants(ctx context.Context, userLit, hostLit string, dbNames []string) (int, int, *agentwire.AgentError) {
	// Desired set: the exact, escaped pattern of each valid listed name. An
	// invalid name is dropped (never granted) rather than failing the sync —
	// the revoke half must still run.
	desired := make(map[string]bool, len(dbNames))
	grants := make([]string, 0, len(dbNames))
	for _, db := range dbNames {
		if !dbNameRegex.MatchString(db) {
			continue
		}
		pat := mariaDBGrantPattern(db)
		if desired[pat] {
			continue
		}
		desired[pat] = true
		grants = append(grants, fmt.Sprintf("GRANT ALL PRIVILEGES ON `%s`.* TO %s@%s;", pat, userLit, hostLit))
	}

	// Current database-level grants of the shadow user. HEX() keeps the stored
	// value exact through the mysql client's batch output, which would
	// otherwise escape the backslash in a pattern like alice\_%.
	rows, err := mysqlQueryLines(ctx, fmt.Sprintf(
		"SELECT HEX(Db) FROM mysql.db WHERE User=%s AND Host=%s", userLit, hostLit))
	if err != nil {
		return 0, 0, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "failed to read phpMyAdmin shadow grants"}
	}
	revokes := make([]string, 0)
	for _, row := range rows {
		raw, err := hex.DecodeString(strings.TrimSpace(row))
		if err != nil {
			return 0, 0, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "failed to read phpMyAdmin shadow grants"}
		}
		stored := string(raw)
		if desired[stored] {
			continue
		}
		ident, err := quoteMariaDBPatternIdent(stored)
		if err != nil {
			return 0, 0, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "unexpected phpMyAdmin shadow grant; cannot revoke"}
		}
		revokes = append(revokes, fmt.Sprintf("REVOKE ALL PRIVILEGES ON %s.* FROM %s@%s;", ident, userLit, hostLit))
	}

	// Revoke before granting, and fail the call if either step fails: the
	// panel refuses the phpMyAdmin session rather than open it with a grant
	// that may still reach another tenant.
	if len(revokes) > 0 {
		if err := execCommandContext(ctx, "mysql", "-e", strings.Join(revokes, " ")).Run(); err != nil {
			return 0, 0, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "failed to revoke stale phpMyAdmin shadow grants"}
		}
	}
	if len(grants) > 0 {
		if err := execCommandContext(ctx, "mysql", "-e", strings.Join(grants, " ")).Run(); err != nil {
			return 0, 0, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "failed to grant phpMyAdmin shadow access"}
		}
	}
	if len(revokes) > 0 || len(grants) > 0 {
		if err := execCommandContext(ctx, "mysql", "-e", "FLUSH PRIVILEGES;").Run(); err != nil {
			return 0, 0, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "failed to flush privileges"}
		}
	}
	return len(grants), len(revokes), nil
}

func init() {
	Default.Register("db.mysqladmin.sync_grants", dbMysqladminSyncGrantsHandler)
}
