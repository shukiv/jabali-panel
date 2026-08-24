// `jabali db user` cobra subcommands — list / create / delete /
// grant database users. M41 operator-CLI extension; closes the
// gap noted in QA-pass: db (database) management was wired but
// db user management wasn't.
//
// Mirrors the REST handler at panel-api/internal/api/database_users.go
// validation: username regex, package quota, prefix logic for
// non-admin users, mariadb + postgres engine dispatch.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

var dbUserNameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,30}$`)

func dbUserRepoFromDB() repository.DatabaseUserRepository {
	return repository.NewDatabaseUserRepository(sharedDB)
}

// newDBUserCmd is wired into newDBCmd's AddCommand list. Adds a
// `user` namespace under `jabali db user`.
func newDBUserCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Manage database users (mariadb / postgres)",
	}
	cmd.AddCommand(
		newDBUserListCmd(),
		newDBUserCreateCmd(),
		newDBUserDeleteCmd(),
		newDBUserGrantCmd(),
		newDBUserRotatePasswordCmd(),
	)
	return cmd
}

func newDBUserListCmd() *cobra.Command {
	var userLookup string
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List database users (filtered by panel user, or all)",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			repo := dbUserRepoFromDB()
			var rows []models.DatabaseUser
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
			fmt.Fprintln(tw, "ID\tUSERNAME\tENGINE\tUSER_ID\tCREATED")
			for _, r := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
					r.ID, r.Username, r.Engine, r.UserID, r.CreatedAt.Format(time.RFC3339))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&userLookup, "user", "", "Filter by panel user (email or username)")
	return cmd
}

func newDBUserCreateCmd() *cobra.Command {
	var userLookup, name, engine, password string
	var asAdmin bool
	cmd := &cobra.Command{
		Use:     "create",
		Short:   "Create a database user (auto-generates password if --password omitted)",
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			if userLookup == "" || name == "" {
				return errors.New("--user and --name are required")
			}
			if engine == "" {
				engine = "mariadb"
			}
			if engine != "mariadb" && engine != "postgres" {
				return fmt.Errorf("--engine must be 'mariadb' or 'postgres' (got %q)", engine)
			}
			if !dbUserNameRe.MatchString(name) {
				return fmt.Errorf("invalid db_user name %q — must match ^[a-zA-Z][a-zA-Z0-9_]{0,30}$", name)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()

			panelUser, err := resolveUser(ctx, userLookup)
			if err != nil {
				return err
			}
			if !asAdmin && (panelUser.Username == nil || *panelUser.Username == "") {
				return fmt.Errorf("user %s has no Linux username — cannot prefix db user name", panelUser.ID)
			}

			finalName := name
			if !asAdmin {
				finalName = *panelUser.Username + "_" + name
			}

			repo := dbUserRepoFromDB()
			exists, err := repo.ExistsByUserAndUsername(ctx, panelUser.ID, finalName)
			if err != nil {
				return fmt.Errorf("collision check: %w", err)
			}
			if exists {
				return fmt.Errorf("db user %q already exists for panel user %s", finalName, *panelUser.Username)
			}

			if engine == "postgres" {
				ssRepo := repository.NewServerSettingsRepository(sharedDB)
				ss, sErr := ssRepo.Get(ctx)
				if sErr != nil {
					return fmt.Errorf("server_settings: %w", sErr)
				}
				if ss == nil || !ss.PostgresEnabled {
					return errors.New("postgres engine requested but server_settings.postgres_enabled=false")
				}
			}

			pw := password
			if pw == "" {
				pw = ids.NewSecret()
			}
			hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
			if err != nil {
				return fmt.Errorf("bcrypt: %w", err)
			}

			if engine == "mariadb" {
				_, err = sharedAgent.Call(ctx, "db_user.create", map[string]any{
					"db_user_name": finalName,
					"password":     pw,
				})
			} else {
				_, err = sharedAgent.Call(ctx, "db.postgres.create_role", map[string]any{
					"role":     finalName,
					"password": pw,
				})
			}
			if err != nil {
				return fmt.Errorf("agent.%s create: %w", engine, err)
			}

			now := time.Now().UTC()
			du := &models.DatabaseUser{
				ID:           ids.NewULID(),
				UserID:       panelUser.ID,
				Username:     finalName,
				Engine:       engine,
				PasswordHash: string(hash),
				CreatedAt:    now,
				UpdatedAt:    now,
			}
			if err := repo.Create(ctx, du); err != nil {
				return fmt.Errorf("db_user row insert: %w", err)
			}
			cliAuditOK(ctx, "db_user.create", "database_user", du.ID, &panelUser.ID)
			fmt.Fprintf(os.Stdout, "Created db user %s (id=%s, engine=%s)\n", finalName, du.ID, engine)
			if password == "" {
				fmt.Fprintf(os.Stdout, "Generated password: %s\n", pw)
				fmt.Fprintln(os.Stdout, "Save it now — not stored in plaintext anywhere.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&userLookup, "user", "", "Panel user (email or username) — required")
	cmd.Flags().StringVar(&name, "name", "", "DB user name (without panel-username prefix) — required")
	cmd.Flags().StringVar(&engine, "engine", "mariadb", "Engine: mariadb | postgres")
	cmd.Flags().StringVar(&password, "password", "", "Password (auto-generated ULID if omitted; revealed once)")
	cmd.Flags().BoolVar(&asAdmin, "as-admin", false, "Skip the panel-username prefix (admin-only DB user names)")
	return cmd
}

func newDBUserDeleteCmd() *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:     "delete",
		Short:   "Delete a database user by ID",
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			if id == "" {
				return errors.New("--id is required")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			repo := dbUserRepoFromDB()
			du, err := repo.FindByID(ctx, id)
			if err != nil {
				return fmt.Errorf("find: %w", err)
			}
			cmdName := "db_user.drop"
			if du.Engine == "postgres" {
				cmdName = "db.postgres.drop_role"
			}
			if _, err := sharedAgent.Call(ctx, cmdName, map[string]any{
				"db_user_name": du.Username,
				"role":         du.Username, // postgres path uses "role"
			}); err != nil {
				return fmt.Errorf("agent.%s: %w", cmdName, err)
			}
			if err := repo.Delete(ctx, du.ID); err != nil {
				return fmt.Errorf("delete row: %w", err)
			}
			cliAuditOK(ctx, "db_user.delete", "database_user", du.ID, &du.UserID)
			fmt.Fprintf(os.Stdout, "Deleted db user %s (engine=%s)\n", du.Username, du.Engine)
			return nil
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "DB user ID (ULID)")
	return cmd
}

func newDBUserGrantCmd() *cobra.Command {
	var dbUserID, dbName, level string
	var privileges []string
	cmd := &cobra.Command{
		Use:   "grant",
		Short: "Grant a db user privileges on a database",
		Long: `Grants the database user the given privileges on the named
