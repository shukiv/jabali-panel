package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/userops"
)

// Mirrors the HTTP-handler ops in internal/api/{users,domains}.go — list
// reads, single-row updates, delete with cascade — but goes straight to the
// DB so the CLI stays usable without a browser-mode Kratos session cookie
// (which is what /api/v1/* requires). All helpers assume initConfig +
// initDB already ran (returned no error); they'd panic-nil otherwise.
// That's intentional: these are hot-path wrappers, not library code.

// ---------- user ----------

// listUsersDirect returns every user ordered by created_at ASC. Page size is
// 1000 — enough for any single-operator install and matches the pre-M20 CLI.
func listUsersDirect(ctx context.Context) ([]models.User, error) {
	if err := initConfig(); err != nil {
		return nil, err
	}
	if err := initDB(); err != nil {
		return nil, err
	}
	users, _, err := userRepo().List(ctx, repository.ListOptions{Limit: 1000})
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	return users, nil
}

// deleteUserDirect removes a user and EVERYTHING they own. Matches the HTTP
// delete handler:
//   - refuses to delete the last admin
//   - cascades all owned domains
//   - drops every MariaDB schema + DB user the panel created for them
//   - deletes the linked Kratos identity (if any)
//   - tears down the OS account + /home (always — no preserve mode)
//
// The purgeHome param is kept on the signature for source-compat but is
// always treated as true. Caller is responsible for the "don't delete
// yourself" check (CLI runs as root, no authenticated caller).
func deleteUserDirect(ctx context.Context, userID string, purgeHome bool) error {
	if err := initConfig(); err != nil {
		return err
	}
	if err := initDB(); err != nil {
		return err
	}
	if err := initAgent(); err != nil {
		return err
	}

	users := userRepo()
	target, err := users.FindByID(ctx, userID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return fmt.Errorf("user %q not found", userID)
		}
		return fmt.Errorf("lookup user: %w", err)
	}
	if target.IsAdmin {
		n, err := users.CountAdmins(ctx)
		if err != nil {
			return fmt.Errorf("count admins: %w", err)
		}
		if n <= 1 {
			return fmt.Errorf("refusing to delete the last admin (would lock out the panel)")
		}
	}

	// Cascade domains first. Best-effort per-row so one failure doesn't
	// strand a half-deleted user — matches the HTTP handler.
	domains := domainRepoFromDB()
	if owned, _, err := domains.ListByUserID(ctx, userID, repository.ListOptions{Limit: 500}); err == nil {
		for _, d := range owned {
			// Purge the domain's Stalwart accounts BEFORE the row delete
			// FK-cascades the mailbox rows away — same call the HTTP
			// handler makes. Without this, jabali user delete left the
			// Stalwart account behind -> re-migrating the same address hit
			// {"type":"primaryKeyViolation","properties":["email"]}.
			// sharedAgent is *agent.Client (concrete); nil-guard at the
			// call site, not inside the helper (a nil *agent.Client boxed
			// into AgentCaller is a non-nil interface and would panic).
			if sharedAgent != nil {
				if err := userops.PurgeDomainMail(ctx, sharedAgent, d.Name); err != nil {
					slog.Warn("cli delete: stalwart domain purge failed",
						"user_id", userID, "domain", d.Name, "err", err)
				}
			}
			if err := domains.Delete(ctx, d.ID); err != nil {
				slog.Warn("cli delete: cascade domain failed",
					"user_id", userID, "domain_id", d.ID, "err", err)
				continue
			}
			// Reap the nginx vhost now the row is gone: sites-enabled +
			// sites-available <domain>.conf, reload, per-domain logs. The HTTP
			// handler does this via Reconciler.ReconcileDeleted; the CLI has no
			// reconciler, so it calls the agent's domain.delete RPC directly
			// (same teardown the domain DELETE route drives). Without it,
			// `jabali user delete` left orphan /etc/nginx/sites-*/<domain>.conf
			// behind after the DB row + /home docroot were already gone — the
			// vhost served a 404-docroot on the deleted domain's Host header.
			// Best-effort: log + continue, matching the other agent cascades.
			if sharedAgent != nil {
				agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				if _, err := sharedAgent.Call(agentCtx, "domain.delete", map[string]any{"domain": d.Name}); err != nil {
					slog.Warn("cli delete: nginx vhost teardown failed",
						"user_id", userID, "domain", d.Name, "err", err)
				}
				cancel()
			}
		}
	}

	// Drop MariaDB schemas + DB users via the agent BEFORE the panel row
	// goes (which CASCADEs the metadata). Best-effort: log + continue.
	dbs := databaseRepoFromDB()
	if dbs != nil && sharedAgent != nil {
		if rows, _, err := dbs.ListByUserID(ctx, userID, repository.ListOptions{Limit: 500}); err == nil {
			for _, d := range rows {
				agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				if _, err := sharedAgent.Call(agentCtx, "db.drop", map[string]any{"db_name": d.Name}); err != nil {
					slog.Warn("cli delete: db.drop failed",
						"user_id", userID, "db_name", d.Name, "err", err)
				}
				cancel()
			}
		}
	}
	dbus := databaseUserRepoFromDB()
	if dbus != nil && sharedAgent != nil {
		if rows, _, err := dbus.ListByUserID(ctx, userID, repository.ListOptions{Limit: 500}); err == nil {
			for _, du := range rows {
				agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				if _, err := sharedAgent.Call(agentCtx, "db_user.drop", map[string]any{"db_user_name": du.Username}); err != nil {
					slog.Warn("cli delete: db_user.drop failed",
						"user_id", userID, "db_user_name", du.Username, "err", err)
				}
				cancel()
			}
		}
	}

	// Drop the per-user MariaDB shadow-admin account (<osuser>_mysqladmin).
	// It is provisioned by db.mysqladmin.ensure but is not a database_users
	// row, so the loop above misses it — an orphaned MySQL login otherwise
	// survives the deleted account. Mirrors the HTTP delete cascade. Reuses
	// the idempotent db_user.drop (DROP USER IF EXISTS): a no-op when absent.
	if sharedAgent != nil && target.Username != nil && *target.Username != "" {
		shadowUser := *target.Username + "_mysqladmin"
		agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if _, err := sharedAgent.Call(agentCtx, "db_user.drop", map[string]any{"db_user_name": shadowUser}); err != nil {
			slog.Warn("cli delete: mysqladmin shadow drop failed",
				"user_id", userID, "mysqladmin_user", shadowUser, "err", err)
		}
		cancel()
	}

	// Kratos identity delete (if linked).
	if target.KratosIdentityID != nil && sharedCfg.Auth.Kratos.PublicURL != "" {
		k := kratosclient.NewClient(sharedCfg.Auth.Kratos.PublicURL, sharedCfg.Auth.Kratos.AdminURL)
		if err := k.DeleteIdentity(ctx, *target.KratosIdentityID); err != nil {
			// Non-fatal: log + continue with panel delete so the operator
			// isn't blocked by a Kratos 500. They can clean orphan
			// identities via `kratos identities delete <id>`.
			slog.Warn("cli delete: kratos identity delete failed",
				"user_id", userID, "identity_id", *target.KratosIdentityID, "err", err)
		}
	}

	// Panel row delete.
	if err := users.Delete(ctx, userID); err != nil {
		return fmt.Errorf("delete panel row: %w", err)
	}

	// OS teardown. Sync here — the CLI should surface agent failures
	// instead of fire-and-forget silence. purgeHome is always treated as
	// true by the handler's contract; jabali user delete is destructive.
	_ = purgeHome
	if sharedAgent != nil && target.Username != nil && *target.Username != "" {
		agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if _, err := sharedAgent.Call(agentCtx, "user.delete", map[string]any{
			"username":    *target.Username,
			"remove_home": true,
		}); err != nil {
			slog.Warn("cli delete: agent user.delete failed — panel row gone, OS user remains",
				"user_id", userID, "username", *target.Username, "err", err)
			return fmt.Errorf("panel row deleted but OS teardown failed: %w (manually clean up /home/%s + the OS account)",
				err, *target.Username)
		}
	}
	return nil
}

