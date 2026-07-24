// `jabali db` cobra subcommands — list / create / delete user
// databases. M41 operator CLI extension.
//
// Post-refactor: validation + agent dispatch + DB write live in
// panel-api/internal/dbops/. This file is the thin flag-decode +
// output-render wrapper. The REST handler at /api/v1/databases
// calls the same package, so behaviour is identical between
// CLI and REST.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dbops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func dbRepoFromDB() repository.DatabaseRepository {
	return repository.NewDatabaseRepository(sharedDB)
}

func dbopsDeps() dbops.Deps {
	return dbops.Deps{
		Users:          repository.NewUserRepository(sharedDB),
		Packages:       repository.NewPackageRepository(sharedDB),
		Databases:      repository.NewDatabaseRepository(sharedDB),
		ServerSettings: repository.NewServerSettingsRepository(sharedDB),
		Agent:          sharedAgent,
	}
}

func newDBCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "db",
		Aliases: []string{"database"},
		Short:   "Manage user databases (mariadb / postgres)",
	}
	cmd.AddCommand(
		newDBListCmd(),
		newDBCreateCmd(),
		newDBDeleteCmd(),
		newDBUserCmd(),
		newDBPostgresCmd(),
		newDBSSOCmd(),
	)
	registerDBOpsCmds(cmd)
	return cmd
}

func newDBListCmd() *cobra.Command {
	var userLookup string
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List databases (filtered by user, or all)",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			repo := dbRepoFromDB()
			var rows []models.Database
			if userLookup == "" {
				r, _, err := repo.List(ctx, repository.ListOptions{Offset: 0, Limit: 500})
				if err != nil {
					return err
				}
				rows = r
			} else {
				u, err := resolveUser(ctx, userLookup)
				if err != nil {
					return err
				}
				r, _, err := repo.ListByUserID(ctx, u.ID, repository.ListOptions{Offset: 0, Limit: 500})
				if err != nil {
					return err
				}
				rows = r
			}
			if jsonOutput {
				return json.NewEncoder(os.Stdout).Encode(rows)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tENGINE\tUSER_ID\tCREATED")
			for _, r := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
					r.ID, r.Name, r.Engine, r.UserID, r.CreatedAt.Format(time.RFC3339))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&userLookup, "user", "", "Filter by user (email or username)")
	return cmd
}

func newDBCreateCmd() *cobra.Command {
	var userLookup, name, engine string
	var asAdmin bool
	cmd := &cobra.Command{
		Use:     "create",
		Short:   "Create a database for a user",
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			if userLookup == "" || name == "" {
				return errors.New("--user and --name are required")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()

			user, err := resolveUser(ctx, userLookup)
			if err != nil {
				return err
			}
			row, err := dbops.Create(ctx, dbopsDeps(), dbops.CreateInput{
				UserID:  user.ID,
				RawName: name,
				Engine:  engine,
				AsAdmin: asAdmin,
			})
			if err != nil {
				return mapDBopsErr(err)
			}
			cliAuditOK(ctx, "database.create", "database", row.ID, nil)
			fmt.Fprintf(os.Stdout, "Created database %s (id=%s, engine=%s)\n", row.Name, row.ID, row.Engine)
			return nil
		},
	}
	cmd.Flags().StringVar(&userLookup, "user", "", "User (email or username) — required")
	cmd.Flags().StringVar(&name, "name", "", "Database name (without user prefix) — required")
	cmd.Flags().StringVar(&engine, "engine", "mariadb", "Engine: mariadb | postgres")
	cmd.Flags().BoolVar(&asAdmin, "as-admin", false, "Skip the username prefix (admin-only DB names)")
	return cmd
}

func newDBDeleteCmd() *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:     "delete",
		Short:   "Delete a database by ID",
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			if id == "" {
				return errors.New("--id is required")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			if err := dbops.Delete(ctx, dbopsDeps(), dbops.DeleteInput{ID: id}); err != nil {
				return mapDBopsErr(err)
			}
			cliAuditOK(ctx, "database.delete", "database", id, nil)
			fmt.Fprintf(os.Stdout, "Deleted database id=%s\n", id)
			return nil
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "Database ID (ULID)")
	return cmd
}

// mapDBopsErr leaves the wrapped error in place but augments with
// a user-readable suffix for the most common cases. Callers can
// still errors.Is / errors.As against the dbops sentinels.
func mapDBopsErr(err error) error {
	switch {
	case errors.Is(err, dbops.ErrUserNotFound):
		return fmt.Errorf("user not found")
	case errors.Is(err, dbops.ErrUserNoUsername):
		return fmt.Errorf("user has no Linux username — set one before creating databases")
	case errors.Is(err, dbops.ErrPostgresOff):
		return fmt.Errorf("postgres engine disabled — flip server_settings.postgres_enabled=true via admin UI or SQL")
	case errors.Is(err, dbops.ErrQuotaExceeded):
		return err
	case errors.Is(err, dbops.ErrNotFound):
		return fmt.Errorf("database not found")
	default:
		return err
	}
}
