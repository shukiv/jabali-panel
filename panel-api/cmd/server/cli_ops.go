package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
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

	// JAB-278: route through the one canonical User Account Lifecycle cascade
	// (userops.DeleteCascade, ADR-0164) that the REST + automation adapters use,
	// instead of a divergent CLI copy. The copy omitted Docker teardown (which
	// must BLOCK the delete on failure), FTP subaccount reaping (JAB-265), Redis
	// cache-ACL revocation, PostgreSQL role handling, and port-allocation
	// release, and it dropped the panel row even when a MariaDB drop failed —
	// leaving invisible orphans. The cascade owns the whole ordered teardown
	// (domains → MariaDB → docker(blocking) → kratos → redis → row → OS+home).
	var caller userops.AgentCaller
	if sharedAgent != nil {
		caller = sharedAgent
	}
	var kc *kratosclient.Client
	if sharedCfg.Auth.Kratos.PublicURL != "" {
		kc = kratosclient.NewClient(sharedCfg.Auth.Kratos.PublicURL, sharedCfg.Auth.Kratos.AdminURL)
	}
	deps := userops.Deps{
		Users:           users,
		Packages:        repository.NewPackageRepository(sharedDB),
		Domains:         domainRepoFromDB(),
		DockerApps:      repository.NewDockerAppRepository(sharedDB),
		DomainTeardowns: repository.NewDomainTeardownRepository(sharedDB),
		PortAllocations: repository.NewPortAllocationRepository(sharedDB),
		Agent:           caller,
		KratosClient:    kc,
		Log:             slog.Default(),
	}
	deleteDeps := userops.DeleteDeps{
		Databases:     databaseRepoFromDB(),
		DatabaseUsers: databaseUserRepoFromDB(),
		FtpAccounts:   repository.NewFtpAccountRepository(sharedDB),
		RevokeCacheACLs: func(ctx context.Context, osUser string) error {
			if sharedRedis == nil {
				return nil // redis not wired here; ACLs get reaped on the next cache op
			}
			return api.RevokeAllUserCacheACLs(ctx, sharedRedis, osUser)
		},
	}
	// purgeHome is always true for `jabali user delete` (destructive by
	// contract); DeleteCascade removes the home + OS account unconditionally.
	_ = purgeHome
	if err := userops.DeleteCascade(ctx, deps, deleteDeps, target, "cli"); err != nil {
		var dte *userops.DockerTeardownError
		if errors.As(err, &dte) {
			return fmt.Errorf("refusing to delete %s: Docker app teardown failed for %s — resolve on the host and retry",
				userID, strings.Join(dte.Slugs, ", "))
		}
		var dbe *userops.DBCleanupError
		if errors.As(err, &dbe) {
			return fmt.Errorf("account KEPT: MariaDB object(s) %v could not be dropped — deleting now would orphan them; retry once the agent is healthy",
				dbe.Objects)
		}
		return err
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

// deleteDomainDirect deletes a domain durably (JAB-236): tombstone before
// the row, then the SYNCHRONOUS host-side teardown (Stalwart purge, nginx
// vhost, pdns zone) via the shared userops path. teardownPending=true means
// the row is gone but the agent-side teardown failed — the tombstone stays
// and the reconciler retries it until it succeeds. Before this, the CLI
// deleted only the DB row and left the domain SERVING.
func deleteDomainDirect(ctx context.Context, domainID string) (d *models.Domain, teardownPending bool, err error) {
	if err := initConfig(); err != nil {
		return nil, false, err
	}
	if err := initDB(); err != nil {
		return nil, false, err
	}
	domains := domainRepoFromDB()
	d, err = domains.FindByID(ctx, domainID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, false, fmt.Errorf("domain %q not found", domainID)
		}
		return nil, false, fmt.Errorf("lookup domain: %w", err)
	}
	// Agent is best-effort at init: without it the row still deletes and
	// the tombstone carries the teardown to the reconciler.
	var caller userops.AgentCaller
	if err := initAgent(); err == nil && sharedAgent != nil {
		caller = sharedAgent
	}
	deps := userops.Deps{
		Domains:         domains,
		DomainTeardowns: repository.NewDomainTeardownRepository(sharedDB),
		PortAllocations: repository.NewPortAllocationRepository(sharedDB),
		Agent:           caller,
		Log:             sharedLog,
	}
	teardownPending, err = userops.DeleteDomain(ctx, deps, domainID, d.Name, false)
	if err != nil {
		return nil, false, fmt.Errorf("delete domain: %w", err)
	}
	return d, teardownPending, nil
}
