package main

import (
	"context"
	"errors"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/apps"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Direct-DB helpers for `jabali app *`. Mirrors the pattern in cli_ops.go
// (user/domain) — straight to the DB so the CLI keeps working under
// a CLI environment where the Kratos session cookie isn't available.
//
// Scope is intentionally narrow: registry + list + get + delete. The
// HTTP create handler (api.applications.go) carries 16 per-app kicker
// goroutines and the DB-chain provisioner — all package-private. Adding
// `app create` direct-DB requires extracting that pipeline into a
// shared service package; tracked as a separate ticket.

// listAppRegistry returns every app descriptor the panel knows about.
// Build the registry in-process so the CLI doesn't have to hit a running
// jabali-panel (matches list/delete). Mutates no state.
func listAppRegistry() ([]apps.App, error) {
	reg := apps.New()
	if err := apps.RegisterDefaults(reg); err != nil {
		return nil, fmt.Errorf("register app defaults: %w", err)
	}
	return reg.List(), nil
}

// listAppsDirect returns every install ordered by created_at ASC. Page
// size 1000 matches listUsersDirect/listDomainsDirect — enough for any
// single-operator install.
func listAppsDirect(ctx context.Context) ([]models.ApplicationInstall, error) {
	if err := initConfig(); err != nil {
		return nil, err
	}
	if err := initDB(); err != nil {
		return nil, err
	}
	installs, _, err := repository.NewApplicationInstallRepository(sharedDB).
		List(ctx, repository.ListOptions{Limit: 1000})
	if err != nil {
		return nil, fmt.Errorf("list applications: %w", err)
	}
	return installs, nil
}

// getAppDirect fetches one install by ID. Returns a typed not-found so
// the CLI can render a clean message instead of a wrapped GORM error.
func getAppDirect(ctx context.Context, installID string) (*models.ApplicationInstall, error) {
	if err := initConfig(); err != nil {
		return nil, err
	}
	if err := initDB(); err != nil {
		return nil, err
	}
	install, err := repository.NewApplicationInstallRepository(sharedDB).FindByID(ctx, installID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, fmt.Errorf("application %q not found", installID)
		}
		return nil, fmt.Errorf("lookup application: %w", err)
	}
	return install, nil
}

// resolveAppSpec accepts either an install ULID or a domain name (which
// resolves to the single install at that domain's root). Domain-name form
// is the common CLI UX: `jabali app delete example.com` reads better than
// pasting an opaque install ID. If the domain name matches multiple
// installs (subdirectory installs), callers should pass the ULID instead.
func resolveAppSpec(ctx context.Context, spec string) (*models.ApplicationInstall, error) {
	if spec == "" {
		return nil, fmt.Errorf("application spec is empty")
	}
	if err := initConfig(); err != nil {
		return nil, err
	}
	if err := initDB(); err != nil {
		return nil, err
	}
	installs := repository.NewApplicationInstallRepository(sharedDB)

	// ULIDs are exactly 26 chars of Crockford base32; bare domain names
	// never look like that. Try ID first so we don't punish script-driven
	// callers with an extra round-trip.
	if len(spec) == 26 {
		if inst, err := installs.FindByID(ctx, spec); err == nil {
			return inst, nil
		} else if !errors.Is(err, repository.ErrNotFound) {
			return nil, fmt.Errorf("lookup application by id: %w", err)
		}
	}

	// Domain-name fallback: resolve domain → find install at that domain.
	dom, err := resolveDomainSpec(ctx, domainRepoFromDB(), spec)
	if err != nil {
		return nil, fmt.Errorf("no application found for %q: %w", spec, err)
	}
	inst, err := installs.FindByDomainID(ctx, dom.ID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, fmt.Errorf("no application installed on domain %q", dom.Name)
		}
		return nil, fmt.Errorf("lookup application by domain: %w", err)
	}
	return inst, nil
}

