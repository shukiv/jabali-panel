package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// AppDeleteDeps is the repo + agent set the shared application-delete lifecycle
// needs. The HTTP handler builds it from ApplicationHandlerConfig; the operator
// CLI builds it from the shared DB + agent. Keeping one implementation is the
// point: HTTP and CLI deletes must produce identical side-effect transcripts
// (JAB-314).
type AppDeleteDeps struct {
	Installs       repository.ApplicationInstallRepository
	Databases      repository.DatabaseRepository
	DatabaseUsers  repository.DatabaseUserRepository
	DatabaseGrants repository.DatabaseUserGrantRepository
	CronJobs       repository.CronJobRepository
	Agent          agent.AgentInterface
}

// AppDeleteArgs identifies the install being torn down plus the pre-resolved
// host handles the agent needs. Callers resolve these once (domain, owner,
// db user/grants) before invoking RunAppDelete.
type AppDeleteArgs struct {
	InstallID      string
	UserID         string
	AppType        string
	Subdirectory   string
	DatabaseID     string
	DBUserID       string
	OSUser         string
	Docroot        string
	DomainName     string
	DBUserUsername string
}

// RunAppDelete is the single authoritative application-delete side-effect
// sequence, shared by the HTTP handler and the CLI (JAB-314). It is fail-closed
// so a partial failure never produces an invisible host or database orphan:
//
//   - Agent app.delete failure stamps status=failed and RETAINS the install row
//     for retry — it never proceeds to drop DB rows.
//   - A db_user.drop / db.drop failure KEEPS the corresponding panel rows so the
//     database/user stays visible (and retryable from the Databases page) rather
//     than surviving on the host with no row to name it. databases.go's own
//     delete refuses to remove a row unless its drop succeeds, so the retry
//     cannot reintroduce the orphan.
//
// It always runs on a detached 5-minute context so an accepted deletion outlives
// the caller's request context. Returns nil on full success; a non-nil error
// describes the retained state (for the CLI to surface — the HTTP path is
// fire-and-forget and ignores it).
func RunAppDelete(args AppDeleteArgs, deps AppDeleteDeps) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	appType := args.AppType
	if appType == "" {
		// Pre-M19 rows had no AppType; treat empty defensively so the agent
		// isn't handed an empty discriminator (which it 400s on).
		appType = "wordpress"
	}

	if deps.Agent == nil {
		msg := "agent not configured"
		if deps.Installs != nil {
			deps.Installs.UpdateStatus(ctx, args.InstallID, "failed", &msg, nil)
		}
		return errors.New(msg)
	}

	// Agent removes the app's files + restores the docroot placeholder. On
	// failure the install row is RETAINED (status=failed) so a retry is
	// possible — we do NOT fall through to drop the DB rows.
	if _, err := deps.Agent.Call(ctx, "app.delete", map[string]any{
		"app_type":     appType,
		"install_id":   args.InstallID,
		"os_user":      args.OSUser,
		"docroot":      args.Docroot,
		"subdirectory": args.Subdirectory,
		"domain":       args.DomainName,
	}); err != nil {
		msg := truncateError(fmt.Sprintf("agent delete failed: %v", err), 1024)
		deps.Installs.UpdateStatus(ctx, args.InstallID, "failed", &msg, nil)
		return fmt.Errorf("agent app.delete failed (install row retained for retry): %w", err)
	}

	// Tear down the app's auto-created cron jobs (they aren't FK-linked to the
	// install, so nothing else removes them).
	switch appType {
	case "itflow":
		removeITFlowCrons(ctx, deps, args.UserID, args.OSUser, appInstallPath(args.Docroot, args.Subdirectory))
	case "wordpress", "joomla", "mediawiki":
		removeAppCrons(ctx, deps, args.UserID, args.OSUser, appType, appInstallPath(args.Docroot, args.Subdirectory))
	}

	// DB-side cleanup, fail-closed. Keep the panel rows whenever the host-side
	// drop fails so a MariaDB database/user never survives as an invisible
	// orphan with no row to name it, absent from `db list` and from backups.
	dropFailed := false
	if args.DBUserID != "" && args.DBUserUsername != "" {
		if _, err := deps.Agent.Call(ctx, "db_user.drop", map[string]any{"db_user_name": args.DBUserUsername}); err != nil {
			dropFailed = true
			slog.ErrorContext(ctx, "app delete: db_user.drop failed — keeping the panel rows so the account stays visible instead of becoming an orphan",
				"err", err, "db_user", args.DBUserUsername)
		}
	}
	if args.DatabaseID != "" {
		if db, err := deps.Databases.FindByID(ctx, args.DatabaseID); err == nil && db != nil {
			if _, aerr := deps.Agent.Call(ctx, "db.drop", map[string]any{"db_name": db.Name}); aerr != nil {
				dropFailed = true
				slog.ErrorContext(ctx, "app delete: db.drop failed — keeping the panel rows so the database stays visible instead of becoming an orphan",
					"err", aerr, "db_id", args.DatabaseID)
			}
		}
	}

	// The files are gone (app.delete succeeded), so the install row goes either
	// way — deleting it before the databases row also releases the RESTRICT
	// fk_wpinstalls_db.
	deps.Installs.Delete(ctx, args.InstallID)

	if dropFailed {
		return errors.New("database or user drop failed on the host; panel rows kept so they stay visible and retryable")
	}

	if args.DBUserID != "" {
		if grants, err := deps.DatabaseGrants.ListByDatabaseUserID(ctx, args.DBUserID); err == nil {
			for _, g := range grants {
				deps.DatabaseGrants.Delete(ctx, g.ID)
			}
		}
		deps.DatabaseUsers.Delete(ctx, args.DBUserID)
	}
	if args.DatabaseID != "" {
		deps.Databases.Delete(ctx, args.DatabaseID)
	}
	return nil
}
