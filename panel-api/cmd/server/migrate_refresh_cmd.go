package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
)

// migrate_refresh_cmd.go — GH #646. `jabali migrate refresh` orchestrates the
// destructive re-migration: backup FIRST (abort on failure), then force-mirror
// files (preserving jabali-managed files), drop+reimport the DB, and reconcile.
// Gated behind --force (refuses on a live host without it, like account-restore).

func newMigrateRefreshCmd() *cobra.Command {
	var (
		docroot, dbName, osUser, domain string
		srcDocroot, srcSQL              string
		oldURL, newURL                  string
		force                           bool
	)
	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Force re-pull (refresh) an already-migrated account from a staged source",
		Long: `Overwrites a live migrated account: dest files are mirrored from the
staged source (--delete, preserving wp-config.php + jabali drop-ins) and the
dest DB is dropped + reimported. A hardlink snapshot + DB dump are taken FIRST;
the refresh aborts if that backup fails. Requires --force.`,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !force {
				return fmt.Errorf("refresh overwrites a live account — pass --force to proceed (a pre-overwrite backup is taken automatically)")
			}
			if docroot == "" || dbName == "" || osUser == "" || srcDocroot == "" || srcSQL == "" {
				return fmt.Errorf("--docroot, --db, --os-user, --source-docroot, --source-sql are required")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Minute)
			defer cancel()
			res, err := migrate.RunRefresh(ctx, sharedAgent, migrate.RefreshInput{
				Docroot: docroot, DBName: dbName, OSUser: osUser, Domain: domain,
				SrcDocroot: srcDocroot, SrcSQL: srcSQL, OldURL: oldURL, NewURL: newURL,
			}, time.Now().UTC())
			if err != nil {
				return err
			}
			fmt.Printf("backup ok — snapshot=%s db_dump=%s\n", res.Snapshot, res.DBDump)
			fmt.Printf("(rollback: rsync the snapshot back + `mysql %s < %s`)\n", dbName, res.DBDump)
			fmt.Println("files mirrored, DB reimported, reconciled")
			for _, w := range res.Warnings {
				fmt.Printf("  warning: %s\n", w)
			}
			fmt.Printf("refresh complete. Backup retained at %s (+ %s) — remove once verified.\n", res.Snapshot, res.DBDump)
			return nil
		},
	}
	cmd.Flags().StringVar(&docroot, "docroot", "", "Dest docroot (overwritten)")
	cmd.Flags().StringVar(&dbName, "db", "", "Dest DB name (identity unchanged)")
	cmd.Flags().StringVar(&osUser, "os-user", "", "Dest Linux user")
	cmd.Flags().StringVar(&domain, "domain", "", "Dest domain (for cache purge)")
	cmd.Flags().StringVar(&srcDocroot, "source-docroot", "", "Staged source docroot")
	cmd.Flags().StringVar(&srcSQL, "source-sql", "", "Staged source SQL dump")
	cmd.Flags().StringVar(&oldURL, "old-url", "", "Old site URL (search-replace)")
	cmd.Flags().StringVar(&newURL, "new-url", "", "New site URL (search-replace)")
	cmd.Flags().BoolVar(&force, "force", false, "Required — refresh overwrites a live account")
	return cmd
}
