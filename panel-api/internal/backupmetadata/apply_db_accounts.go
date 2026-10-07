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
// password the backup carried for it: the MariaDB hash or the PostgreSQL
// SCRAM verifier ("" when it carried none).
type restoredDBAccount struct {
	row        *models.DatabaseUser
	nativeHash string
	pgVerifier string
}

// nativePasswordHashRe is a mysql_native_password hash, the only form
// db_user.create takes.
var nativePasswordHashRe = regexp.MustCompile(`^\*[0-9A-Fa-f]{40}$`)

// restoredDBAccountTimeout bounds each agent call.
const restoredDBAccountTimeout = 30 * time.Second

// capDBUserCreateOnly is the agent capability for db_user.create's
// create_only, which never changes an existing MariaDB account.
const capDBUserCreateOnly = "db_user_create_only"

// capPGRoleCreateOnly is the agent capability for db.postgres.create_role's
// create_only and password_verifier, and for archive_postgres_databases.
const capPGRoleCreateOnly = "pg_role_create_only"

// dbEngine is a row's engine; an empty one is MariaDB, the column default.
func dbEngine(e string) string {
	if e == "postgres" {
		return "postgres"
	}
	return "mariadb"
}

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

// createRestoredDBAccounts creates the MariaDB accounts and PostgreSQL roles
// of the database users this restore created, then the grants it created. It
// runs after Apply decided which rows to keep, so the agent acts only for
// those. A database user that existed before the restore keeps its password
// (users holds only new rows) and gets only the restored grants. A user
// restored without a usable password (a backup made before the agent captured
// it, or a PostgreSQL role with an md5 password) gets a generated one, and
// the report says so. A row whose database side fails is taken back out, so
// the panel never lists a database user or grant that doesn't work.
//
// SECURITY: an account or role is created only where none of its name
// exists (create_only): an existing one is not the restored row's, whoever
// holds it, and a row on it would let the account's owner reset its password
// through the panel. An agent too old to promise that gets nothing created
// and the rows are taken back out. A grant joins a user and a database of the
// same engine only. From an uploaded file, a grant is made only on a database
// whose data the file restored: the file's author must not get a login to
// data they didn't supply.
func createRestoredDBAccounts(ctx context.Context, d Deps, accountID string, users []restoredDBAccount, grants []*models.DatabaseUserGrant, r *ApplyResult) {
	failed := map[string]bool{}
	createOnly := map[string]bool{}
	for _, u := range users {
		engine := dbEngine(u.row.Engine)
		if _, asked := createOnly[engine]; !asked {
			capName := capDBUserCreateOnly
			if engine == "postgres" {
				capName = capPGRoleCreateOnly
			}
			createOnly[engine] = agentHasCapability(ctx, d.Agent, capName)
		}
	}
	for _, u := range users {
		engine := dbEngine(u.row.Engine)
		if !createOnly[engine] {
			failed[u.row.ID] = true
			dropRestoredDBUser(ctx, d, u.row, r, fmt.Sprintf("this server's agent is too old to create its %s safely; run jabali update and restore again", dbAccountKind(engine)))
			continue
		}
		cmd, params := "db_user.create", map[string]any{"db_user_name": u.row.Username, "create_only": true}
		usable := nativePasswordHashRe.MatchString(u.nativeHash)
		if engine == "postgres" {
			cmd, params = "db.postgres.create_role", map[string]any{"role": u.row.Username, "create_only": true}
			usable = internalbackup.IsPostgresSCRAMVerifier(u.pgVerifier)
		}
		switch {
		case usable && engine == "postgres":
			params["password_verifier"] = u.pgVerifier
		case usable:
			params["password_hash"] = u.nativeHash
		default:
			pw, err := randomDBPassword()
			if err != nil {
				failed[u.row.ID] = true
				dropRestoredDBUser(ctx, d, u.row, r, fmt.Sprintf("generating a password failed: %v", err))
				continue
			}
			params["password"] = pw
		}
		callCtx, cancel := context.WithTimeout(ctx, restoredDBAccountTimeout)
		_, err := d.Agent.Call(callCtx, cmd, params)
		cancel()
		if err != nil {
			failed[u.row.ID] = true
			var ae *agentwire.AgentError
			if errors.As(err, &ae) && ae.Code == agentwire.CodeAlreadyExists {
				dropRestoredDBUser(ctx, d, u.row, r, fmt.Sprintf("a %s with this name already exists on this server, and it is not this account's", dbAccountKind(engine)))
			} else {
				dropRestoredDBUser(ctx, d, u.row, r, fmt.Sprintf("creating its %s failed: %v", dbAccountKind(engine), err))
			}
			continue
		}
		if !usable {
			r.Errors = append(r.Errors, fmt.Sprintf("db_user %s (%s): restored with a new password, because the backup doesn't carry its %s password; set one under Databases and in the site's settings", u.row.ID, u.row.Username, dbEngineName(engine)))
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
		engine := dbEngine(db.Engine)
		if dbEngine(du.Engine) != engine {
			dropRestoredGrant(ctx, d, g, r, fmt.Sprintf("%s is a %s database and %s a %s user", db.Name, dbEngineName(engine), du.Username, dbEngineName(dbEngine(du.Engine))))
			continue
		}
		if why := uploadedGrantRefusal(d, db); why != "" {
			dropRestoredGrant(ctx, d, g, r, why)
			continue
		}
		cmd, params := "db_user.grant", map[string]any{"db_name": db.Name, "db_user_name": du.Username, "grant_level": g.GrantLevel}
		if engine == "postgres" {
			// A PostgreSQL grant is all of the database, as the Databases page
			// makes it.
			cmd, params = "db.postgres.grant", map[string]any{"db_name": db.Name, "role": du.Username}
		} else if privs := grantPrivileges(g.Privileges); len(privs) > 0 {
			params["privileges"] = privs
		}
		callCtx, cancel := context.WithTimeout(ctx, restoredDBAccountTimeout)
		_, err := d.Agent.Call(callCtx, cmd, params)
		cancel()
		if err != nil {
			dropRestoredGrant(ctx, d, g, r, fmt.Sprintf("granting it in %s failed: %v", dbEngineName(engine), err))
		}
	}
}

// dbEngineName is an engine's name for the report.
func dbEngineName(engine string) string {
	if engine == "postgres" {
		return "PostgreSQL"
	}
	return "MariaDB"
}

// dbAccountKind is what a database user is in an engine, for the report.
func dbAccountKind(engine string) string {
	if engine == "postgres" {
		return "PostgreSQL role"
	}
	return "MariaDB account"
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

// uploadedGrantRefusal says why a grant from an uploaded file on database db
// isn't made, or "". The file's author knows its users' passwords, so the
// file grants access only to a database holding nothing but its own data.
// Each engine has its own list: a name restored as one engine says nothing
// about the other engine's database of that name.
func uploadedGrantRefusal(d Deps, db *models.Database) string {
	archive := d.ArchiveMariaDBs
	if dbEngine(db.Engine) == "postgres" {
		archive = d.ArchivePostgresDBs
	}
	switch {
	case !d.Untrusted:
		return ""
	case !d.RestoredDatabases[db.Name]:
		return fmt.Sprintf("the uploaded backup didn't restore %s's data, so it can't grant access to it", db.Name)
	case archive == nil:
		return fmt.Sprintf("this server's agent is too old to tell whether %s holds only the uploaded backup's data; run jabali update and restore again", db.Name)
	case !archive[db.Name]:
		return fmt.Sprintf("%s holds data that isn't the uploaded backup's (it wasn't empty before the restore, or the backup's data didn't load), so the uploaded backup can't grant access to it", db.Name)
	}
	return ""
}
