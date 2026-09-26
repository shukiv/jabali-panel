// userops_lifecycle.go — JAB-190 (ADR-0164) extractions so the REST
// handlers and the automation API share ONE write path for password
// rotation, package assignment, and the destructive delete cascade.
//
// Every function here is a verbatim move of the logic that previously
// lived inline in panel-api/internal/api/users.go; behavior changes are
// bugs. The gin-specific parts (claims checks, JSON shapes) stay in the
// handlers — these functions speak typed sentinels.
package userops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dbops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ftpops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// LimitsReconciler is the narrow reconciler slice SetPackage needs
// (satisfied by *reconciler.Reconciler).
type LimitsReconciler interface {
	ReconcileUserLimits(ctx context.Context)
}

// Lifecycle sentinels. Callers map to HTTP codes:
// ErrNoKratosIdentity→409, ErrKratosUnavailable→503,
// ErrKratosSetPassword→502, ErrAgentPassword→502 (kratos already
// synced), ErrDockerTeardown→409.
var (
	ErrNoKratosIdentity  = errors.New("userops: user has no kratos identity")
	ErrKratosUnavailable = errors.New("userops: kratos client not wired")
	ErrKratosSetPassword = errors.New("userops: kratos set password failed")
	ErrAgentPassword     = errors.New("userops: agent password sync failed")
)

// DockerTeardownError reports the docker apps whose host teardown failed
// during DeleteCascade. The user row is NOT deleted when this is returned
// (Gitea #532: never orphan live containers).
type DockerTeardownError struct {
	Slugs []string
}

func (e *DockerTeardownError) Error() string {
	return "userops: docker app teardown failed for " + strings.Join(e.Slugs, ", ")
}

// DBCleanupError reports database engine objects (MariaDB schemas + logins and
// the _mysqladmin shadow, plus the PostgreSQL _pgadmin shadow role) whose
// host-side drop failed during DeleteCascade. The user row is NOT deleted when
// this is returned: deleting it CASCADEs the databases/database_users metadata
// away, so a failed drop would strand the engine object on the host with no
// panel row left to name it — invisible to `jabali db list`, excluded from
// backups, grants still live (#1010 / db fix 541612543+4d455c150, originally
// inline in the delete handler; kept here so the automation delete path gets the
// same guarantee; JAB-289 added the PG shadow role). Retrying the delete is
// safe: the domain/docker/ACL steps are idempotent and re-list empty on the retry.
type DBCleanupError struct {
	Objects []string
}

func (e *DBCleanupError) Error() string {
	return "userops: host-side database engine drop failed for " + strings.Join(e.Objects, ", ")
}

// RotatePassword rotates a user's auth password. Order is load-bearing
// (moved verbatim from the REST update handler): bcrypt hash → Kratos
// SetPassword (authoritative post-M20; failure = old password keeps
// working) → agent user.password for non-admin OS accounts (failure =
// Kratos already rotated, DB hash intentionally NOT updated — matches
// the pre-extraction handler) → DB hash update.
//
// Owner re-authentication (current_password) is the REST handler's
// concern; callers of RotatePassword act with admin authority.
func RotatePassword(ctx context.Context, d Deps, user *models.User, password string) error {
	if d.Users == nil || d.BcryptCost == 0 {
		return ErrDeps
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), d.BcryptCost)
	if err != nil {
		return fmt.Errorf("%w: bcrypt: %v", ErrInternal, err)
	}
	if user.KratosIdentityID == nil || *user.KratosIdentityID == "" {
		return ErrNoKratosIdentity
	}
	if d.KratosClient == nil {
		return ErrKratosUnavailable
	}
	kctx, kcancel := context.WithTimeout(ctx, 10*time.Second)
	if err := d.KratosClient.SetPassword(kctx, *user.KratosIdentityID, string(hash)); err != nil {
		kcancel()
		if d.Log != nil {
			d.Log.Warn("kratos SetPassword failed", "user_id", user.ID, "err", err)
		}
		return fmt.Errorf("%w: %v", ErrKratosSetPassword, err)
	}
	kcancel()
	if !user.IsAdmin && user.Username != nil && *user.Username != "" && d.Agent != nil {
		actx, acancel := context.WithTimeout(ctx, 10*time.Second)
		_, agentErr := d.Agent.Call(actx, "user.password", map[string]any{
			"username": *user.Username,
			"password": password,
		})
		acancel()
		if agentErr != nil {
			if d.Log != nil {
				d.Log.Warn("agent user.password failed", "user_id", user.ID, "err", agentErr)
			}
			return fmt.Errorf("%w: %v", ErrAgentPassword, agentErr)
		}
	}
	user.PasswordHash = string(hash)
	if err := d.Users.Update(ctx, user); err != nil {
		return fmt.Errorf("%w: persist hash: %v", ErrInternal, err)
	}
	return nil
}

