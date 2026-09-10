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
	"log/slog"
	"os"
	"strings"
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
		// Delete-path collaborators (JAB-275) — the CLI delete now runs the
		// same attachment refusal + grant teardown the REST handler does.
		DatabaseGrants: repository.NewDatabaseUserGrantRepository(sharedDB),
		DatabaseUsers:  repository.NewDatabaseUserRepository(sharedDB),
		Installs:       repository.NewApplicationInstallRepository(sharedDB),
		Agent:          sharedAgent,
		Log:            slog.Default(),
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
		newDBReassignCmd(),
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

// newDBReassignCmd is the CLI parity for the REST admin change-of-owner
// (POST /admin/databases/:id/chown, GH #1619). Both call the same
// dbops.ReassignDatabaseOwner, so the refusal rules — shared DB user, prefix
// mismatch, name collision, postgres-unsupported — are identical between CLI
// and REST (GH #1609). Modelled on the sibling `domain chown` verb.
func newDBReassignCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "chown <database> <new-owner>",
		Short: "Reassign a database to a different owner (GH #1609)",
		Long: `Reassign a database — and the DB users bound exclusively to it — to a
different tenant, renaming them onto the new owner's prefix and re-granting
access. Mirrors POST /admin/databases/:id/chown; both call the same
dbops.ReassignDatabaseOwner.

The move is refused when it would cross a hardening boundary: a DB user shared
with another database or owner, a name not under the current owner's prefix, or a
name collision under the new owner. Postgres databases are not supported yet.`,
		Args:    cobra.ExactArgs(2),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			// The move fans out a rename of the database plus each exclusively
			// bound DB user and its grants through the agent, so it uses the same
			// 5-minute ceiling the REST handler and the domain chown CLI use — a
			// 60s cancel mid-run can leave the box half-moved.
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()

			db, err := resolveDatabaseCLI(ctx, args[0])
			if err != nil {
				return err
			}
			newOwner, err := resolveUser(ctx, args[1])
			if err != nil {
				return err
			}
			// Capture the current owner before the move, for the failure-audit
			// subject (their resource was the target).
			oldOwnerID := db.UserID

			if !yes {
				ok, cerr := confirm(fmt.Sprintf(
					"Reassign database %q to %q? This renames the database and its dedicated users onto the new owner's prefix and re-grants access.",
					db.Name, args[1]))
				if cerr != nil {
					return cerr
				}
				if !ok {
					fmt.Println("Aborted.")
					return nil
				}
			}

			res, err := dbops.ReassignDatabaseOwner(ctx, dbopsDeps(), dbops.ReassignInput{
				DatabaseID: db.ID,
				NewOwnerID: newOwner.ID,
			})
			if err != nil {
				cliAuditErr(ctx, "database.chown", "database", db.ID, &oldOwnerID)
				return mapDBopsErr(err)
			}
			// Success subject = NEW owner: the CLI audit row has no meta column for
			// new_owner_id (unlike the REST auditDBChown), so the subject encodes
			// who received the database.
			cliAuditOK(ctx, "database.chown", "database", db.ID, &newOwner.ID)

			if jsonOutput {
				return json.NewEncoder(os.Stdout).Encode(res)
			}
			fmt.Fprintf(os.Stdout, "Reassigned database to %q (new name %s).\n", *newOwner.Username, res.NewName)
			if len(res.RenamedUsers) > 0 {
				fmt.Fprintf(os.Stdout, "Renamed DB users: %s\n", strings.Join(res.RenamedUsers, ", "))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
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
	case errors.Is(err, dbops.ErrAttached):
		var attached *dbops.AttachedError
		if errors.As(err, &attached) {
			return fmt.Errorf("database is attached to application install %s — delete the app first", attached.InstallID)
		}
		return fmt.Errorf("database is attached to an application install — delete the app first")
	case errors.Is(err, dbops.ErrReassignSameOwner):
		return fmt.Errorf("database is already owned by that user")
	case errors.Is(err, dbops.ErrReassignOwnerInvalid):
		return fmt.Errorf("new owner must be a fully-provisioned tenant (needs a Linux username and an active package)")
	case errors.Is(err, dbops.ErrReassignEngine):
		return fmt.Errorf("reassigning a postgres database isn't supported yet")
	case errors.Is(err, dbops.ErrReassignPrefix):
		return fmt.Errorf("a name is not under the current owner's prefix — needs manual review")
	case errors.Is(err, dbops.ErrReassignSharedUser):
		return fmt.Errorf("a database user is shared with another database or owner — detach it first")
	case errors.Is(err, dbops.ErrReassignCollision):
		return fmt.Errorf("a name already exists under the new owner")
	default:
		return err
	}
}
