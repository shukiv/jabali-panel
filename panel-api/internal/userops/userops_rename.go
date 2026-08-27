package userops

import (
	"context"
	"fmt"
	"regexp"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// renameUsernameRegex mirrors the agent-side validation (POSIX username).
var renameUsernameRegex = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// RenameReconciler re-renders a domain's config after its owner's username — and
// thus its docroot path — changed. Satisfied by *reconciler.Reconciler.
type RenameReconciler interface {
	Schedule(domainID string)
}

// RenameDeps carries the extra repos RenameUser needs for its preflight refusals
// and post-rename reconcile, beyond the core Deps.
type RenameDeps struct {
	FtpAccounts repository.FtpAccountRepository
	PythonApps  repository.PythonAppRepository
	Reconciler  RenameReconciler
	// GH #1238 DB re-prefix: when all three are wired, the rename also moves the
	// tenant's databases / DB users / shadow roles onto the new prefix. Nil (dev
	// / CLI-without-DB) skips the DB step.
	Databases     repository.DatabaseRepository
	DatabaseUsers repository.DatabaseUserRepository
	DBUserGrants  repository.DatabaseUserGrantRepository
}

// RenameUser renames a tenant's Linux/login username in place (GH #1238).
//
// uid is the stable anchor, so no file re-chown is needed — the agent's
// user.rename does usermod -l / -d -m; here the panel repoints the DB (username
// + every docroot's /home/<old> prefix), re-keys the Kratos login identifier, and
// reconciles the owned domains so their vhosts re-render under the new path/pool.
//
// v1 REFUSES a rename when the tenant has FTP/SFTP subaccounts (their jails are
// bind-mounted under the home) or Python apps (app-root paths + services) — those
// need move handling a later version will add. Shadow DB roles and tenant database
// names deliberately keep their old-username prefix: the panel keys them by
// user_id, so they keep working; MariaDB has no RENAME DATABASE.
//
// Idempotent + abort-with-resume: the OS account is renamed first, then the DB.
// A failure after the agent step leaves the box ahead of the DB; re-running the
// command recovers (the agent no-ops, the DB steps retry).
func RenameUser(ctx context.Context, d Deps, rd RenameDeps, target *models.User, newName string) error {
	if target == nil {
		return fmt.Errorf("rename: nil target user")
	}
	if target.IsAdmin || target.Username == nil || *target.Username == "" {
		return fmt.Errorf("rename: only tenant accounts with a Linux username can be renamed")
	}
	old := *target.Username
	if newName == old {
		return fmt.Errorf("rename: new username is the same as the current one")
	}
	if !renameUsernameRegex.MatchString(newName) {
		return fmt.Errorf("rename: invalid new username %q (must match ^[a-z_][a-z0-9_-]{0,31}$)", newName)
	}
	if target.LinuxUID == nil {
		return fmt.Errorf("rename: %q has no linux_uid yet (account not fully provisioned)", old)
	}
	if d.Users == nil || d.Agent == nil {
		return fmt.Errorf("rename: users repository and agent must be wired")
	}

	// --- Preflight refusals, BEFORE the agent mutates the box ---
	if existing, err := d.Users.FindByUsername(ctx, newName); err == nil && existing != nil {
		return fmt.Errorf("rename: username %q is already taken", newName)
	}
	if rd.FtpAccounts != nil {
		n, err := rd.FtpAccounts.CountByUserID(ctx, target.ID)
		if err != nil {
			return fmt.Errorf("rename: checking FTP subaccounts: %w", err)
		}
		if n > 0 {
			return fmt.Errorf("rename: %q has %d FTP/SFTP subaccount(s) — remove them first (v1 does not move their jails)", old, n)
		}
	}
	if rd.PythonApps != nil {
		apps, err := rd.PythonApps.ListByUser(ctx, target.ID)
		if err != nil {
			return fmt.Errorf("rename: checking Python apps: %w", err)
		}
		if len(apps) > 0 {
			return fmt.Errorf("rename: %q has %d Python app(s) — remove them first (v1 does not move their app roots)", old, len(apps))
		}
	}
	// GH #1238: v1 re-prefixes MariaDB artifacts only. Refuse if the tenant has
	// any PostgreSQL database / role — re-prefixing those is a follow-up.
	if perr := refusePostgresArtifacts(ctx, rd, target, old); perr != nil {
		return perr
	}

	// --- Rename the OS account on the box ---
	uid := int(*target.LinuxUID)
	if _, err := d.Agent.Call(ctx, "user.rename", map[string]any{
		"old_username": old,
		"new_username": newName,
		"uid":          uid,
	}); err != nil {
		return fmt.Errorf("rename: agent user.rename failed: %w", err)
	}

	// --- Repoint the DB (box already renamed past this line) ---
	if err := d.Users.UpdateUsername(ctx, target.ID, newName); err != nil {
		return fmt.Errorf("rename: persist new username (box already renamed to %q — re-run to resync the DB): %w", newName, err)
	}
	if d.Domains != nil {
		if _, err := d.Domains.RewriteDocRootPrefix(ctx, target.ID, "/home/"+old, "/home/"+newName); err != nil {
			return fmt.Errorf("rename: rewrite docroots (re-run to resume): %w", err)
		}
	}
	if d.KratosClient != nil && target.KratosIdentityID != nil && *target.KratosIdentityID != "" {
		if err := d.KratosClient.UpdateUsernameTrait(ctx, *target.KratosIdentityID, newName); err != nil {
			logWarn(d, "rename: could not update the Kratos username trait (login identifier may lag until next set)", "user_id", target.ID, "err", err)
		}
	}

	// --- GH #1238: re-prefix the tenant's DBs / DB users / shadow roles so a
	// reused old username can't inherit them. Fatal-on-error so a partial move
	// resumes on re-run (every step is idempotent). ---
	if err := renameUserDBArtifacts(ctx, d, rd, target, old, newName); err != nil {
		return err
	}

	// --- Reconcile owned domains so vhosts re-render under the new path/pool ---
	if rd.Reconciler != nil && d.Domains != nil {
		if doms, _, err := d.Domains.ListByUserID(ctx, target.ID, repository.ListOptions{Limit: 10000}); err == nil {
			for i := range doms {
				rd.Reconciler.Schedule(doms[i].ID)
			}
		}
	}

	target.Username = &newName
	return nil
}
