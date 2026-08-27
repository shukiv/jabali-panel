package userops

// GH #1238 — re-prefix a tenant's DB artifacts when their username changes, so a
// later same-name user can't inherit them (the mysqladmin GRANT is a
// <prefix>_%.* wildcard) and a delete doesn't orphan a role.
//
// Order matters: databases first (the on-disk RENAME TABLE move), then DB users
// (RENAME USER carries their grants — still on the OLD db names), then re-point
// the per-DB grants onto the new names and REVOKE the stale ones, then the
// mysqladmin/pgadmin shadow roles (rename + re-point the wildcard grant). Each
// step skips work already done (rename verbs are idempotent), but note the
// caller renames the OS account + panel username BEFORE this runs, so a failure
// here leaves a partially re-prefixed tenant whose panel name is already the new
// one — recovery is manual (the CLI can't be re-invoked with the old name). The
// steps are ordered + logged so an operator can finish it by hand.
//
// This MOVES tenant data and BREAKS app configs (the DB name + user change) —
// the caller warns the operator. v1 covers MariaDB base-table DBs + Postgres;
// the agent refuses a MariaDB DB with views/triggers/routines/events.

import (
	"context"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// renameUserDBArtifacts is best-effort-fatal: any error aborts the rename so a
// re-run resumes (the OS account is already renamed by the caller at this point).
// It no-ops cleanly when the DB repos aren't wired (dev/test paths).
func renameUserDBArtifacts(ctx context.Context, d Deps, rd RenameDeps, target *models.User, oldName, newName string) error {
	if d.Agent == nil || rd.Databases == nil || rd.DatabaseUsers == nil || rd.DBUserGrants == nil {
		return nil
	}
	oldPrefix := oldName + "_"
	newPrefix := newName + "_"
	reprefix := func(name string) string {
		if strings.HasPrefix(name, oldPrefix) {
			return newPrefix + strings.TrimPrefix(name, oldPrefix)
		}
		return name
	}

	// 1. Rename each owned database. Keep old→new + engine keyed by db id for
	//    the grant re-point below.
	dbs, _, err := rd.Databases.ListByUserID(ctx, target.ID, repository.ListOptions{Limit: 10000})
	if err != nil {
		return fmt.Errorf("rename: list databases: %w", err)
	}
	type dbInfo struct{ oldName, newName, engine string }
	dbByID := make(map[string]dbInfo, len(dbs))
	for i := range dbs {
		db := &dbs[i]
		nn := reprefix(db.Name)
		dbByID[db.ID] = dbInfo{oldName: db.Name, newName: nn, engine: db.Engine}
		if nn == db.Name || db.Engine != "mariadb" { // PG refused in preflight
			continue
		}
		if _, err := d.Agent.Call(ctx, "db.rename_db", map[string]any{"old_db": db.Name, "new_db": nn}); err != nil {
			return fmt.Errorf("rename: move database %q → %q: %w", db.Name, nn, err)
		}
		if err := rd.Databases.UpdateName(ctx, db.ID, nn); err != nil {
			return fmt.Errorf("rename: persist database name (agent moved it — re-run to resync): %w", err)
		}
	}

	// 2. Rename each owned DB user (RENAME USER carries its grants onto the new
	//    account, still referencing the OLD db names — re-pointed in step 3).
	dus, _, err := rd.DatabaseUsers.ListByUserID(ctx, target.ID, repository.ListOptions{Limit: 10000})
	if err != nil {
		return fmt.Errorf("rename: list database users: %w", err)
	}
	type duInfo struct{ newName, engine string }
	duByID := make(map[string]duInfo, len(dus))
	var duIDs []string
	for i := range dus {
		du := &dus[i]
		nn := reprefix(du.Username)
		duByID[du.ID] = duInfo{newName: nn, engine: du.Engine}
		duIDs = append(duIDs, du.ID)
		if nn == du.Username || du.Engine != "mariadb" { // PG refused in preflight
			continue
		}
		if _, err := d.Agent.Call(ctx, "db.rename_user", map[string]any{"old_name": du.Username, "new_name": nn}); err != nil {
			return fmt.Errorf("rename: rename database user %q → %q: %w", du.Username, nn, err)
		}
		if err := rd.DatabaseUsers.UpdateUsername(ctx, du.ID, nn); err != nil {
			return fmt.Errorf("rename: persist database-user name (re-run to resync): %w", err)
		}
	}

	// 3. Re-point each grant onto the new db + user, then REVOKE the stale grant
	//    the RENAME USER carried over on the old db name (MariaDB — PG grants
	//    follow the role/db rename natively).
	grants, err := rd.DBUserGrants.ListByDatabaseUserIDs(ctx, duIDs)
	if err != nil {
		return fmt.Errorf("rename: list grants: %w", err)
	}
	for i := range grants {
		g := &grants[i]
		db, okDB := dbByID[g.DatabaseID]
		du, okDU := duByID[g.DatabaseUserID]
		if !okDB || !okDU || db.engine != "mariadb" {
			continue
		}
		privs := splitPrivileges(g.Privileges)
		if _, err := d.Agent.Call(ctx, "db_user.grant", map[string]any{
			"db_name": db.newName, "db_user_name": du.newName, "privileges": privs,
		}); err != nil {
			return fmt.Errorf("rename: re-grant %q on %q: %w", du.newName, db.newName, err)
		}
		if db.oldName != db.newName {
			if _, err := d.Agent.Call(ctx, "db_user.revoke", map[string]any{
				"db_name": db.oldName, "db_user_name": du.newName, "privileges": privs,
			}); err != nil {
				return fmt.Errorf("rename: revoke stale grant on %q: %w", db.oldName, err)
			}
		}
	}

	// 4. Shadow admin roles: rename + re-point the <prefix>_%.* wildcard grant.
	//    RENAME USER preserves the password hash, so the panel's stored password
	//    stays valid — only the row's name needs repointing.
	// (PG pgadmin is refused in the preflight, so only mysqladmin is handled.)
	if target.MysqladminUsername != nil && strings.HasPrefix(*target.MysqladminUsername, oldPrefix) {
		nn := reprefix(*target.MysqladminUsername)
		if _, err := d.Agent.Call(ctx, "db.rename_user", map[string]any{
			"old_name": *target.MysqladminUsername, "new_name": nn,
			"old_prefix": oldName, "new_prefix": newName,
		}); err != nil {
			return fmt.Errorf("rename: mysqladmin shadow role: %w", err)
		}
		if err := d.Users.UpdateShadowDBUsernames(ctx, target.ID, &nn, nil); err != nil {
			return fmt.Errorf("rename: persist shadow-role name (re-run to resync): %w", err)
		}
	}
	return nil
}

// refusePostgresArtifacts blocks the rename (before the OS mutation) if the
// tenant has any PostgreSQL database / DB user / admin role — v1 re-prefixes
// MariaDB only.
func refusePostgresArtifacts(ctx context.Context, rd RenameDeps, target *models.User, old string) error {
	if target.PgadminUsername != nil && *target.PgadminUsername != "" {
		return fmt.Errorf("rename: %q has a PostgreSQL admin role — re-prefixing PostgreSQL on rename isn't supported yet; not renaming", old)
	}
	if rd.Databases != nil {
		dbs, _, err := rd.Databases.ListByUserID(ctx, target.ID, repository.ListOptions{Limit: 10000})
		if err != nil {
			return fmt.Errorf("rename: checking databases: %w", err)
		}
		for i := range dbs {
			if dbs[i].Engine == "postgres" {
				return fmt.Errorf("rename: %q has a PostgreSQL database (%q) — re-prefixing PostgreSQL isn't supported yet; migrate or drop it first", old, dbs[i].Name)
			}
		}
	}
	if rd.DatabaseUsers != nil {
		dus, _, err := rd.DatabaseUsers.ListByUserID(ctx, target.ID, repository.ListOptions{Limit: 10000})
		if err != nil {
			return fmt.Errorf("rename: checking database users: %w", err)
		}
		for i := range dus {
			if dus[i].Engine == "postgres" {
				return fmt.Errorf("rename: %q has a PostgreSQL database user (%q) — not supported yet", old, dus[i].Username)
			}
		}
	}
	return nil
}

// splitPrivileges turns a stored "ALL" / "SELECT,INSERT" string into the list
// db_user.grant expects. Empty → ["ALL"] (the row default).
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