database. --level is a shortcut for common patterns:
  rw  → SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, ALTER, INDEX
  ro  → SELECT
--privileges takes precedence when both are passed.

Mariadb-only in v1; postgres grants land via panel UI / admin REST.`,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			if dbUserID == "" || dbName == "" {
				return errors.New("--db-user-id and --db-name are required")
			}
			if len(privileges) == 0 && level == "" {
				return errors.New("--privileges or --level required")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			g, du, err := dbUserGrantCreate(ctx, dbGrantDeps{
				agent:     sharedAgent,
				dbUsers:   dbUserRepoFromDB(),
				databases: repository.NewDatabaseRepository(sharedDB),
				grants:    repository.NewDatabaseUserGrantRepository(sharedDB),
			}, dbUserID, dbName, privileges, level)
			if err != nil {
				return err
			}
			cliAuditOK(ctx, "db_user.grant", "database_user_grant", g.ID, &du.UserID)
			if jsonOutput {
				return printJSON(g)
			}
			fmt.Fprintf(os.Stdout, "Granted %s on %s to %s (grant id=%s)\n", g.Privileges, dbName, du.Username, g.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&dbUserID, "db-user-id", "", "DB user ID (ULID) — required")
	cmd.Flags().StringVar(&dbName, "db-name", "", "Database name (with panel-prefix) — required")
	cmd.Flags().StringVar(&level, "level", "", "Shortcut: 'rw' or 'ro' (alternative to --privileges)")
	cmd.Flags().StringSliceVar(&privileges, "privileges", nil, "MariaDB privilege list (e.g. SELECT,INSERT,UPDATE)")
	cmd.AddCommand(newDBUserGrantUpdateCmd(), newDBUserGrantRevokeCmd())
	return cmd
}

// dbGrantDeps is the narrow slice of collaborators dbUserGrantCreate needs,
// so the cobra RunE stays a thin wire-up over globals and the logic is
// unit-testable with fakes.
type dbGrantDeps struct {
	agent     agent.AgentInterface
	dbUsers   repository.DatabaseUserRepository
	databases repository.DatabaseRepository
	grants    repository.DatabaseUserGrantRepository
}

// dbUserGrantCreate applies a MariaDB grant to the engine AND persists the
// matching database_user_grants desired-state row (JAB-284). Before this, the
// CLI applied the engine grant with no row, so the grant was invisible to the
// reconciler, the backup metadata builder, and the delete-cascade — and, having
// no row, could not even be revoked via `db user grant revoke <id>`.
//
// Parity with the REST handler: same canonical privilege set (api.CanonicalDBGrant),
// duplicate guard, and host-grant compensation on a persist failure.
func dbUserGrantCreate(ctx context.Context, d dbGrantDeps, dbUserID, dbName string, privileges []string, level string) (*models.DatabaseUserGrant, *models.DatabaseUser, error) {
	du, err := d.dbUsers.FindByID(ctx, dbUserID)
	if err != nil {
		return nil, nil, fmt.Errorf("find db user: %w", err)
	}
	if du.Engine != "mariadb" {
		return nil, nil, fmt.Errorf("grant CLI only supports mariadb in v1; got %q (use admin REST/UI for postgres)", du.Engine)
	}
	canonical, computedLevel, err := api.CanonicalDBGrant(privileges, level)
	if err != nil {
		return nil, nil, err
	}

	// Owner-scoped resolution: match by exact name within the db-user's owner's
	// databases. This makes the CLI strictly same-owner — narrower than the
	// admin REST path, which may grant cross-user; cross-user grants stay
	// REST/UI-only. It also validates the database exists + is owned.
	dbs, _, err := d.databases.ListByUserID(ctx, du.UserID, repository.ListOptions{Limit: 1000})
	if err != nil {
		return nil, nil, fmt.Errorf("list databases: %w", err)
	}
	var db *models.Database
	for i := range dbs {
		if dbs[i].Name == dbName {
			db = &dbs[i]
			break
		}
	}
	if db == nil {
		return nil, nil, fmt.Errorf("database %q not found for this db user's owner (cross-user grants are REST/UI-only)", dbName)
	}

	// Duplicate guard BEFORE the agent call: a retry must error cleanly rather
	// than re-grant and then have Create's unique-key failure compensate away a
	// live grant. (A grant orphaned before this fix — engine yes, row no — slips
	// past here, the re-grant no-ops, and Create adopts the orphan into desired
	// state: the migration path for existing damage.)
	if existing, _ := d.grants.FindByDBAndDBUser(ctx, db.ID, du.ID); existing != nil {
		return nil, nil, fmt.Errorf("grant already exists for %s on %s (id=%s)", du.Username, db.Name, existing.ID)
	}

	if _, err := d.agent.Call(ctx, "db_user.grant", map[string]any{
		"db_name":      db.Name,
		"db_user_name": du.Username,
		"privileges":   strings.Split(canonical, ","),
	}); err != nil {
		return nil, nil, fmt.Errorf("agent db_user.grant: %w", err)
	}

	now := time.Now().UTC()
	g := &models.DatabaseUserGrant{
		ID:             ids.NewULID(),
		DatabaseID:     db.ID,
		DatabaseUserID: du.ID,
		GrantLevel:     computedLevel,
		Privileges:     canonical,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := d.grants.Create(ctx, g); err != nil {
		// Compensate: revoke the host grant we just applied so we never leave an
		// engine grant with no desired-state row — the exact bug this closes.
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, rerr := d.agent.Call(rctx, "db_user.revoke", map[string]any{
			"db_name":      db.Name,
			"db_user_name": du.Username,
			"privileges":   strings.Split(canonical, ","),
		})
		cancel()
		if rerr != nil {
			return nil, nil, fmt.Errorf("persist grant row failed (%v) AND the compensating revoke ALSO failed (%v) — an engine grant for %s on %s is now orphaned; revoke it directly on the DB engine", err, rerr, du.Username, db.Name)
		}
		return nil, nil, fmt.Errorf("persist grant row: %w (host grant rolled back)", err)
	}
	return g, du, nil
}
