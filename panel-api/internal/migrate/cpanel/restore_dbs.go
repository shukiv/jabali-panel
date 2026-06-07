package cpanel

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/agent"
	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/ids"
	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/models"
	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/repository"
)

// DBImportResult is returned to the restore-stage caller for
// progress reporting + manifest update.
type DBImportResult struct {
	Created int
	Skipped []string
	// Credentials captures (db_name → DBCredential) for each DB +
	// user pair created during this restore pass. ImportAppConfigs
	// reads it to rewrite WordPress/Drupal/Joomla/Magento config
	// files in the user's homedir with the new (name, user, pass)
	// triple so apps boot against the migrated MariaDB.
	Credentials map[string]DBCredential
}

// DBCredential is one (db_name, db_user, db_pass) row the config-
// rewrite step uses to splice values into wp-config.php and
// friends.
type DBCredential struct {
	DBName   string
	DBUser   string
	Password string // plaintext temp_pwd printed in the manifest line
}

// dbRestoreNameRe mirrors the agent's db.restore validation
// (^[a-zA-Z][a-zA-Z0-9_-]{0,63}$). Kept here so we reject names
// up-front rather than failing at agent dispatch time after a
// db.create succeeded — half-applied state.
var dbRestoreNameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)

// ImportDatabases walks each .sql dump in the parsed tarball,
// derives the destination DB name (jabali-username-prefixed),
// invokes agent db.create + db.restore, and inserts a databases
// row. Idempotency: a name collision (same DB already exists for
// this user) skips the entry rather than failing the whole import
// — resume after a partial failure no-ops on already-imported
// dbs.
//
// agentClient is nullable for unit-test purposes; production
// callers always pass a live client.
func ImportDatabases(
	ctx context.Context,
	dbsRepo repository.DatabaseRepository,
	dbUsersRepo repository.DatabaseUserRepository,
	dbGrantsRepo repository.DatabaseUserGrantRepository,
	agentClient agent.AgentInterface,
	parsed *ParsedTarball,
	targetUserID, targetUsername string,
) (*DBImportResult, error) {
	if dbsRepo == nil {
		return nil, fmt.Errorf("ImportDatabases: dbs repo nil")
	}
	if agentClient == nil {
		return nil, fmt.Errorf("ImportDatabases: agent client nil")
	}
	if parsed == nil {
		return nil, fmt.Errorf("ImportDatabases: parsed nil")
	}
	if targetUserID == "" || targetUsername == "" {
		return nil, fmt.Errorf("ImportDatabases: targetUserID/targetUsername empty")
	}

	res := &DBImportResult{}
	// Source→destination DB-name map, populated as each dump is imported.
	// Used after the loop to translate the cpmove `mysql.sql` grants
	// (which name the source DB) → the namespaced destination DB names
	// when we recreate the original cPanel MySQL users (ADR-0094
	// compat-user path, fix for the "migrated app sees Access denied"
	// scar).
	sourceToFinalDB := map[string]string{}
	for _, dumpPath := range parsed.MySQLDumps {
		base := strings.TrimSuffix(filepath.Base(dumpPath), ".sql")
		// Strip the cPanel-side username prefix if present
		// (cpuser_blogdb → blogdb). Falls back to the full base
		// if no underscore — the operator's source DB name is
		// what we keep.
		logical := base
		if idx := strings.Index(base, "_"); idx > 0 && idx < len(base)-1 {
			logical = base[idx+1:]
		}
		// Apply jabali prefix to land in the destination
		// namespace. Same shape `databases` REST handler produces.
		finalName := targetUsername + "_" + logical
		if !dbRestoreNameRe.MatchString(finalName) {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: derived name %q rejects validator", dumpPath, finalName))
			continue
		}
		// Remember the source→destination mapping for the compat-user
		// grant translation below. `base` is the literal cpmove DB
		// name (`<cpacct>_<dbname>`) which is also how mysql.sql
		// names the DB in its GRANT lines.
		sourceToFinalDB[base] = finalName

		// Idempotent collision check — resume after partial
		// failure no-ops on an already-imported DB.
		exists, err := dbsRepo.ExistsByUserAndName(ctx, targetUserID, finalName)
		if err != nil {
			return res, fmt.Errorf("collision check %s: %w", finalName, err)
		}
		if exists {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: %q already imported", dumpPath, finalName))
			continue
		}

		// agent.db.create → materialise empty schema. Tight
		// timeout — CREATE DATABASE is single-statement.
		createCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err = agentClient.Call(createCtx, "db.create", map[string]any{
			"db_name":   finalName,
			"charset":   "utf8mb4",
			"collation": "utf8mb4_unicode_ci",
		})
		cancel()
		if err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: db.create failed: %v", finalName, err))
			continue
		}

		// agent.db.restore → mariadb < dump.sql. Generous
		// timeout — multi-GB dumps take real time. 30 minutes
		// per DB is generous; stuck-process kill is a separate
		// concern handled by the transient unit's own timeout.
		restoreCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		// reset_before_restore=true: ADR-0095 amendment 2026-05-12
		// stage idempotency. Migration restores partway, fails, then
		// resume-retry re-streams the dump. CREATE TABLE inside the
		// dump conflicts unless we DROP+CREATE the DB first. Migration
		// targets are freshly-provisioned by jabali — destroying the
		// DB is safe (M35 spec restores INTO new accounts, never over
		// operator data).
		_, err = agentClient.Call(restoreCtx, "db.restore", map[string]any{
			"db_name":              finalName,
			"path":                 dumpPath,
			"reset_before_restore": true,
		})
		cancel()
		if err != nil {
			// Best-effort cleanup: drop the empty DB so resume
			// doesn't trip the collision check + skip on retry.
			dropCtx, dcancel := context.WithTimeout(ctx, 10*time.Second)
			_, _ = agentClient.Call(dropCtx, "db.drop", map[string]any{"db_name": finalName})
			dcancel()
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: db.restore failed: %v", finalName, err))
			continue
		}

		row := &models.Database{
			ID:        ids.NewULID(),
			UserID:    targetUserID,
			Name:      finalName,
			Engine:    "mariadb",
			Charset:   "utf8mb4",
			Collation: "utf8mb4_unicode_ci",
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}
		if err := dbsRepo.Create(ctx, row); err != nil {
			// Schema is materialised, dump is loaded, but the
			// panel's row failed to land. Operator can SQL the
			// row in by hand from logs, or re-run import (the
			// idempotent check above will skip the existing
			// DB, but databases row insert here is what fails).
			// Surface the error rather than silent — restore
			// stage caller decides whether to retry.
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: databases row insert failed: %v", finalName, err))
			continue
		}
		res.Created++

		// Best-effort: create a MariaDB user with the same name as
		// the DB and GRANT ALL. Failure records a warning but never
		// rolls back the already-restored DB.
		if dbUsersRepo != nil && dbGrantsRepo != nil {
			plainPwd := ids.NewULID()
			hash, hashErr := bcrypt.GenerateFromPassword([]byte(plainPwd), bcrypt.DefaultCost)
			if hashErr != nil {
				res.Skipped = append(res.Skipped, fmt.Sprintf("%s: bcrypt db user: %v", finalName, hashErr))
			} else {
				userCtx, userCancel := context.WithTimeout(ctx, 30*time.Second)
				_, userErr := agentClient.Call(userCtx, "db_user.create", map[string]any{
					"db_user_name": finalName,
					"password":     plainPwd,
				})
				userCancel()
				if userErr != nil {
					res.Skipped = append(res.Skipped, fmt.Sprintf("%s: db_user.create: %v", finalName, userErr))
				} else {
					duRow := &models.DatabaseUser{
						ID:           ids.NewULID(),
						UserID:       targetUserID,
						Username:     finalName,
						Engine:       "mariadb",
						PasswordHash: string(hash),
						CreatedAt:    time.Now().UTC(),
						UpdatedAt:    time.Now().UTC(),
					}
					if duErr := dbUsersRepo.Create(ctx, duRow); duErr != nil {
						res.Skipped = append(res.Skipped, fmt.Sprintf("%s: database_users row: %v", finalName, duErr))
					} else {
						grantCtx, grantCancel := context.WithTimeout(ctx, 30*time.Second)
						_, grantErr := agentClient.Call(grantCtx, "db_user.grant", map[string]any{
							"db_name":      finalName,
							"db_user_name": finalName,
							"privileges":   []string{"ALL"},
						})
						grantCancel()
						if grantErr != nil {
							res.Skipped = append(res.Skipped, fmt.Sprintf("%s: db_user.grant: %v", finalName, grantErr))
						} else {
							gRow := &models.DatabaseUserGrant{
								ID:             ids.NewULID(),
								DatabaseID:     row.ID,
								DatabaseUserID: duRow.ID,
								GrantLevel:     "rw",
								Privileges:     "ALL",
								CreatedAt:      time.Now().UTC(),
								UpdatedAt:      time.Now().UTC(),
							}
							if gErr := dbGrantsRepo.Create(ctx, gRow); gErr != nil {
								res.Skipped = append(res.Skipped, fmt.Sprintf("%s: database_user_grants row: %v", finalName, gErr))
							} else {
								// Do NOT write the plaintext password into the
								// manifest — manifest_json is persisted in the
								// panel DB (migration_jobs.manifest_json, longtext)
								// and is queryable, so a generated DB credential
								// would sit in cleartext at rest. The real value
								// is spliced into the app config below (in-memory,
								// this run only); the operator resets via panel if
								// they need direct DB access.
								res.Skipped = append(res.Skipped, fmt.Sprintf(
									"%s: db_user created (temp password set — reset via panel) — change via panel",
									finalName))
								// Stash (name, user, plaintext-pwd) so the
								// config-rewrite step can splice values
								// into wp-config.php / configuration.php /
								// settings.php / app/etc/env.php files.
								if res.Credentials == nil {
									res.Credentials = map[string]DBCredential{}
								}
								res.Credentials[finalName] = DBCredential{
									DBName:   finalName,
									DBUser:   finalName,
									Password: plainPwd,
								}
							}
						}
					}
				}
			}
		}
	}

	// ADR-0094 amendment 2026-05-20: recreate the ORIGINAL cPanel
	// MySQL users on the destination, preserving their NAME and
	// password HASH from cpmove's `mysql.sql`. Without this the
	// migrated app's hardcoded creds (db.php / wp-config.php /
	// settings.php) all 1045 Access-denied — jabali had only created
	// a namespaced `<target>_<db>` user with a fresh random password
	// (see DBCredential above), forcing the operator to either edit
	// every db.php by hand or `CREATE USER … IDENTIFIED BY '<orig-pw>'`
	// in mysql. Now: the source user(s) are imported alongside the
	// panel-managed one with their ORIGINAL hash + grants → migrated
	// apps Just Work, zero config rewrite. Only `@localhost` entries
	// are kept (jabali is single-host); the panel-managed user above
	// remains for UI-driven password rotation.
	grantsPath := filepath.Join(parsed.ExtractDir, "cpmove-"+parsed.SourceUser, "mysql.sql")
	if compatUsers, gerr := ParseMySQLGrants(grantsPath); gerr == nil && len(compatUsers) > 0 {
		compatCreated := 0
		for _, u := range compatUsers {
			if !IsNativePasswordHash(u.Hash) {
				res.Skipped = append(res.Skipped, fmt.Sprintf("compat_user %s: skipped — unsupported hash format", u.Name))
				continue
			}
			ucCtx, ucCancel := context.WithTimeout(ctx, 30*time.Second)
			_, ucErr := agentClient.Call(ucCtx, "db_user.create", map[string]any{
				"db_user_name":  u.Name,
				"password_hash": u.Hash,
			})
			ucCancel()
			if ucErr != nil {
				res.Skipped = append(res.Skipped, fmt.Sprintf("compat_user %s: db_user.create: %v", u.Name, ucErr))
				continue
			}
			grantedDBs := 0
			for _, g := range u.Grant {
				finalDB, ok := sourceToFinalDB[g.SourceDB]
				if !ok || finalDB == "" {
					// The grant references a DB whose dump didn't
					// import (rare — operator-excluded? skipped by
					// validator?). Best-effort: skip.
					res.Skipped = append(res.Skipped, fmt.Sprintf(
						"compat_user %s: skip grant on source DB %q (no destination mapping)",
						u.Name, g.SourceDB))
					continue
				}
				privs := g.Privs
				if len(privs) == 0 {
					privs = []string{"ALL"}
				}
				gCtx, gCancel := context.WithTimeout(ctx, 30*time.Second)
				_, gErr := agentClient.Call(gCtx, "db_user.grant", map[string]any{
					"db_name":      finalDB,
					"db_user_name": u.Name,
					"privileges":   privs,
				})
				gCancel()
				if gErr != nil {
					res.Skipped = append(res.Skipped, fmt.Sprintf(
						"compat_user %s: db_user.grant on %s: %v", u.Name, finalDB, gErr))
					continue
				}
				grantedDBs++
			}
			compatCreated++
			res.Skipped = append(res.Skipped, fmt.Sprintf(
				"compat_user %s: created with original password hash + %d grant(s) (migrated apps with hardcoded creds keep working)",
				u.Name, grantedDBs))
		}
		if compatCreated > 0 {
			res.Skipped = append(res.Skipped, fmt.Sprintf("compat_users: created=%d (ADR-0094 amendment)", compatCreated))
		}
	}

	return res, nil
}
