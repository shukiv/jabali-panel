package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/sso"
)

// jabali db sso — mint a single-use phpMyAdmin/Adminer SSO login URL for a
// database (JAB-132). The GUI/API already expose this (POST /sso/phpmyadmin,
// POST /sso/adminer); this closes the CLI gap for automation. The token is
// minted for the database's OWNER and is single-use with a 5-minute TTL.

func newDBSSOCmd() *cobra.Command {
	var dbID, engineFlag string
	cmd := &cobra.Command{
		Use:   "sso --database <id> [--engine mariadb|postgres]",
		Short: "Mint a single-use phpMyAdmin/Adminer SSO login URL for a database",
		Long: "Mints a one-time DB-console login URL for the database's owner.\n" +
			"A mariadb database opens in phpMyAdmin; a postgres database opens in Adminer.\n" +
			"The URL is single-use and expires in 5 minutes.",
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			dbID = strings.TrimSpace(dbID)
			if dbID == "" {
				return fmt.Errorf("--database <id> is required")
			}
			key := ssoKeyForCLI()
			if key == nil {
				return fmt.Errorf("SSO signing key not configured (%s); cannot mint a token",
					ssoKeyPathForMessage())
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()

			db, err := dbRepoFromDB().FindByID(ctx, dbID)
			if err != nil {
				return fmt.Errorf("database %q not found", dbID)
			}
			engine := strings.TrimSpace(db.Engine)
			if engine == "" {
				engine = "mariadb"
			}
			// --engine is an optional guard: it must match the database's real
			// engine (you can't open a postgres database in phpMyAdmin).
			if e := strings.TrimSpace(engineFlag); e != "" && e != engine {
				return fmt.Errorf("database %s is %q, not %q", db.Name, engine, e)
			}

			base := sso.NewService(
				sharedDB,
				repository.NewUserRepository(sharedDB),
				repository.NewPhpMyAdminSSOTokenRepository(sharedDB),
				sharedAgent,
				key,
				sharedLog,
			)

			var loginURL string
			switch engine {
			case "mariadb":
				if err := base.EnsureShadow(ctx, db.UserID); err != nil {
					return fmt.Errorf("ensure phpMyAdmin shadow account: %w", err)
				}
				token, err := base.MintToken(ctx, db.UserID, db.ID, db.Name)
				if err != nil {
					return fmt.Errorf("mint token: %w", err)
				}
				q := url.Values{}
				q.Set("token", token)
				q.Set("db", db.Name)
				loginURL = phpMyAdminBaseURLForCLI() + "/phpmyadmin/sso.php?" + q.Encode()
			case "postgres":
				adminer := sso.NewAdminerService(base, repository.NewAdminerSSOTokenRepository(sharedDB))
				if err := adminer.EnsurePgShadow(ctx, db.UserID); err != nil {
					return fmt.Errorf("ensure Adminer PostgreSQL shadow role: %w", err)
				}
				token, err := adminer.MintAdminerToken(ctx, db.UserID, db.ID, "postgres")
				if err != nil {
					return fmt.Errorf("mint token: %w", err)
				}
				q := url.Values{}
				q.Set("token", token)
				loginURL = adminerBaseURLForCLI() + "/jabali-adminer/?" + q.Encode()
			default:
				return fmt.Errorf("unsupported engine %q", engine)
			}

			if jsonOutput {
				return printJSON(map[string]string{
					"database": db.Name, "engine": engine, "login_url": loginURL,
				})
			}
			fmt.Printf("Single-use %s login for %s (expires in 5 minutes):\n%s\n",
				consoleName(engine), db.Name, loginURL)
			return nil
		},
	}
	cmd.Flags().StringVar(&dbID, "database", "", "database id (ULID)")
	cmd.Flags().StringVar(&engineFlag, "engine", "", "optional engine guard: mariadb|postgres (defaults to the database's engine)")
	return cmd
}

func consoleName(engine string) string {
	if engine == "postgres" {
		return "Adminer"
	}
	return "phpMyAdmin"
}

// phpMyAdminBaseURLForCLI mirrors the HTTP handler's base-URL resolution for a
// non-browser caller: the explicit config override, else https://<hostname>.
func phpMyAdminBaseURLForCLI() string {
	if sharedCfg != nil && sharedCfg.SSO.PhpMyAdminBaseURL != "" {
		return strings.TrimSuffix(sharedCfg.SSO.PhpMyAdminBaseURL, "/")
	}
	return panelHTTPSBaseForCLI()
}

func adminerBaseURLForCLI() string {
	if sharedCfg != nil && sharedCfg.SSO.AdminerBaseURL != "" {
		return strings.TrimSuffix(sharedCfg.SSO.AdminerBaseURL, "/")
	}
	return panelHTTPSBaseForCLI()
}

func panelHTTPSBaseForCLI() string {
	host := ""
	if sharedCfg != nil {
		host = strings.TrimSpace(sharedCfg.Server.Hostname)
	}
	if host == "" {
		host = "localhost"
	}
	return "https://" + host
}

func ssoKeyPathForMessage() string {
	if sharedCfg != nil && sharedCfg.SSO.KeyPath != "" {
		return sharedCfg.SSO.KeyPath
	}
	return "/etc/jabali-panel/sso.key"
}
