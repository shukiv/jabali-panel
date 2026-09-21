package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dbconsoleops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/logger"
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

			// Audit to STDERR, never sharedLog (which is stdout): stdout carries
			// the login URL / --json payload, and an audit line there would
			// corrupt the machine-readable result. Same config/format as the
			// server so the two are uniform in the journal.
			auditLog := logger.New(sharedCfg.Log, os.Stderr)

			db, err := dbRepoFromDB().FindByID(ctx, dbID)
			if err != nil {
				auditCLIIssuance(auditLog, "", dbID, "", "", "db_not_found")
				return fmt.Errorf("database %q not found", dbID)
			}
			// Normalize engine to canonical form (empty→mariadb). All adapters use
			// the same normalization (JAB-348).
			engine := dbconsoleops.NormalizeEngine(db.Engine)

			// --engine is an optional guard: it must match the database's real
			// engine (you can't open a postgres database in phpMyAdmin).
			if e := strings.TrimSpace(engineFlag); e != "" && e != engine {
				auditCLIIssuance(auditLog, db.UserID, db.ID, engine, "", "engine_mismatch")
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
			adminer := sso.NewAdminerService(base, repository.NewAdminerSSOTokenRepository(sharedDB))

			// Provision shadow account for the database owner via the unified
			// engine dispatch leaf (JAB-348). This ensures mariadb and postgres
			// paths are identical across CLI and REST adapters.
			if err := dbconsoleops.EnsureShadowForEngine(ctx, engine, db.UserID, base, adminer); err != nil {
				if errors.Is(err, dbconsoleops.ErrInvalidEngine) {
					auditCLIIssuance(auditLog, db.UserID, db.ID, engine, "", "unknown_engine")
					return fmt.Errorf("unsupported engine %q", engine)
				}
				auditCLIIssuance(auditLog, db.UserID, db.ID, engine, "", "ensure_shadow_fail")
				return fmt.Errorf("ensure shadow account: %w", err)
			}

			var loginURL, hashPrefix string
			switch engine {
			case "mariadb":
				token, err := base.MintToken(ctx, db.UserID, db.ID, db.Name)
				if err != nil {
					auditCLIIssuance(auditLog, db.UserID, db.ID, engine, "", "mint_fail")
					return fmt.Errorf("mint token: %w", err)
				}
				hashPrefix = dbconsoleops.TokenAuditPrefix(token)
				loginURL = dbconsoleops.PhpMyAdminRedirect(phpMyAdminBaseURLForCLI(), token, db.Name)
			case "postgres":
				token, err := adminer.MintAdminerToken(ctx, db.UserID, db.ID, "postgres")
				if err != nil {
					auditCLIIssuance(auditLog, db.UserID, db.ID, engine, "", "mint_fail")
					return fmt.Errorf("mint token: %w", err)
				}
				hashPrefix = dbconsoleops.TokenAuditPrefix(token)
				loginURL = dbconsoleops.AdminerRedirect(adminerBaseURLForCLI(), token, db.Name, "postgres")
			}

			// Audit the successful issuance — hash-prefix only, never the token —
			// so a CLI-minted SSO handoff is auditable in the journal like the
			// REST doors (JAB-348 AC5). The token still leaves via stdout; that
			// is the deliverable, not an audit record.
			auditCLIIssuance(auditLog, db.UserID, db.ID, engine, hashPrefix, "issued")

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

// auditCLIIssuance emits a structured audit line for a CLI-driven DB-console SSO
// issuance, mirroring the REST doors' `sso_adminer` / `sso_phpmyadmin` shape
// (msg + user_id / database_id / engine / token_hash_prefix / outcome) so the
// journal can correlate a CLI mint with its validate/unauthorized counterparts.
// It carries the token hash-prefix (dbconsoleops.TokenAuditPrefix), never the
// token itself; hashPrefix is empty on the failure paths, which run before a
// token exists (JAB-348 AC5).
func auditCLIIssuance(log *slog.Logger, userID, databaseID, engine, hashPrefix, outcome string) {
	if log == nil {
		return
	}
	log.Info("sso_cli",
		"user_id", userID,
		"database_id", databaseID,
		"engine", engine,
		"token_hash_prefix", hashPrefix,
		"outcome", outcome,
	)
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
