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

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// LimitsReconciler is the narrow reconciler slice SetPackage needs
// (satisfied by *reconciler.Reconciler).
type LimitsReconciler interface {
	ReconcileUserLimits(ctx context.Context)
}

// DomainReconciler is the narrow reconciler slice DeleteCascade needs
// (satisfied by *reconciler.Reconciler).
type DomainReconciler interface {
	ReconcileDeleted(ctx context.Context, domainName string)
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

// DBCleanupError reports MariaDB objects (schemas, logins, the mysqladmin
// shadow) whose host-side drop failed during DeleteCascade. The user row is NOT
// deleted when this is returned: deleting it CASCADEs the databases/
// database_users metadata away, so a failed drop would strand the MariaDB
// object on the host with no panel row left to name it — invisible to
// `jabali db list`, excluded from backups, grants still live (#1010 / db fix
// 541612543+4d455c150, originally inline in the delete handler; kept here so the
// automation delete path gets the same guarantee). Retrying the delete is safe:
// the domain/docker/ACL steps are idempotent and re-list empty on the retry.
type DBCleanupError struct {
	Objects []string
}

func (e *DBCleanupError) Error() string {
	return "userops: host-side MariaDB drop failed for " + strings.Join(e.Objects, ", ")
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
	Reconciler    DomainReconciler
	// RevokeCacheACLs revokes the tenant's wp_<osuser> Redis cache ACLs
	// (GH #408 / ADR-0148). Passed as a callback so userops stays free of
	// the redis client dependency.
	RevokeCacheACLs func(ctx context.Context, osUser string) error
}

// DeleteCascade removes EVERYTHING a user owns, then the user row, then
// the OS account — a verbatim move of the REST delete handler's cascade
// (docs and scar-comments preserved there in spirit; see ADR-0164).
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
				name := dom.Name
				// Purge EVERY Stalwart registry account under this domain BEFORE
				// the domain delete FK-cascades the panel mailbox rows away. Purge
				// BY DOMAIN (not per row) so orphans die too. Best-effort.
				if d.Agent != nil {
					if delErr := PurgeDomainMail(ctx, d.Agent, name); delErr != nil {
						logWarn(d, "cascade delete: stalwart domain purge failed",
							"user_id", id, "domain", name, "err", delErr)
					}
				}
				if err := d.Domains.Delete(ctx, dom.ID); err != nil {
					logWarn(d, "cascade delete: domain DB delete failed",
						"user_id", id, "domain_id", dom.ID, "domain", name, "err", err)
					continue
				}
				if dd.Reconciler != nil {
					// Fire-and-forget — don't block the user delete on nginx
					// teardown. Fresh context: the request ctx ends with the caller.
					name := name
					go func() {
						bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer cancel()
						dd.Reconciler.ReconcileDeleted(bgCtx, name)
					}()
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
				agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				_, dropErr := d.Agent.Call(agentCtx, "db.drop", map[string]any{"db_name": dbName})
				cancel()
				if dropErr != nil {
					undropped = append(undropped, dbName)
					logError(d, "cascade delete: db.drop failed — aborting so the row stays addressable",
						"user_id", id, "db_name", dbName, "err", dropErr)
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
				agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				_, dropErr := d.Agent.Call(agentCtx, "db_user.drop", map[string]any{"db_user_name": duName})
				cancel()
				if dropErr != nil {
					undropped = append(undropped, duName)
					logError(d, "cascade delete: db_user.drop failed — aborting so the row stays addressable",
						"user_id", id, "db_user_name", duName, "err", dropErr)
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

	if err := d.Users.Delete(ctx, id); err != nil {
		return fmt.Errorf("%w: delete user row: %v", ErrInternal, err)
	}

	// Always-destructive OS teardown — the operator chose delete; the
	// cascade follows. Fire-and-forget.
	if d.Agent != nil && username != "" {
		agentRef := d.Agent
		go func() {
			bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := agentRef.Call(bgCtx, "user.delete", map[string]any{
				"username":    username,
				"remove_home": true,
			}); err != nil {
				logWarn(d, "user agent teardown failed", "user_id", id, "username", username, "err", err)
			}
			// M33: re-evaluate maldet inotify watches after teardown.
			rctx, rcancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer rcancel()
			_, _ = agentRef.Call(rctx, "security.malware.monitor.reload", map[string]any{})
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
