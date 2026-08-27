package commands

// db.rename_user — RENAME USER for a MariaDB account (GH #1238 DB re-prefix on
// user rename). RENAME USER preserves the account's grants AND its password hash,
// so the panel's stored password stays valid.
//
// Optional wildcard re-grant (for the <prefix>_mysqladmin shadow role, whose
// GRANT is a <prefix>_%.* wildcard): after the rename, REVOKE the old prefix and
// GRANT the new one, so the role admins the tenant's moved (new-prefix) DBs and
// no longer any old-prefix name a future same-name user could create.
//
// Idempotent: if the old account is gone (already renamed / never existed) it is
// a no-op success; a re-grant still runs against the new name so a resumed rename
// converges.

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
	// WildcardRegrant, when both set, re-points a <old>_%.* → <new>_%.* GRANT
	// after the rename (the shadow-admin role).
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

	// Optional wildcard re-grant for the shadow-admin role.
	if p.OldPrefix != "" && p.NewPrefix != "" {
		if !dbPrefixRegex.MatchString(p.OldPrefix) || !dbPrefixRegex.MatchString(p.NewPrefix) {
			return nil, invalidArg("invalid prefix")
		}
		// Backtick pattern with an escaped underscore, matching db.mysqladmin.ensure.
		oldPat := fmt.Sprintf("`%s\\_%%`", p.OldPrefix)
		newPat := fmt.Sprintf("`%s\\_%%`", p.NewPrefix)
		// REVOKE may error if the old grant is absent (already re-pointed on a
		// resume) — tolerate that; the GRANT + FLUSH are what must stick.
		_ = execCommandContext(ctx, "mysql", "-e", fmt.Sprintf(
			"REVOKE ALL PRIVILEGES ON %s.* FROM %s@%s", oldPat, newLit, hostLit)).Run()
		if err := execCommandContext(ctx, "mysql", "-e", fmt.Sprintf(
			"GRANT ALL PRIVILEGES ON %s.* TO %s@%s; FLUSH PRIVILEGES;", newPat, newLit, hostLit)).Run(); err != nil {
			return nil, internalErr("failed to re-point shadow grant")
		}
	}

	return dbRenameUserResponse{OK: true, Renamed: renamed}, nil
}

func init() {
	Default.Register("db.rename_user", dbRenameUserHandler)
}
