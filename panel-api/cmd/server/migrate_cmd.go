package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/db"
)

func newMigrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Database migration commands",
	}
	cmd.AddCommand(newMigrateUpCmd())
	// M35 account-import subcommand. Namespaced under `migrate` so
	// the parent `jabali migrate` reads as 'migration verbs' (one
	// schema, one account-import). Different scope from `up` which
	// runs golang-migrate DB-schema migrations.
	cmd.AddCommand(newMigrateImportCmd())
	cmd.AddCommand(newMigrateReapSecretsCmd())
	cmd.AddCommand(newMigratePullSourceCmd())
	cmd.AddCommand(newMigrateImportWPCmd())
	// One-shot offline restore: create job + stage cpmove + run import.
	cmd.AddCommand(newMigrateRestoreCmd())
	cmd.AddCommand(newMigrateRefreshCmd()) // GH #646
	// GH #390 (Google Workspace) / #374 (M365) phase A — per-mailbox IMAP
	// account import (single account or --csv batch). Distinct from the
	// panel-account cpmove sources: each account carries its own remote login.
	cmd.AddCommand(newMigrateImapCmd())
	return cmd
}

func newMigrateUpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "up",
		Short: "Run pending database migrations",
		PreRunE: func(cmd *cobra.Command, args []string) error {
			return initConfig()
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := sharedCfg
			if cfg.Database.URL == "" || cfg.Database.URL == "placeholder-until-phase-3" {
				return fmt.Errorf("DATABASE_URL not configured")
			}

			if err := db.Migrate(cfg.Database.URL); err != nil {
				return fmt.Errorf("migrate: %w", err)
			}

			fmt.Println("Migrations up-to-date.")
			return nil
		},
	}
}
