package cpanel

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// DBImportResult is returned to the restore-stage caller for
// progress reporting + manifest update.
type DBImportResult struct {
	Created int
	// AlreadyPresent counts dumps whose target database was already imported by
	// an earlier run of the SAME account and left in place.
	//
	// JAB-215: these used to be indistinguishable from a failure. A
	// --retry-from-scratch wipes the job's stages but does NOT drop the tenant
	// databases the previous run created, so every dump hit the
	// "already imported" branch, Created stayed 0, and the run ended
	// "DEGRADED: N dump(s) present but 0 imported" with a non-zero exit — on a
	// box whose databases were complete and healthy. That trains operators to
	// reach for --allow-degraded, which defeats the flag for real failures.
	//
	// Counted separately rather than folded into Created so the summary can say
	// which databases were re-imported and which were verified-existing.
	AlreadyPresent int
	Skipped        []string
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

	// PreservedUsers is the set of ORIGINAL source DB users that were recreated
	// on the destination with their native password HASH and granted on this
	// DB's namespaced destination during a --preserve-source-state restore
	// (JAB-207 / GH #723). When an app config's DB user is one of these, the
	// rewriter keeps the config's ORIGINAL user + password untouched — that
	// recreated user authenticates with the source password the file already
	// holds — and only namespaces DB_NAME + normalises DB_HOST.
	//
	// This subsumes the earlier same-name-collision special case: whether the
	// destination account keeps the source's name (source user == panel-managed
	// user, so db_user.create ALTERs one account to the source hash) or is named
	// differently (a second, source-named account is created), the config's user
	// is a member here and its credentials are preserved. Empty on a preserve-off
	// restore, or for a user we could NOT recreate (unsupported hash format) — in
	// which case the rewriter falls back to the panel-managed credentials so the
	// site still connects.
	PreservedUsers map[string]bool
}

// preservesUser reports whether configUser — the DB user an app config file
// references — was recreated as a preserved compat user for this DB. When true
// the app-config rewriter keeps the config's original DB user + password (that
// user authenticates with the source password already in the file) and only
// namespaces DB_NAME / normalises DB_HOST.
func (c DBCredential) preservesUser(configUser string) bool {
	if configUser == "" || c.PreservedUsers == nil {
		return false
	}
	return c.PreservedUsers[configUser]
}

// dbRestoreNameRe mirrors the agent's db.restore validation
// (^[a-zA-Z][a-zA-Z0-9_-]{0,63}$). Kept here so we reject names
// up-front rather than failing at agent dispatch time after a
// db.create succeeded — half-applied state.
var dbRestoreNameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)