// ValidatePackage checks a non-empty package id resolves to a hosting
// package. Shared by the REST update handler and SetPackage.
func ValidatePackage(ctx context.Context, d Deps, packageID string) error {
	if d.Packages == nil {
		return fmt.Errorf("%w: packages repo not wired", ErrInternal)
	}
	if _, err := d.Packages.FindByID(ctx, packageID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ErrInvalidPackage
		}
		return fmt.Errorf("%w: load package: %v", ErrInternal, err)
	}
	return nil
}

// SetPackage assigns (or, with nil, clears) a user's hosting package and,
// on an actual change, kicks the limits reconciler so POSIX quota +
// cgroup drop-ins land without waiting for the next 60s tick — the same
// sequence the REST update handler performs.
func SetPackage(ctx context.Context, d Deps, user *models.User, packageID *string, rec LimitsReconciler) error {
	if d.Users == nil {
		return ErrDeps
	}
	prev := ""
	if user.PackageID != nil {
		prev = *user.PackageID
	}
	next := ""
	if packageID != nil && *packageID != "" {
		if err := ValidatePackage(ctx, d, *packageID); err != nil {
			return err
		}
		user.PackageID = packageID
		next = *packageID
	} else {
		user.PackageID = nil
	}
	if err := d.Users.Update(ctx, user); err != nil {
		return fmt.Errorf("%w: persist package: %v", ErrInternal, err)
	}
	if next != prev && rec != nil {
		go func() {
			bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			rec.ReconcileUserLimits(bgCtx)
		}()
	}
	return nil
}

// DeleteDeps carries the cascade-only collaborators DeleteCascade needs
// beyond Deps. All optional — nil skips that stage best-effort, exactly
// like the pre-extraction handler's nil checks.
type DeleteDeps struct {
	Databases     repository.DatabaseRepository
	DatabaseUsers repository.DatabaseUserRepository
	// FtpAccounts reaps the tenant's FTP/SFTP subaccounts on delete (JAB-265):
	// user_id is index-only with NO FK, and nothing else in the cascade touched
	// them, so a deleted tenant left live Unix credentials + jails + DB rows
	// orphaned (and the stray-alias reaper never swept them — it keys on a
	// MISSING row, but the rows survived). Optional: nil skips FTP reap.
	FtpAccounts repository.FtpAccountRepository
	// RevokeCacheACLs revokes the tenant's wp_<osuser> Redis cache ACLs
	// (GH #408 / ADR-0148). Passed as a callback so userops stays free of
	// the redis client dependency.
	RevokeCacheACLs func(ctx context.Context, osUser string) error
	// SyncOSTeardown removes the OS account (agent user.delete: FPM pools,
	// slice, account and home) before the user row, and fails the delete
	// with *OSTeardownError when that does not happen. A short-lived caller
	// must set it: the default runs the teardown in a goroutine after the row
	// delete, and a process that exits right after DeleteCascade returns (the
	// CLI) exits before that goroutine runs, leaving the tenant's
	// login-capable account and /home on the host. The long-running panel
	// leaves it false so a large home does not hold the HTTP request past its
	// write timeout.
	SyncOSTeardown bool
}

// OSTeardownError means the OS account could not be removed. It is returned
// only with SyncOSTeardown, before the user row is deleted, so the row stays
// and the delete can be run again.
type OSTeardownError struct {
	Username string
	Err      error
}

func (e *OSTeardownError) Error() string {
	return fmt.Sprintf("OS account %q could not be removed: %v", e.Username, e.Err)
}

func (e *OSTeardownError) Unwrap() error { return e.Err }

