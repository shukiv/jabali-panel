package main

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/db"
)

func newMigrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Database migration commands",
	}
	cmd.AddCommand(newMigrateUpCmd())
	cmd.AddCommand(newMigrateStatusCmd())
	cmd.AddCommand(newMigrateForceCmd())
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

			// GH #1094/#1103: `jabali update` runs this (via the freshly-installed
			// binary) and rolls the binary back if it fails — so a host stuck on
			// the half-applied migration 264 could never receive the fix. Self-heal
			// that exact state here so the update completes on its own. No-op unless
			// the precise fingerprint matches; anything else falls through to the
			// normal (and, if dirty, still-failing → operator-driven) migrate.
			if recovered, rerr := db.RecoverBrokenFtp264(cfg.Database.URL); rerr != nil {
				fmt.Printf("warning: dirty-264 auto-recovery attempt failed; continuing to migrate: %v\n", rerr)
			} else if recovered {
				fmt.Println("recovered from half-applied migration 264 (GH #1094)")
			}

			if err := db.Migrate(cfg.Database.URL); err != nil {
				return fmt.Errorf("migrate: %w", err)
			}

			fmt.Println("Migrations up-to-date.")
			return nil
		},
	}
}

// newMigrateStatusCmd reports the schema version and, crucially, whether
// golang-migrate thinks a migration is still mid-flight.
//
// Worth its own subcommand because the dirty state takes the panel DOWN:
// panel-api runs migrations at startup, refuses to boot on a dirty schema, and
// systemd restart-loops it. The operator sees a crash loop and an error telling
// them to "Fix and force version" — golang-migrate's phrasing for something the
// CLI previously offered no way to inspect or clear.
func newMigrateStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the schema version and whether it is dirty",
		PreRunE: func(cmd *cobra.Command, args []string) error {
			return initConfig()
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := sharedCfg
			if cfg.Database.URL == "" || cfg.Database.URL == "placeholder-until-phase-3" {
				return fmt.Errorf("DATABASE_URL not configured")
			}
			st, err := db.State(cfg.Database.URL)
			if err != nil {
				return err
			}
			fmt.Printf("version: %d\n", st.Version)
			fmt.Printf("dirty:   %v\n", st.Dirty)
			if st.Dirty {
				fmt.Printf(`
Migration %d started and never recorded completion — the process was killed
part-way (an interrupted 'jabali update' is the usual cause). panel-api runs
migrations at startup and refuses to boot while the schema is dirty, so it is
restart-looping right now.

Before clearing this, check whether migration %d actually applied — read
panel-api/internal/db/migrations/%06d_*.up.sql and verify its columns/tables
are present:

  mysql jabali_panel -e 'SHOW COLUMNS FROM <table>'

  applied in full  ->  jabali migrate force %d      (then restart jabali-panel)
  applied partly   ->  undo its effects by hand first, then force %d

Forcing forward on a half-applied migration skips the rest permanently.
`, st.Version, st.Version, st.Version, st.Version, st.Version-1)
			}
			return nil
		},
	}
}

// newMigrateForceCmd rewrites golang-migrate's bookkeeping to assert the schema
// is at a given version. It runs no SQL of its own.
//
// The version is a required argument rather than inferred from the dirty state
// on purpose: whether to force FORWARD (migration completed) or BACK (it did
// not) is a judgement only someone who has looked at the schema can make, and
// guessing wrong silently leaves the database missing changes.
func newMigrateForceCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "force <version>",
		Short: "Clear the dirty flag by asserting the schema is at <version> (runs no SQL)",
		Args:  cobra.ExactArgs(1),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			return initConfig()
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := sharedCfg
			if cfg.Database.URL == "" || cfg.Database.URL == "placeholder-until-phase-3" {
				return fmt.Errorf("DATABASE_URL not configured")
			}
			v, err := strconv.Atoi(args[0])
			if err != nil || v < 0 {
				return fmt.Errorf("version must be a non-negative integer, got %q", args[0])
			}
			if err := db.ForceVersion(cfg.Database.URL, v); err != nil {
				return err
			}
			fmt.Printf("Schema forced to version %d (dirty flag cleared).\n", v)
			fmt.Println("Restart the panel so it can run any remaining migrations:")
			fmt.Println("  systemctl restart jabali-panel")
			return nil
		},
	}
}
