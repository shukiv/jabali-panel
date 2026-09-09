package dbops

// dbops_chown.go — reassign a single database (and the DB users bound only to
// it) from one tenant to another (GH #1609).
//
// This reuses the GH #1238 re-prefix primitives that user-rename is built on
// (agent db.rename_db / db.rename_user / db_user.grant / db_user.revoke), but
// scoped to ONE database instead of a whole account, and crossing an owner
// boundary instead of a username change. Because the panel names every DB and
// DB user `<owner-username>_*` and the tenant list filters on that prefix, a
// reassign is NOT a user_id repoint — the database and its users are renamed
// onto the NEW owner's prefix so they stay visible and the new owner's
// `<newowner>_%.*` mysqladmin wildcard grant covers them, while the old owner's
// wildcard no longer matches.
//
// Order (fail-closed, mirrors renameUserDBArtifacts): run ALL the idempotent
// agent verbs first — rename the database, then each bound DB user, then
// re-point their grants onto the new names and revoke the stale ones — and only
// after the box is fully moved repoint the panel rows. Because every agent verb
// is a re-runnable no-op once done (old gone / new present), an agent-side
// failure leaves every panel row untouched, so a re-run resumes cleanly from the
// top. Each row write is a single atomic name+owner UPDATE (TransferOwner), so
// the only non-resumable window is between two local UPDATEs (no network); every
// completed row write is logged so a rare failure there can be finished by hand.
//
// v1 is deliberately conservative and REFUSES rather than half-move:
//   - postgres databases (the rename+regrant path is MariaDB-only here, same as
//     user-rename; a PG slice is a follow-up);
//   - a database that backs an application install (its config holds the old
//     owner's credentials — a cross-tenant leak, same hazard as domain chown);
//   - a DB user that also has grants on another database, or is owned by someone
//     other than the current owner (renaming it would break that other database);
//   - a database or DB user whose name isn't under the current owner's prefix
//     (admin-created off-convention — needs manual review);
//   - a name collision under the new owner.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Reassign-specific sentinels (create/delete sentinels like ErrDeps,
// ErrUserNotFound, ErrNotFound, ErrQuotaExceeded and ErrAttached are reused).
var (
	ErrReassignSameOwner    = errors.New("dbops: database is already owned by that user")
	ErrReassignOwnerInvalid = errors.New("dbops: new owner must be a fully-provisioned tenant")
	ErrReassignEngine       = errors.New("dbops: reassigning a postgres database isn't supported yet")
	ErrReassignPrefix       = errors.New("dbops: a name is not under the current owner's prefix (needs manual review)")
	ErrReassignSharedUser   = errors.New("dbops: a database user is shared with another database or owner")
	ErrReassignCollision    = errors.New("dbops: a name already exists under the new owner")
)

// ReassignInput is the shared input for both callers (REST now; CLI can follow).
type ReassignInput struct {
	DatabaseID string
	NewOwnerID string
}

// ReassignResult reports what moved, for the caller's response/audit.
type ReassignResult struct {
	Database     *models.Database
	NewName      string
	RenamedUsers []string // new (post-move) usernames
}