const (
	osTeardownBackgroundTimeout = 30 * time.Second
	// A synchronous teardown removes the whole home before returning; give a
	// large one time to finish instead of reporting a failure the agent then
	// completes anyway.
	osTeardownSyncTimeout = 10 * time.Minute
)

// removeOSAccount runs the agent's user.delete for username and then asks
// the malware monitor to drop the home's watches. An account that does not
// exist counts as removed.
func removeOSAccount(ctx context.Context, a AgentCaller, username string, timeout time.Duration) error {
	tctx, cancel := context.WithTimeout(ctx, timeout)
	_, err := a.Call(tctx, "user.delete", map[string]any{
		"username":    username,
		"remove_home": true,
	})
	cancel()
	var ae *agentwire.AgentError
	if errors.As(err, &ae) && ae.Code == agentwire.CodeNotFound {
		err = nil
	}
	// M33: re-evaluate maldet inotify watches after teardown. Best-effort.
	rctx, rcancel := context.WithTimeout(ctx, 10*time.Second)
	defer rcancel()
	_, _ = a.Call(rctx, "security.malware.monitor.reload", map[string]any{})
	return err
}

// reapTenantFtpAccounts tears down every FTP/SFTP subaccount a tenant owns as
// part of DeleteCascade (JAB-265) through the FTP Account Lifecycle Module
// (JAB-276), which shares the host teardown with the per-account delete but
// keeps the cascade's policy: the row is deleted regardless of the host
// result, and failures are logged, never fail the user delete.
func reapTenantFtpAccounts(ctx context.Context, d Deps, dd DeleteDeps, userID, username string) {
	ftpops.ReapOwner(ctx, ftpops.Deps{
		Agent:    d.Agent,
		Accounts: dd.FtpAccounts,
		Users:    d.Users,
		Packages: d.Packages,
		Log:      d.Log,
	}, userID, username)
}