// deriveDestDBName turns a SQL dump file path into the destination Jabali DB
// name (targetUsername-prefixed) plus the literal source base used for grant
// translation. ok=false means the derived name fails validation.
//
// Handles both cPanel `<user>_<db>.sql` and HestiaCP `<user>_<db>.mysql.sql` /
// `.pgsql.sql` dumps (JAB-27). The engine suffix (`.mysql`/`.pgsql`) is stripped
// after `.sql` so the underscore split doesn't leave a dotted logical name
// (`itflowapp_test.mysql.sql` → base `itflowapp_test` → logical `test` →
// `<user>_test`, not the previously-rejected `<user>_test.mysql`).
func deriveDestDBName(dumpPath, targetUsername string) (finalName, sourceBase string, ok bool) {
	base := strings.TrimSuffix(filepath.Base(dumpPath), ".sql")
	base = strings.TrimSuffix(base, ".mysql")
	base = strings.TrimSuffix(base, ".pgsql")
	// Strip the source-side username prefix if present (cpuser_blogdb → blogdb).
	// Falls back to the full base when there is no underscore.
	logical := base
	if idx := strings.Index(base, "_"); idx > 0 && idx < len(base)-1 {
		logical = base[idx+1:]
	}
	finalName = targetUsername + "_" + logical
	return finalName, base, dbRestoreNameRe.MatchString(finalName)
}

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
	preserveCredentials bool,
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
		finalName, base, ok := deriveDestDBName(dumpPath, targetUsername)
		if !ok {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: derived name %q rejects validator", dumpPath, finalName))
			continue
		}
		// `base` is the literal source DB name (`<user>_<db>`), which is also how
		// the grants dump names the DB — remember source→destination for the
		// compat-user grant translation below.
		sourceToFinalDB[base] = finalName

		// Idempotent collision check — resume after partial
		// failure no-ops on an already-imported DB.
		exists, err := dbsRepo.ExistsByUserAndName(ctx, targetUserID, finalName)
		if err != nil {
			return res, fmt.Errorf("collision check %s: %w", finalName, err)
		}
		if exists {
			res.AlreadyPresent++
			res.Skipped = append(res.Skipped, fmt.Sprintf(
				"%s: %q verified existing — left in place, not re-imported", dumpPath, finalName))
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
			// JAB-57: schema is materialised + the dump is loaded, but the
			// panel's row failed to land. Without compensation this leaves
			// an INVISIBLE restored database (customer data the UI, quota,
			// backups, and user-deletion cleanup never see) and a resume
			// gets worse — the collision check keys on DB rows, not system
			// state. Roll the side effect back: drop the restored DB so the
			// system matches the (absent) panel row and a retry re-imports
			// cleanly from the tarball dump. Safe on migration targets
			// (freshly provisioned; M35 restores INTO new accounts).
			dropCtx, dcancel := context.WithTimeout(ctx, 30*time.Second)
			_, dropErr := agentClient.Call(dropCtx, "db.drop", map[string]any{"db_name": finalName})
			dcancel()
			if dropErr != nil {
				res.Skipped = append(res.Skipped, fmt.Sprintf("%s: databases row insert failed AND rollback db.drop failed (orphan DB may remain): row=%v drop=%v", finalName, err, dropErr))
			} else {
				res.Skipped = append(res.Skipped, fmt.Sprintf("%s: databases row insert failed; restored DB rolled back: %v", finalName, err))
			}
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
						// JAB-57: the MariaDB user exists but its panel row
						// failed — an orphaned credential the panel doesn't
						// manage. Drop the user so no unmanaged login survives;
						// retry recreates it.
						duDropCtx, duDropCancel := context.WithTimeout(ctx, 30*time.Second)
						_, _ = agentClient.Call(duDropCtx, "db_user.drop", map[string]any{"db_user_name": finalName})
						duDropCancel()
						res.Skipped = append(res.Skipped, fmt.Sprintf("%s: database_users row failed; MariaDB user rolled back: %v", finalName, duErr))
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
								//
								// GH #723: key this map by the SOURCE DB name
								// (`base`), NOT the namespaced destination name
								// (`finalName`). The app-config rewriters
								// (rewriteWordPress/Joomla/Drupal/Magento) look
								// the credential up by the DB name they read out
								// of the config file — which is the source name
								// (e.g. `notary_45635`). Keying by `finalName`
								// (`<target>_45635`) only ever matched when the
								// target account shared the source's name; for a
								// multi-DB account migrated into a differently
								// named account the lookup missed and the
								// `len(creds)==1` fallback couldn't save it, so
								// wp-config was left pointing at a DB that no
								// longer exists → "Error establishing a database
								// connection". The value still carries the
								// namespaced DBName/DBUser so the rewrite writes
								// the panel-managed destination credentials.
								if res.Credentials == nil {
									res.Credentials = map[string]DBCredential{}
								}
								res.Credentials[base] = DBCredential{
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
	// JAB-48: recreating the source's original MySQL users with their native
	// password HASHES preserves old/weak/compromised DB credentials by default.
	// The panel-managed user above is fresh + rotated; apps rewritten to it work.
	// Only recreate the original-hash compatibility users when the operator opts
	// in (preserve.Credentials / --preserve-source-state) — for apps with
	// hardcoded original creds. Default: quarantine (record, don't create).
	// Source compat users come from the cpmove `mysql.sql` grants
	// (cPanel/WHM/CloudPanel/Plesk) OR, when a source adapter pre-populated
	// them, parsed.CompatUsers — HestiaCP keeps the native password hash in the
	// backup's per-DB db.conf MD5= field, not a mysql.sql (GH #633).
	grantsPath := filepath.Join(parsed.ExtractDir, "cpmove-"+parsed.SourceUser, "mysql.sql")
	compatUsers := parsed.CompatUsers
	if len(compatUsers) == 0 {
		if cu, gerr := ParseMySQLGrants(grantsPath); gerr == nil {
			compatUsers = cu
		}
	}
	if len(compatUsers) > 0 && !preserveCredentials {
		res.Skipped = append(res.Skipped, fmt.Sprintf(
			"compat_users: %d source MySQL user(s) with original password hashes NOT recreated (opt in with --preserve-source-state; apps using the panel-managed user are unaffected)",
			len(compatUsers)))
	} else if len(compatUsers) > 0 {
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
			// JAB-207 / GH #723. This compat user was created with the source's
			// native password hash. db_user.create runs an unconditional ALTER
			// USER for the hash path (deliberate — GH #633, the preserved hash
			// must win): when the source user is spelled the same as the
			// panel-managed one (destination account keeps the source's name) it
			// ALTERs that one account to the source hash; when named differently
			// it creates a second, source-named account. Either way the account
			// authenticates with the ORIGINAL password, not the temp one the dump
			// loop generated. So any app config that references THIS user must
			// keep its original user + password (the source config already holds
			// the password that matches this hash) — the rewriter only namespaces
			// DB_NAME + normalises DB_HOST. Recorded per DB below, but only after
			// the grant on the namespaced destination also succeeds, so we never
			// promise a preserve for an account that can't actually reach the DB.
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
				// The recreated user now has both the source password hash and a
				// grant on this DB's namespaced destination — so an app config
				// pointing at (this DB, this user) keeps its original credentials.
				// res.Credentials is keyed by the SOURCE DB name (g.SourceDB).
				if cred, have := res.Credentials[g.SourceDB]; have {
					if cred.PreservedUsers == nil {
						cred.PreservedUsers = map[string]bool{}
					}
					cred.PreservedUsers[u.Name] = true
					res.Credentials[g.SourceDB] = cred
				}
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
