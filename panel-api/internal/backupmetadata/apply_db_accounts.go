package backupmetadata

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1993: a restore rebuilt the database-user and grant rows but never the
// MariaDB accounts behind them, so after restoring an account on another
// server every site's database login failed. createRestoredDBAccounts gives
// the rows a restore created their MariaDB side.

// restoredDBAccount is a database user row a restore created, with the
// MariaDB password hash the backup carried for it ("" when it carried none).
type restoredDBAccount struct {
	row        *models.DatabaseUser
	nativeHash string
}

// nativePasswordHashRe is a mysql_native_password hash, the only form
// db_user.create takes.
var nativePasswordHashRe = regexp.MustCompile(`^\*[0-9A-Fa-f]{40}$`)

// restoredDBAccountTimeout bounds each agent call.
const restoredDBAccountTimeout = 30 * time.Second

// capDBUserCreateOnly is the agent capability for db_user.create's
// create_only, which never changes an existing MariaDB account.
const capDBUserCreateOnly = "db_user_create_only"

// restoredDBUserEngine is the engine of a database user restored from du.
// Bundles made before GH #1993 carry no engine: a user granted on a
// PostgreSQL database there is a PostgreSQL role.
func restoredDBUserEngine(du internalbackup.MetadataDatabaseUser, dbs []internalbackup.MetadataDatabase) string {
	switch du.Engine {
	case "mariadb", "postgres":
		return du.Engine
	}
	for _, g := range du.Grants {
		for _, db := range dbs {
			if db.Engine == "postgres" && ((g.DatabaseID != "" && g.DatabaseID == db.ID) || (g.DatabaseName != "" && g.DatabaseName == db.Name)) {
				return "postgres"
			}
		}
	}
	return "mariadb"
}

// createRestoredDBAccounts creates the MariaDB accounts of the database users
// this restore created, then the grants it created. It runs after Apply
// decided which rows to keep, so the agent acts only for those. A database
// user that existed before the restore keeps its password (users holds only
// new rows) and gets only the restored grants. A user restored without a
// usable password hash (a backup made before the agent captured it) gets a
// generated one, and the report says so. A row whose MariaDB side fails is
// taken back out, so the panel never lists a database user or grant that
// doesn't work. PostgreSQL users and grants are left as they were.
//
// SECURITY: an account is created only where none of its name exists
// (create_only): an existing MariaDB account is not the restored row's,
// whoever holds it, and a row on it would let the account's owner reset its
// password through the panel. An agent too old to promise that gets no
// account created and the rows are taken back out. From an uploaded file, a
// grant is made only on a database whose data the file restored: the file's
// author must not get a login to data they didn't supply.
func createRestoredDBAccounts(ctx context.Context, d Deps, accountID string, users []restoredDBAccount, grants []*models.DatabaseUserGrant, r *ApplyResult) {
	failed := map[string]bool{}
	createOnly := false
	for _, u := range users {
		if u.row.Engine == "mariadb" {
			createOnly = agentHasCapability(ctx, d.Agent, capDBUserCreateOnly)
			break
		}
	}
	for _, u := range users {
		if u.row.Engine != "mariadb" {
			continue
		}
		if !createOnly {
			failed[u.row.ID] = true
			dropRestoredDBUser(ctx, d, u.row, r, "this server's agent is too old to create its MariaDB account safely; run jabali update and restore again")
			continue
		}
		params := map[string]any{"db_user_name": u.row.Username, "create_only": true}
		newPassword := false
		if nativePasswordHashRe.MatchString(u.nativeHash) {
			params["password_hash"] = u.nativeHash
		} else {
			pw, err := randomDBPassword()
			if err != nil {
				failed[u.row.ID] = true
				dropRestoredDBUser(ctx, d, u.row, r, fmt.Sprintf("generating a password failed: %v", err))
				continue
			}
			params["password"] = pw
			newPassword = true
		}
		callCtx, cancel := context.WithTimeout(ctx, restoredDBAccountTimeout)
		_, err := d.Agent.Call(callCtx, "db_user.create", params)
		cancel()
		if err != nil {
			failed[u.row.ID] = true
			var ae *agentwire.AgentError
			if errors.As(err, &ae) && ae.Code == agentwire.CodeAlreadyExists {
				dropRestoredDBUser(ctx, d, u.row, r, "a MariaDB account with this name already exists on this server, and it is not this account's")
			} else {
				dropRestoredDBUser(ctx, d, u.row, r, fmt.Sprintf("creating its MariaDB account failed: %v", err))
			}
			continue
		}
		if newPassword {
			r.Errors = append(r.Errors, fmt.Sprintf("db_user %s (%s): restored with a new password, because the backup doesn't carry its MariaDB password; set one under Databases and in the site's settings", u.row.ID, u.row.Username))
		}
	}
	for _, g := range grants {
		if failed[g.DatabaseUserID] {
			// Its user's row is gone; so is the grant row.
			dropRestoredGrant(ctx, d, g, r, "")
			continue
		}
		du, uErr := d.DatabaseUsers.FindByID(ctx, g.DatabaseUserID)
		db, dErr := d.Databases.FindByID(ctx, g.DatabaseID)
		if uErr != nil || dErr != nil || du == nil || db == nil || du.UserID != accountID || db.UserID != accountID {
			dropRestoredGrant(ctx, d, g, r, "its database or database user is not one of this account's")
			continue
		}
		if du.Engine == "postgres" || db.Engine == "postgres" {
			continue
		}
		if d.Untrusted && !d.RestoredDatabases[db.Name] {
			dropRestoredGrant(ctx, d, g, r, fmt.Sprintf("the uploaded backup didn't restore %s's data, so it can't grant access to it", db.Name))
			continue
		}
		params := map[string]any{"db_name": db.Name, "db_user_name": du.Username, "grant_level": g.GrantLevel}
		if privs := grantPrivileges(g.Privileges); len(privs) > 0 {
			params["privileges"] = privs
		}
		callCtx, cancel := context.WithTimeout(ctx, restoredDBAccountTimeout)
		_, err := d.Agent.Call(callCtx, "db_user.grant", params)
		cancel()
		if err != nil {
			dropRestoredGrant(ctx, d, g, r, fmt.Sprintf("granting it in MariaDB failed: %v", err))
		}
	}
}