// ---------- domain ----------

func listDomainsDirect(ctx context.Context) ([]models.Domain, error) {
	if err := initConfig(); err != nil {
		return nil, err
	}
	if err := initDB(); err != nil {
		return nil, err
	}
	domains, _, err := domainRepoFromDB().List(ctx, repository.ListOptions{Limit: 1000})
	if err != nil {
		return nil, fmt.Errorf("list domains: %w", err)
	}
	return domains, nil
}

// setDomainEnabledDirect flips the is_enabled column. The reconciler picks
// up the change on its next tick and either materialises or tears down the
// nginx vhost. Returns the updated domain so the caller can confirm.
func setDomainEnabledDirect(ctx context.Context, domainID string, enabled bool) (*models.Domain, error) {
	if err := initConfig(); err != nil {
		return nil, err
	}
	if err := initDB(); err != nil {
		return nil, err
	}
	domains := domainRepoFromDB()
	d, err := domains.FindByID(ctx, domainID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, fmt.Errorf("domain %q not found", domainID)
		}
		return nil, fmt.Errorf("lookup domain: %w", err)
	}
	d.IsEnabled = enabled
	d.UpdatedAt = time.Now().UTC()
	if err := domains.Update(ctx, d); err != nil {
		return nil, fmt.Errorf("update domain: %w", err)
	}
	return d, nil
}

// deleteDomainDirect removes a domain row. The reconciler detects the
// missing row on its next tick and tears down the nginx vhost + SSL cert
// backlog. No inline nginx call — matches the HTTP handler.
func deleteDomainDirect(ctx context.Context, domainID string) (*models.Domain, error) {
	if err := initConfig(); err != nil {
		return nil, err
	}
	if err := initDB(); err != nil {
		return nil, err
	}
	domains := domainRepoFromDB()
	d, err := domains.FindByID(ctx, domainID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, fmt.Errorf("domain %q not found", domainID)
		}
		return nil, fmt.Errorf("lookup domain: %w", err)
	}
	if err := domains.Delete(ctx, domainID); err != nil {
		return nil, fmt.Errorf("delete domain row: %w", err)
	}
	return d, nil
}
