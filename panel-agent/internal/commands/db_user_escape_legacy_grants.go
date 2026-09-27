package commands

// db_user.escape_legacy_grants — rewrites database-level grants that name a
// database with an unescaped `_`.
//
// In `GRANT ... ON <db>.*` MariaDB reads `_` as a one-character wildcard, so
// a grant on alice_shop also covers aliceXshop. Tenant database names are
// <owner>_<name>, which lets a tenant pick a name whose grant covers a
// sibling tenant's database (tenant "ab" granting on ab_c_d reaches tenant
// "abxc"'s abxc_d). db_user.grant now writes the escaped name; this verb
// converts the grants written before it, on every box.
//
// Each conversion replays the grantee's own GRANT line for that database with
// the name escaped, then revokes the unescaped row. The privileges are copied
// verbatim, never recomputed, and the old row goes only after the new one is
// in place. A grant whose name also carries `%` (an intentional pattern) and
// the system accounts (root, mysql.*, the phpMyAdmin shadows) are left alone.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// legacyGrantDBRegex is a plain database name: no escape and no `%`. Only
// such a name, when it holds a `_`, is a legacy grant to convert.
var legacyGrantDBRegex = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)

type dbUserEscapeLegacyGrantsResponse struct {
	Converted []string          `json:"converted"`
	Failed    map[string]string `json:"failed,omitempty"`
}

func dbUserEscapeLegacyGrantsHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	rows, err := mysqlQueryLines(ctx, "SELECT HEX(User), HEX(Host), HEX(Db) FROM mysql.db")
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeUnavailable, Message: "failed to read database grants"}
	}
	resp := dbUserEscapeLegacyGrantsResponse{Converted: []string{}}
	fail := func(key, msg string) {
		if resp.Failed == nil {
			resp.Failed = map[string]string{}
		}
		resp.Failed[key] = msg
		slog.WarnContext(ctx, "escape legacy grant failed", "grant", key, "error", msg)
	}
	for _, row := range rows {
		cols := strings.Split(strings.TrimSpace(row), "\t")
		if len(cols) != 3 {
			continue
		}
		user, uerr := hex.DecodeString(cols[0])
		host, herr := hex.DecodeString(cols[1])
		db, derr := hex.DecodeString(cols[2])
		if uerr != nil || herr != nil || derr != nil {
			continue
		}
		name := string(db)
		// A role has an empty host and no user@host form; the panel
		// creates none.
		if len(host) == 0 || !strings.Contains(name, "_") || !legacyGrantDBRegex.MatchString(name) {
			continue
		}
		userLit, err := EscapeMariaDBLiteral(string(user))
		if err != nil {
			continue
		}
		hostLit, err := EscapeMariaDBLiteral(string(host))
		if err != nil {
			continue
		}
		grantee := userLit + "@" + hostLit
		if isSystemGrantee(grantee) {
			continue
		}
		key := grantee + " on " + name
		if err := escapeLegacyGrant(ctx, grantee, name); err != nil {
			fail(key, err.Error())
			continue
		}
		resp.Converted = append(resp.Converted, key)
	}
	if len(resp.Converted) > 0 {
		if out, err := mysqlExec(ctx, "FLUSH PRIVILEGES"); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("flush privileges: %v: %s", err, strings.TrimSpace(out))}
		}
		slog.InfoContext(ctx, "escaped legacy database grants", "converted", resp.Converted)
	}
	if len(resp.Failed) > 0 {
		// Reported as an error so the panel retries; the conversions that
		// did succeed stay done.
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("%d of %d legacy database grants not converted", len(resp.Failed), len(resp.Failed)+len(resp.Converted)),
		}
	}
	return resp, nil
}

// escapeLegacyGrant moves grantee's grant on the plain name db to the escaped
// name: replay the grantee's GRANT line with the name escaped, then revoke the
// plain row (and its GRANT OPTION, which ALL PRIVILEGES does not cover).
func escapeLegacyGrant(ctx context.Context, grantee, db string) error {
	stmt, _ := dbSchemaGrantStmt(ctx, grantee, db)
	if stmt == "" {
		return fmt.Errorf("no GRANT line for the database in SHOW GRANTS")
	}
	plain := " ON `" + db + "`.* "
	replay := strings.Replace(stmt, plain, " ON `"+mariaDBGrantPattern(db)+"`.* ", 1)
	if out, err := mysqlExec(ctx, replay); err != nil {
		return fmt.Errorf("grant escaped name: %v: %s", err, strings.TrimSpace(out))
	}
	revokes := []string{fmt.Sprintf("REVOKE ALL PRIVILEGES ON `%s`.* FROM %s", db, grantee)}
	if strings.Contains(stmt, "WITH GRANT OPTION") {
		revokes = append(revokes, fmt.Sprintf("REVOKE GRANT OPTION ON `%s`.* FROM %s", db, grantee))
	}
	for _, sql := range revokes {
		if out, err := mysqlExec(ctx, sql); err != nil && !isNoSuchGrant(out) {
			return fmt.Errorf("revoke plain name: %v: %s", err, strings.TrimSpace(out))
		}
	}
	return nil
}

func init() {
	Default.Register("db_user.escape_legacy_grants", dbUserEscapeLegacyGrantsHandler)
}