// ReassignDatabaseOwner moves one database + its exclusively-bound DB users to a
// new tenant, renaming them onto the new owner's prefix. See the file header for
// the ordering and refusal rules.
func ReassignDatabaseOwner(ctx context.Context, d Deps, in ReassignInput) (*ReassignResult, error) {
	if d.Users == nil || d.Databases == nil || d.DatabaseUsers == nil ||
		d.DatabaseGrants == nil || d.Installs == nil || d.Packages == nil || d.Agent == nil {
		return nil, ErrDeps
	}
	if in.DatabaseID == "" || in.NewOwnerID == "" {
		return nil, fmt.Errorf("%w: database id and new owner id required", ErrNameInvalid)
	}

	db, err := d.Databases.FindByID(ctx, in.DatabaseID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("%w: load database: %v", ErrInternal, err)
	}
	if db.Engine != "mariadb" {
		return nil, fmt.Errorf("%w: %q is a %s database", ErrReassignEngine, db.Name, db.Engine)
	}

	newOwner, err := d.Users.FindByID(ctx, in.NewOwnerID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("%w: load new owner: %v", ErrInternal, err)
	}
	if newOwner.IsAdmin || newOwner.Username == nil || *newOwner.Username == "" || newOwner.LinuxUID == nil {
		return nil, fmt.Errorf("%w: %s", ErrReassignOwnerInvalid, in.NewOwnerID)
	}
	if db.UserID == newOwner.ID {
		return nil, fmt.Errorf("%w: %q", ErrReassignSameOwner, db.Name)
	}

	oldOwner, err := d.Users.FindByID(ctx, db.UserID)
	if err != nil || oldOwner == nil || oldOwner.Username == nil || *oldOwner.Username == "" {
		return nil, fmt.Errorf("%w: resolve current owner of %q: %v", ErrInternal, db.Name, err)
	}
	oldPrefix := *oldOwner.Username + "_"
	newPrefix := *newOwner.Username + "_"
	if !strings.HasPrefix(db.Name, oldPrefix) {
		return nil, fmt.Errorf("%w: database %q", ErrReassignPrefix, db.Name)
	}
	newDBName := newPrefix + strings.TrimPrefix(db.Name, oldPrefix)

	// Precise app-install refusal (application_installs.db_id — GH #1609/#1238):
	// the install's config carries the OLD owner's DB credentials.
	if inst, err := d.Installs.FindByDBID(ctx, db.ID); err == nil && inst != nil {
		return nil, &AttachedError{InstallID: inst.ID}
	} else if err != nil && !errors.Is(err, repository.ErrNotFound) {
		// Fail closed: never move a database while the leak check is unavailable.
		return nil, fmt.Errorf("%w: app-install check: %v", ErrInternal, err)
	}

	// Package clamps on the NEW owner (#282: never fail-open a clamp). Load the
	// package once here; the DB clamp is checked now, the DB-user clamp after the
	// bound users are resolved (below).
	var newOwnerPkg *models.HostingPackage
	if newOwner.PackageID != nil && *newOwner.PackageID != "" {
		newOwnerPkg, err = d.Packages.FindByID(ctx, *newOwner.PackageID)
		if err != nil {
			return nil, fmt.Errorf("%w: load new owner package: %v", ErrInternal, err)
		}
		if newOwnerPkg.MaxDatabases > 0 {
			count, err := d.Databases.CountByUserID(ctx, newOwner.ID)
			if err != nil {
				return nil, fmt.Errorf("%w: count new owner databases: %v", ErrInternal, err)
			}
			if count >= int64(newOwnerPkg.MaxDatabases) {
				return nil, fmt.Errorf("%w: %d/%d databases", ErrQuotaExceeded, count, newOwnerPkg.MaxDatabases)
			}
		}
	}

	// Resolve the DB users bound to this database (via grants), and refuse any
	// that are shared with another database/owner or off-convention.
	type moveUser struct {
		id      string
		oldName string
		newName string
	}
	grants, err := d.DatabaseGrants.ListByDatabaseID(ctx, db.ID)
	if err != nil {
		return nil, fmt.Errorf("%w: list grants for database: %v", ErrInternal, err)
	}
	seen := map[string]bool{}
	var users []moveUser
	for i := range grants {
		duID := grants[i].DatabaseUserID
		if seen[duID] {
			continue
		}
		seen[duID] = true

		du, err := d.DatabaseUsers.FindByID(ctx, duID)
		if err != nil || du == nil {
			return nil, fmt.Errorf("%w: load database user %s: %v", ErrInternal, duID, err)
		}
		if du.UserID != db.UserID {
			return nil, fmt.Errorf("%w: user %q is owned by another tenant", ErrReassignSharedUser, du.Username)
		}
		if du.Engine != "mariadb" {
			return nil, fmt.Errorf("%w: user %q is a %s user", ErrReassignEngine, du.Username, du.Engine)
		}
		if !strings.HasPrefix(du.Username, oldPrefix) {
			return nil, fmt.Errorf("%w: database user %q", ErrReassignPrefix, du.Username)
		}
		// Shared-user refusal: any grant of this user on a DIFFERENT database.
		duGrants, err := d.DatabaseGrants.ListByDatabaseUserID(ctx, duID)
		if err != nil {
			return nil, fmt.Errorf("%w: list grants for user %q: %v", ErrInternal, du.Username, err)
		}
		for j := range duGrants {
			if duGrants[j].DatabaseID != db.ID {
				return nil, fmt.Errorf("%w: user %q also has access to another database", ErrReassignSharedUser, du.Username)
			}
		}
		users = append(users, moveUser{
			id:      du.ID,
			oldName: du.Username,
			newName: newPrefix + strings.TrimPrefix(du.Username, oldPrefix),
		})
	}

	// Package DB-user clamp on the NEW owner (#282): the move brings len(users)
	// DB users across, so the new owner's total must not breach MaxDatabaseUsers.
	if newOwnerPkg != nil && newOwnerPkg.MaxDatabaseUsers > 0 && len(users) > 0 {
		count, err := d.DatabaseUsers.CountByUserID(ctx, newOwner.ID)
		if err != nil {
			return nil, fmt.Errorf("%w: count new owner database users: %v", ErrInternal, err)
		}
		if count+int64(len(users)) > int64(newOwnerPkg.MaxDatabaseUsers) {
			return nil, fmt.Errorf("%w: %d/%d database users", ErrQuotaExceeded, count+int64(len(users)), newOwnerPkg.MaxDatabaseUsers)
		}
	}

	// Collision check under the new owner (database + each user), before any move.
	if exists, err := d.Databases.ExistsByUserAndName(ctx, newOwner.ID, newDBName); err != nil {
		return nil, fmt.Errorf("%w: collision check database: %v", ErrInternal, err)
	} else if exists {
		return nil, fmt.Errorf("%w: database %q", ErrReassignCollision, newDBName)
	}
	for _, u := range users {
		if exists, err := d.DatabaseUsers.ExistsByUserAndUsername(ctx, newOwner.ID, u.newName); err != nil {
			return nil, fmt.Errorf("%w: collision check user: %v", ErrInternal, err)
		} else if exists {
			return nil, fmt.Errorf("%w: database user %q", ErrReassignCollision, u.newName)
		}
	}

	// ---- mutate: ALL idempotent agent verbs first, THEN the panel rows ----
	//
	// Every agent verb below is a re-runnable no-op once done (db.rename_db /
	// db.rename_user succeed when old is gone and new is present; grant/revoke
	// tolerate an already-applied state). Running the whole box-side move before
	// touching any row means an agent-side failure leaves every panel row on the
	// OLD names/owner, so a re-run resumes cleanly from the top. Only the row
	// writes at the end are non-idempotent — and each is a single atomic UPDATE.

	// 1. Rename the database on the box. db.rename_db refuses
	//    views/triggers/routines/events, so a DB carrying those is rejected here
	//    before anything moves.
	if _, err := d.Agent.Call(ctx, "db.rename_db", map[string]any{"old_db": db.Name, "new_db": newDBName}); err != nil {
		return nil, fmt.Errorf("%w: rename database %q → %q: %v", ErrAgentFailed, db.Name, newDBName, err)
	}

	// 2. Rename each bound DB user (RENAME USER carries its grants onto the new
	//    account, still on the OLD db name).
	for _, u := range users {
		if _, err := d.Agent.Call(ctx, "db.rename_user", map[string]any{"old_name": u.oldName, "new_name": u.newName}); err != nil {
			return nil, fmt.Errorf("%w: rename database user %q → %q: %v", ErrAgentFailed, u.oldName, u.newName, err)
		}
	}

	// 3. Re-point each grant onto the new db + user, then revoke the stale grant
	//    the RENAME USER carried over on the OLD db name.
	for i := range grants {
		g := &grants[i]
		var nu string
		for _, u := range users {
			if u.id == g.DatabaseUserID {
				nu = u.newName
				break
			}
		}
		if nu == "" {
			continue
		}
		privs := splitPrivileges(g.Privileges)
		if _, err := d.Agent.Call(ctx, "db_user.grant", map[string]any{
			"db_name": newDBName, "db_user_name": nu, "privileges": privs,
		}); err != nil {
			return nil, fmt.Errorf("%w: re-grant %q on %q: %v", ErrAgentFailed, nu, newDBName, err)
		}
		if _, err := d.Agent.Call(ctx, "db_user.revoke", map[string]any{
			"db_name": db.Name, "db_user_name": nu, "privileges": privs,
		}); err != nil {
			return nil, fmt.Errorf("%w: revoke stale grant on %q: %v", ErrAgentFailed, db.Name, err)
		}
	}

	// 4. Box fully moved. Repoint the panel rows — each an atomic name+owner
	//    UPDATE. A failure past this point is the one non-resumable window (the
	//    box is on the new names but a row still claims the old owner); log every
	//    completed step so an operator can finish the reassign by hand.
	if err := d.Databases.TransferOwner(ctx, db.ID, newOwner.ID, newDBName); err != nil {
		return nil, fmt.Errorf("%w: repoint database row to %q owner %s (box already moved — manual resync needed): %v", ErrInternal, newDBName, newOwner.ID, err)
	}
	logInfo(d, "dbops.reassign: database row repointed", "database_id", db.ID, "new_name", newDBName, "new_owner", newOwner.ID)
	for _, u := range users {
		if err := d.DatabaseUsers.TransferOwner(ctx, u.id, newOwner.ID, u.newName); err != nil {
			return nil, fmt.Errorf("%w: repoint database-user row to %q owner %s (box already moved — manual resync needed): %v", ErrInternal, u.newName, newOwner.ID, err)
		}
		logInfo(d, "dbops.reassign: database-user row repointed", "database_user_id", u.id, "new_name", u.newName, "new_owner", newOwner.ID)
	}

	db.Name = newDBName
	db.UserID = newOwner.ID
	res := &ReassignResult{Database: db, NewName: newDBName}
	for _, u := range users {
		res.RenamedUsers = append(res.RenamedUsers, u.newName)
	}
	return res, nil
}

// splitPrivileges turns a stored "ALL" / "SELECT,INSERT" grant string into the
// list db_user.grant/revoke expect. Empty → ["ALL"] (the row default). Mirrors
// the userops helper of the same name (GH #1238).
func splitPrivileges(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return []string{"ALL"}
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return []string{"ALL"}
	}
	return out
}