// DeleteCascade removes EVERYTHING a user owns, then the user row, then
// the OS account — a verbatim move of the REST delete handler's cascade
// (docs and scar-comments preserved there in spirit; see ADR-0164). With
// DeleteDeps.SyncOSTeardown the OS account goes before the row instead, and
// a failure returns *OSTeardownError with the row kept.
//
// Caller-side protections (self-delete, last-admin, authorization) are
// NOT here — handlers enforce them before calling. On a docker teardown
// failure the cascade stops BEFORE the user row is touched and returns
// *DockerTeardownError (Gitea #532).
//
// actor is recorded in the structured audit log line.
func DeleteCascade(ctx context.Context, d Deps, dd DeleteDeps, target *models.User, actor string) error {
	if d.Users == nil {
		return ErrDeps
	}
	id := target.ID

	// Cascade-delete all domains owned by this user. DB first, then
	// out-of-band agent teardown via the reconciler. Best-effort: any
	// per-domain failure is logged, never fails the user delete.
	if d.Domains != nil {
		const batchSize = 500
		for {
			owned, _, err := d.Domains.ListByUserID(ctx, id, repository.ListOptions{Limit: batchSize})
			if err != nil {
				logWarn(d, "cascade delete: list user domains failed", "user_id", id, "err", err)
				break
			}
			if len(owned) == 0 {
				break
			}
			for i := range owned {
				dom := &owned[i]
				// JAB-236: the domain lifecycle module's durable delete —
				// tombstone before the row delete, teardown (Stalwart purge +
				// vhost + pdns zone) after it, retried by the reconciler if the
				// async attempt fails or the panel restarts mid-cascade.
				// Per-domain failure is logged, never fails the user delete.
				if _, err := domainops.Delete(ctx, domainDeleteDeps(d), dom.ID, dom.Name, true); err != nil {
					logWarn(d, "cascade delete: domain DB delete failed",
						"user_id", id, "domain_id", dom.ID, "domain", dom.Name, "err", err)
				}
			}
			if len(owned) < batchSize {
				break
			}
		}
	}

	// Capture username BEFORE deleting so the OS teardown works after the
	// DB row is gone. For admins, username is NULL.
	var username string
	if target.Username != nil {
		username = *target.Username
	}

	// JAB-265: reap the tenant's FTP/SFTP subaccounts BEFORE the OS user is
	// deleted (ftpaccount.delete resolves the tenant by name, which must still
	// exist).
	reapTenantFtpAccounts(ctx, d, dd, id, username)

	// The database and database_user drops both dispatch per engine through
	// dbops.DropDatabaseHost / DropDatabaseUserHost — two things differ per
	// engine and BOTH matter: the command name, and the payload key. Before
	// AC6 the MariaDB defaults were used for every row, so a Postgres role
	// survived the cascade — and because `db_user.drop` reaches MariaDB, whose
	// DROP USER IF EXISTS succeeds on a name that was never there, the failure
	// never surfaced (GH #1013).

	// Cascade-drop MariaDB schemas + grants BEFORE the panel row goes
	// (which CASCADEs the metadata rows). A failed drop is NOT best-effort:
	// collect the failures and abort before the row delete below, so the panel
	// row stays as the only handle on the orphaned MariaDB object (see
	// DBCleanupError).
	var undropped []string
	if dd.Databases != nil && d.Agent != nil && username != "" {
		const batchSize = 500
		for {
			dbs, _, dbErr := dd.Databases.ListByUserID(ctx, id, repository.ListOptions{Limit: batchSize})
			if dbErr != nil {
				logWarn(d, "cascade delete: list user databases failed", "user_id", id, "err", dbErr)
				break
			}
			if len(dbs) == 0 {
				break
			}
			for i := range dbs {
				dbName := dbs[i].Name
				// Dispatch on the engine the row was created with. Sending
				// db.drop at a postgres row reaches MariaDB, whose
				// `DROP DATABASE IF EXISTS` cheerfully succeeds on a name that
				// was never there — so the abort below never fires and the real
				// Postgres database is orphaned on the HAPPY path, with the
				// panel row cascaded away behind it (GH #1013).
				//
				// databases.go's own delete already picks the command this way.
				// Route the drop through the one lifecycle operation so the
				// engine dispatch is not copied here (JAB-275 AC6); dropCmd is
				// the same command DropDatabaseHost sends, kept only to name it
				// in the failure log.
				dropCmd := dbops.DropDatabaseCommand(dbs[i].Engine)
				agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				dropErr := dbops.DropDatabaseHost(agentCtx, d.Agent, dbs[i].Engine, dbName)
				cancel()
				if dropErr != nil {
					undropped = append(undropped, dbName)
					logError(d, "cascade delete: database drop failed — aborting so the row stays addressable",
						"user_id", id, "db_name", dbName, "engine", dbs[i].Engine, "cmd", dropCmd, "err", dropErr)
				}
			}
			if len(dbs) < batchSize {
				break
			}
		}
	}
	if dd.DatabaseUsers != nil && d.Agent != nil && username != "" {
		const batchSize = 500
		for {
			dbus, _, duErr := dd.DatabaseUsers.ListByUserID(ctx, id, repository.ListOptions{Limit: batchSize})
			if duErr != nil {
				logWarn(d, "cascade delete: list user database_users failed", "user_id", id, "err", duErr)
				break
			}
			if len(dbus) == 0 {
				break
			}
			for i := range dbus {
				duName := dbus[i].Username
				// A Postgres ROLE is not dropped by db_user.drop, and the
				// payload key differs too. Route the login drop through the one
				// lifecycle operation so the engine dispatch is not copied here
				// (JAB-275 AC6); cmd is the same command DropDatabaseUserHost
				// sends, kept only to name it in the failure log. The databases
				// loop above already ran, so a Postgres role no longer owns its
				// database and DROP ROLE can succeed.
				cmd := dbops.DropDatabaseUserCommand(dbus[i].Engine)
				agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				dropErr := dbops.DropDatabaseUserHost(agentCtx, d.Agent, dbus[i].Engine, duName)
				cancel()
				if dropErr != nil {
					undropped = append(undropped, duName)
					logError(d, "cascade delete: database user drop failed — aborting so the row stays addressable",
						"user_id", id, "db_user_name", duName, "engine", dbus[i].Engine, "cmd", cmd, "err", dropErr)
				}
			}
			if len(dbus) < batchSize {
				break
			}
		}
	}

	// Drop the per-user MariaDB shadow-admin (<osuser>_mysqladmin) — not a
	// database_users row, so the loop above never reaps it. db_user.drop is
	// an idempotent DROP USER IF EXISTS; no shadow account = harmless no-op.
	if d.Agent != nil && username != "" {
		shadowUser := username + "_mysqladmin"
		agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, dropErr := d.Agent.Call(agentCtx, "db_user.drop", map[string]any{"db_user_name": shadowUser})
		cancel()
		if dropErr != nil {
			// Counts toward the abort like any other login: it has a valid
			// password and is not a database_users row, so nothing downstream
			// would ever find it again — the exact orphan class this guards.
			undropped = append(undropped, shadowUser)
			logError(d, "cascade delete: mysqladmin shadow drop failed — aborting so the login is not orphaned",
				"user_id", id, "mysqladmin_user", shadowUser, "err", dropErr)
		}
	}

	// Drop the per-user PostgreSQL shadow-admin ROLE (<osuser>_pgadmin) — the
	// Adminer-SSO counterpart to the MariaDB _mysqladmin shadow above (JAB-289).
	// Like it, the role is not a database_users row, so the reap loop never
	// touches it; unlike it, it lives on Postgres, so db_user.drop (MariaDB)
	// would never reach it. Left unreaped, deleting a tenant who used Adminer
	// SSO against Postgres orphaned a live LOGIN role with a decryptable
	// password (pgadmin_password_enc) — the same orphan class this guards.
	//
	// Guarded on a provisioned pgadmin_username: only PG-SSO tenants have one,
	// and the guard also avoids calling db.postgres.drop_role on boxes where the
	// optional Postgres engine isn't installed. db.postgres.drop_role is an
	// idempotent DROP ROLE IF EXISTS.
	if d.Agent != nil && target.PgadminUsername != nil && *target.PgadminUsername != "" {
		pgShadow := *target.PgadminUsername
		agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, dropErr := d.Agent.Call(agentCtx, "db.postgres.drop_role", map[string]any{"role": pgShadow})
		cancel()
		if dropErr != nil {
			undropped = append(undropped, pgShadow)
			logError(d, "cascade delete: pgadmin shadow role drop failed — aborting so the login is not orphaned",
				"user_id", id, "pgadmin_user", pgShadow, "err", dropErr)
		}
	}

	// Docker apps: tear down containers + data BEFORE the panel row
	// CASCADEs the metadata. If ANY teardown fails, stop — never leave a
	// live container with no owner row (Gitea #532).
	if d.DockerApps != nil && d.Agent != nil {
		apps, derr := d.DockerApps.ListByUserID(ctx, id)
		if derr != nil {
			logWarn(d, "cascade delete: list user docker apps failed", "user_id", id, "err", derr)
		}
		var teardownFailed []string
		for _, app := range apps {
			agentCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			// purge_volumes=true: rm -rf the app data tree, not just compose
			// down — otherwise deleted users orphan data under
			// /var/lib/jabali/docker-apps (Gitea #523).
			_, delErr := d.Agent.Call(agentCtx, "docker_app.delete", map[string]any{"slug": app.EffectiveSlug(), "purge_volumes": true})
			cancel()
			if delErr != nil {
				logWarn(d, "cascade delete: docker_app.delete failed",
					"user_id", id, "slug", app.EffectiveSlug(), "err", delErr)
				teardownFailed = append(teardownFailed, app.EffectiveSlug())
			}
		}
		if len(teardownFailed) > 0 {
			return &DockerTeardownError{Slugs: teardownFailed}
		}
	}

	// Remove the Kratos identity so username + email free up immediately
	// (GH#132 follow-up). Synchronous + before the DB delete; best-effort
	// on a Kratos outage.
	if d.KratosClient != nil && target.KratosIdentityID != nil && *target.KratosIdentityID != "" {
		kctx, kcancel := context.WithTimeout(ctx, 15*time.Second)
		if kerr := d.KratosClient.DeleteIdentity(kctx, *target.KratosIdentityID); kerr != nil {
			logWarn(d, "user delete: kratos identity delete failed (orphan may block recreate)",
				"user_id", id, "identity_id", *target.KratosIdentityID, "err", kerr)
		}
		kcancel()
	}

	// Revoke the tenant's WordPress-cache Redis ACL user (GH #408 /
	// ADR-0148). Best-effort.
	if dd.RevokeCacheACLs != nil && username != "" {
		rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
		if rErr := dd.RevokeCacheACLs(rctx, username); rErr != nil {
			logWarn(d, "cascade delete: revoke tenant cache ACL failed",
				"user_id", id, "username", username, "err", rErr)
		}
		rcancel()
	}

	// Point of no return: deleting the row CASCADEs databases/database_users
	// away. If any host-side drop above failed, stop here — the panel rows are
	// the only remaining handle on those MariaDB objects. Placed after the
	// best-effort kratos/redis steps, mirroring the original inline order.
	if len(undropped) > 0 {
		logError(d, "cascade delete: aborted, host-side drops failed",
			"user_id", id, "objects", undropped)
		return &DBCleanupError{Objects: undropped}
	}

	// Always-destructive OS teardown — the operator chose delete; the
	// cascade follows. With SyncOSTeardown it runs here, before the row
	// delete, so a failure keeps the row as the handle to run the delete
	// again. The caller's deadline is dropped: the teardown has its own.
	if dd.SyncOSTeardown && d.Agent != nil && username != "" {
		if err := removeOSAccount(context.WithoutCancel(ctx), d.Agent, username, osTeardownSyncTimeout); err != nil {
			logError(d, "cascade delete: OS account teardown failed — user row kept",
				"user_id", id, "username", username, "err", err)
			return &OSTeardownError{Username: username, Err: err}
		}
	}

	if err := d.Users.Delete(ctx, id); err != nil {
		return fmt.Errorf("%w: delete user row: %v", ErrInternal, err)
	}

	// Default: fire-and-forget after the row delete (see SyncOSTeardown).
	if !dd.SyncOSTeardown && d.Agent != nil && username != "" {
		agentRef := d.Agent
		go func() {
			if err := removeOSAccount(context.Background(), agentRef, username, osTeardownBackgroundTimeout); err != nil {
				logWarn(d, "user agent teardown failed", "user_id", id, "username", username, "err", err)
			}
		}()
	}

	if d.Log != nil {
		d.Log.Info("audit",
			"event", "user_deleted",
			"actor_id", actor,
			"target_id", id,
			"target_email", target.Email)
	}
	return nil
}

