// `jabali db user` parity subcommands (Gitea #540): rotate-password, and
// grant update / grant revoke. Mirrors the REST handlers' agent dispatch
// (mariadb + postgres) so the CLI reaches operations previously UI/REST-only.
// All mutations are CLI-audited (#537).
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// privsForLevel maps the rw/ro shortcut to the MariaDB privilege list — the
// same mapping the CLI grant-create uses.
func privsForLevel(level string) []string {
	switch level {
	case "rw":
		return []string{"SELECT", "INSERT", "UPDATE", "DELETE", "CREATE", "DROP", "ALTER", "INDEX"}
	case "ro":
		return []string{"SELECT"}
	}
	return nil
}

func newDBUserRotatePasswordCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rotate-password <db-user-id>",
		Short:   "Rotate a database user's password (revealed once)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			repo := dbUserRepoFromDB()
			du, err := repo.FindByID(ctx, args[0])
			if err != nil {
				return fmt.Errorf("find db user: %w", err)
			}
			plain := ids.NewSecret()
			hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
			if err != nil {
				return fmt.Errorf("hash password: %w", err)
			}
			// JAB-285: same engine-correct wire contract as the REST handler.
			// The CLI previously sent `password` for the MariaDB rotate verb,
			// but the agent field is `new_password` (empty → rejected), so CLI
			// MariaDB rotation always failed. The shared helper prevents re-drift.
			verb, params := api.DBUserRotateAgentCommand(du.Engine, du.Username, plain)
			if _, err := sharedAgent.Call(ctx, verb, params); err != nil {
				cliAuditErr(ctx, "db_user.rotate_password", "database_user", du.ID, &du.UserID)
				return fmt.Errorf("agent %s: %w", verb, err)
			}
			if err := repo.UpdatePasswordHash(ctx, du.ID, string(hash)); err != nil {
				return fmt.Errorf("persist password hash: %w", err)
			}
			cliAuditOK(ctx, "db_user.rotate_password", "database_user", du.ID, &du.UserID)
			if jsonOutput {
				return printJSON(map[string]any{"id": du.ID, "username": du.Username, "password": plain})
			}
			fmt.Fprintf(os.Stdout, "Rotated password for %s\nNew password: %s\n(Shown once — store it now.)\n", du.Username, plain)
			return nil
		},
	}
}

func newDBUserGrantRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "revoke <grant-id>",
		Short:   "Revoke a single grant, keeping the database user",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			grants := repository.NewDatabaseUserGrantRepository(sharedDB)
			g, err := grants.FindByID(ctx, args[0])
			if err != nil {
				return fmt.Errorf("find grant: %w", err)
			}
			du, err := dbUserRepoFromDB().FindByID(ctx, g.DatabaseUserID)
			if err != nil {
				return fmt.Errorf("find db user: %w", err)
			}
			db, err := repository.NewDatabaseRepository(sharedDB).FindByID(ctx, g.DatabaseID)
			if err != nil {
				return fmt.Errorf("find database: %w", err)
			}
			verb, params := "db_user.revoke", map[string]any{"db_name": db.Name, "db_user_name": du.Username, "privileges": strings.Split(g.Privileges, ",")}
			if du.Engine == "postgres" {
				verb, params = "db.postgres.revoke", map[string]any{"db_name": db.Name, "role": du.Username}
			}
			if _, err := sharedAgent.Call(ctx, verb, params); err != nil {
				cliAuditErr(ctx, "db_user.grant_revoke", "database_user_grant", g.ID, &du.UserID)
				return fmt.Errorf("agent %s: %w", verb, err)
			}
			if err := grants.Delete(ctx, g.ID); err != nil {
				return fmt.Errorf("delete grant row: %w", err)
			}
			cliAuditOK(ctx, "db_user.grant_revoke", "database_user_grant", g.ID, &du.UserID)
			fmt.Fprintf(os.Stdout, "Revoked grant %s (%s on %s)\n", g.ID, du.Username, db.Name)
			return nil
		},
	}
}

func newDBUserGrantUpdateCmd() *cobra.Command {
	var level string
	cmd := &cobra.Command{
		Use:     "update <grant-id>",
		Short:   "Change a grant's level (rw|ro)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			if level != "rw" && level != "ro" {
				return fmt.Errorf("--level must be 'rw' or 'ro' (got %q)", level)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			grants := repository.NewDatabaseUserGrantRepository(sharedDB)
			g, err := grants.FindByID(ctx, args[0])
			if err != nil {
				return fmt.Errorf("find grant: %w", err)
			}
			du, err := dbUserRepoFromDB().FindByID(ctx, g.DatabaseUserID)
			if err != nil {
				return fmt.Errorf("find db user: %w", err)
			}
			db, err := repository.NewDatabaseRepository(sharedDB).FindByID(ctx, g.DatabaseID)
			if err != nil {
				return fmt.Errorf("find database: %w", err)
			}
			verb, params := "db_user.grant", map[string]any{"db_name": db.Name, "db_user_name": du.Username, "grant_level": level, "privileges": privsForLevel(level)}
			if du.Engine == "postgres" {
				verb, params = "db.postgres.grant", map[string]any{"db_name": db.Name, "role": du.Username}
			}
			if _, err := sharedAgent.Call(ctx, verb, params); err != nil {
				cliAuditErr(ctx, "db_user.grant_update", "database_user_grant", g.ID, &du.UserID)
				return fmt.Errorf("agent %s: %w", verb, err)
			}
			if err := grants.UpdateLevel(ctx, g.ID, level); err != nil {
				return fmt.Errorf("persist grant level: %w", err)
			}
			cliAuditOK(ctx, "db_user.grant_update", "database_user_grant", g.ID, &du.UserID)
			fmt.Fprintf(os.Stdout, "Updated grant %s to %s on %s for %s\n", g.ID, level, db.Name, du.Username)
			return nil
		},
	}
	cmd.Flags().StringVar(&level, "level", "", "new grant level: 'rw' or 'ro' (required)")
	return cmd
}