// deleteAppDirect mirrors the side effects of api.createDeleteAndKickAgent
// (panel-api/internal/api/wordpress.go). Order matters because the
// fk_wpinstalls_db FK is RESTRICT — see the comments in the HTTP path
// for the rationale; do not reorder without re-reading them.
//
// Steps:
//  1. Resolve domain + os_user + db user/grants for the agent payload
//  2. Mark status=deleting so concurrent dashboards stop trying to read it
//  3. Fire agent app.delete (files + nginx placeholder restore)
//  4. Drop grants → mariadb user → install row → mariadb database
//
// Errors during the agent calls are NOT fatal — the panel rows are still
// dropped so the operator can re-run after fixing the host-side issue.
// Each agent failure logs at warn level; the final error returned is
// only set when a critical step (status update, install delete) fails.
func deleteAppDirect(ctx context.Context, installID string) (*models.ApplicationInstall, error) {
	if err := initConfig(); err != nil {
		return nil, err
	}
	if err := initDB(); err != nil {
		return nil, err
	}
	if err := initAgent(); err != nil {
		return nil, err
	}

	installs := repository.NewApplicationInstallRepository(sharedDB)
	domains := repository.NewDomainRepository(sharedDB)
	users := userRepo()
	dbs := repository.NewDatabaseRepository(sharedDB)
	dbUsers := repository.NewDatabaseUserRepository(sharedDB)
	dbGrants := repository.NewDatabaseUserGrantRepository(sharedDB)

	install, err := installs.FindByID(ctx, installID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, fmt.Errorf("application %q not found", installID)
		}
		return nil, fmt.Errorf("lookup application: %w", err)
	}

	domain, err := domains.FindByID(ctx, install.DomainID)
	if err != nil {
		return nil, fmt.Errorf("lookup domain: %w", err)
	}

	owner, err := users.FindByID(ctx, install.UserID)
	if err != nil {
		return nil, fmt.Errorf("lookup owner: %w", err)
	}
	if owner.Username == nil || *owner.Username == "" {
		return nil, fmt.Errorf("owner %q has no linux username (orphaned install?)", install.UserID)
	}
	osUser := *owner.Username

	// Pre-resolve the DB user before we mark the install deleting so a
	// FK lookup failure aborts before we mutate anything.
	var dbUserID, dbUserUsername string
	if install.DBIDOr() != "" {
		grants, gErr := dbGrants.ListByDatabaseID(ctx, install.DBIDOr())
		if gErr == nil && len(grants) > 0 {
			dbUserID = grants[0].DatabaseUserID
			if dbu, duErr := dbUsers.FindByID(ctx, dbUserID); duErr == nil && dbu != nil {
				dbUserUsername = dbu.Username
			}
		}
	}

	if err := installs.UpdateStatus(ctx, installID, "deleting", nil, nil); err != nil {
		return nil, fmt.Errorf("mark deleting: %w", err)
	}

	// M16 Wave D — best-effort Hydra client teardown. Matches the HTTP
	// path (panel-api/internal/api/wordpress.go:createDeleteAndKickAgent):
	// orphan clients are harmless (redirect_uris point at a vanished
	// docroot) but they pollute `hydra list clients` and operators
	appType := install.AppType
	if appType == "" {
		// Pre-M19 rows had no AppType; the column default backfilled to
		// "wordpress" but treat empty defensively to avoid dispatching
		// app.delete with an empty discriminator (which the agent would
		// 400 on).
		appType = "wordpress"
	}

	// JAB-314: delegate to the shared, fail-closed delete lifecycle so the CLI
	// and the HTTP handler produce identical transcripts. RunAppDelete retains
	// the install row on an agent app.delete failure (retryable) and keeps the
	// DB rows visible when a drop fails — instead of the old behaviour of
	// dropping every panel row regardless, which is how invisible host/DB
	// orphans were made. It also tears down the app's auto-created cron jobs,
	// which this CLI path never did.
	if err := api.RunAppDelete(api.AppDeleteArgs{
		InstallID:      installID,
		UserID:         install.UserID,
		AppType:        appType,
		Subdirectory:   install.Subdirectory,
		DatabaseID:     install.DBIDOr(),
		DBUserID:       dbUserID,
		OSUser:         osUser,
		Docroot:        domain.DocRoot,
		DomainName:     domain.Name,
		DBUserUsername: dbUserUsername,
	}, api.AppDeleteDeps{
		Installs:       installs,
		Databases:      dbs,
		DatabaseUsers:  dbUsers,
		DatabaseGrants: dbGrants,
		CronJobs:       repository.NewCronJobRepository(sharedDB),
		Agent:          sharedAgent,
	}); err != nil {
		return install, err
	}
	return install, nil
}

// truncString clips s to max bytes (no UTF-8 awareness — last_error is
// stored as VARCHAR(1024) and the column truncates anyway; this just
// avoids sending an oversized payload to MariaDB).
func truncString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