// CascadePreview is the read-only dry-run enumeration of what
// DeleteCascade would remove (ADR-0164: dry_run never rehearses).
type CascadePreview struct {
	Domains       int `json:"domains"`
	Databases     int `json:"databases"`
	DatabaseUsers int `json:"database_users"`
	DockerApps    int `json:"docker_apps"`
}

// PreviewDeleteCascade counts the user's cascade-affected resources
// without touching anything.
func PreviewDeleteCascade(ctx context.Context, d Deps, dd DeleteDeps, userID string) (CascadePreview, error) {
	var p CascadePreview
	if d.Domains != nil {
		if _, n, err := d.Domains.ListByUserID(ctx, userID, repository.ListOptions{Limit: 1}); err == nil {
			p.Domains = int(n)
		}
	}
	if dd.Databases != nil {
		if _, n, err := dd.Databases.ListByUserID(ctx, userID, repository.ListOptions{Limit: 1}); err == nil {
			p.Databases = int(n)
		}
	}
	if dd.DatabaseUsers != nil {
		if _, n, err := dd.DatabaseUsers.ListByUserID(ctx, userID, repository.ListOptions{Limit: 1}); err == nil {
			p.DatabaseUsers = int(n)
		}
	}
	if d.DockerApps != nil {
		if apps, err := d.DockerApps.ListByUserID(ctx, userID); err == nil {
			p.DockerApps = len(apps)
		}
	}
	return p, nil
}

// logWarn guards the optional logger.
// domainDeleteDeps hands the domain lifecycle module (JAB-279) the slice of
// Deps a domain delete needs.
func domainDeleteDeps(d Deps) domainops.DeleteDeps {
	return domainops.DeleteDeps{
		Domains:   d.Domains,
		Teardowns: d.DomainTeardowns,
		Ports:     d.PortAllocations,
		Agent:     d.Agent,
		Log:       d.Log,
	}
}

func logWarn(d Deps, msg string, args ...any) {
	if d.Log != nil {
		d.Log.Warn(msg, args...)
	}
}

func logError(d Deps, msg string, args ...any) {
	if d.Log != nil {
		d.Log.Error(msg, args...)
	}
}