// dropRestoredDBUser takes a database user row this restore created back out
// and reports why.
func dropRestoredDBUser(ctx context.Context, d Deps, row *models.DatabaseUser, r *ApplyResult, why string) {
	if err := d.DatabaseUsers.Delete(ctx, row.ID); err != nil {
		why += fmt.Sprintf(" (and removing its row failed: %v)", err)
	} else {
		r.DatabaseUsers--
	}
	r.Errors = append(r.Errors, fmt.Sprintf("db_user %s (%s): not restored: %s", row.ID, row.Username, why))
}

// dropRestoredGrant takes a grant row this restore created back out and, with
// a why, reports it.
func dropRestoredGrant(ctx context.Context, d Deps, g *models.DatabaseUserGrant, r *ApplyResult, why string) {
	if err := d.DatabaseGrants.Delete(ctx, g.ID); err != nil {
		why = strings.TrimSpace(why + fmt.Sprintf(" (removing its row failed: %v)", err))
	} else {
		r.DatabaseGrants--
	}
	if why != "" {
		r.Errors = append(r.Errors, fmt.Sprintf("db_grant %s: not restored: %s", g.ID, why))
	}
}

// grantPrivileges splits a grant row's privilege list ("ALL" or
// "SELECT,INSERT,…"). The agent checks each against its whitelist.
func grantPrivileges(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.ToUpper(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// agentHasCapability reports whether ag lists capability name in agent.version.
// An older agent lists none.
func agentHasCapability(ctx context.Context, ag agent.AgentInterface, name string) bool {
	callCtx, cancel := context.WithTimeout(ctx, restoredDBAccountTimeout)
	defer cancel()
	raw, err := ag.Call(callCtx, "agent.version", nil)
	if err != nil {
		return false
	}
	var v struct {
		Capabilities []string `json:"capabilities"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	for _, c := range v.Capabilities {
		if c == name {
			return true
		}
	}
	return false
}

// randomDBPassword is a new database user password.
func randomDBPassword() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
