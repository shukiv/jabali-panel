package commands

// db.rename_user — RENAME USER for a MariaDB account (GH #1238 DB re-prefix on
// user rename). RENAME USER preserves the account's grants AND its password hash,
// so the panel's stored password stays valid.
//
// Shadow-admin role (old_prefix/new_prefix set, for <prefix>_mysqladmin): after
// the rename, every database-level grant of the renamed account is revoked. Its
// grants name the tenant's databases by their OLD names (or, on a box that
// predates db.mysqladmin.sync_grants, the old <prefix>\_% wildcard), and a
// future user could create a database under an old name. The panel re-grants
// the renamed databases by exact name on the tenant's next phpMyAdmin open
// (db.mysqladmin.sync_grants). No wildcard is granted: a <prefix>\_% pattern
// also matches a sibling tenant whose username starts with "<prefix>_".
//
// Idempotent: if the old account is gone (already renamed / never existed) it is
// a no-op success; the grant clear-out still runs against the new name so a
// resumed rename converges.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
)

// MariaDB account + prefix names: lower/upper alnum, underscore, hyphen; the
// mysqladmin/per-DB users all fit this. 80 is MariaDB's user-name ceiling.
var dbAccountRegex = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_-]{0,79}$`)
var dbPrefixRegex = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

type dbRenameUserParams struct {
	OldName string `json:"old_name"`
	NewName string `json:"new_name"`
	Host    string `json:"host"` // default 'localhost'
	// When both are set the account is the shadow-admin role: its database
	// grants are revoked after the rename (see the file comment).
	OldPrefix string `json:"old_prefix,omitempty"`
	NewPrefix string `json:"new_prefix,omitempty"`
}

type dbRenameUserResponse struct {
	OK      bool `json:"ok"`
	Renamed bool `json:"renamed"`
}

func dbRenameUserHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbRenameUserParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, invalidArg(fmt.Sprintf("failed to parse params: %v", err))
	}
	if !dbAccountRegex.MatchString(p.OldName) || !dbAccountRegex.MatchString(p.NewName) {
		return nil, invalidArg("invalid account name")
	}
	host := p.Host
	if host == "" {
		host = "localhost"
	}
	if !dbAccountRegex.MatchString(host) && host != "localhost" && host != "%" {
		return nil, invalidArg("invalid host")
	}
	hostLit, err := EscapeMariaDBLiteral(host)
	if err != nil {
		return nil, invalidArg("invalid host")
	}
	oldLit, err := EscapeMariaDBLiteral(p.OldName)
	if err != nil {
		return nil, invalidArg("invalid account name")
	}
	newLit, err := EscapeMariaDBLiteral(p.NewName)
	if err != nil {
		return nil, invalidArg("invalid account name")
	}

	renamed := false
	if p.OldName != p.NewName {
		// Only RENAME if the old account still exists (idempotent resume).
		exists, qerr := mysqlQueryLines(ctx, fmt.Sprintf(
			"SELECT 1 FROM mysql.user WHERE User=%s AND Host=%s", oldLit, hostLit))
		if qerr != nil {
			return nil, internalErr("failed to inspect account")
		}
		if len(exists) > 0 {
			if err := execCommandContext(ctx, "mysql", "-e", fmt.Sprintf(
				"RENAME USER %s@%s TO %s@%s", oldLit, hostLit, newLit, hostLit)).Run(); err != nil {
				return nil, internalErr("failed to rename account")
			}
			renamed = true
		}
	}

	// Shadow-admin role: drop every database grant the renamed account holds.
	if p.OldPrefix != "" && p.NewPrefix != "" {
		if !dbPrefixRegex.MatchString(p.OldPrefix) || !dbPrefixRegex.MatchString(p.NewPrefix) {
			return nil, invalidArg("invalid prefix")
		}
		if _, _, aerr := syncMysqladminShadowGrants(ctx, newLit, hostLit, nil); aerr != nil {
			return nil, internalErr("failed to clear shadow grants")
		}
	}

	return dbRenameUserResponse{OK: true, Renamed: renamed}, nil
}

func init() {
	Default.Register("db.rename_user", dbRenameUserHandler)
}
