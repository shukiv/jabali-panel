// Log access + streaming CLI (Gitea #569). Operator-side mirror of the
// /logs REST surface (api/logs.go): list the supported log types, mint a
// time-limited log-stream access grant, and revoke one.
//
// Faithful to the handler: same fixed log-type catalog (access/error/goaccess),
// same per-user concurrency cap (5), same 16-byte (32 hex) stream key, same
// 15-minute expiry, same optional per-domain scoping (with ownership check).
// The grant is a pure DB row; the returned WSS URL is the SAME panel-controlled
// stream endpoint the browser consumes (operators can attach with e.g.
// `websocat`). Streaming the bytes from the CLI is intentionally NOT built — the
// transport is the panel WS, identical to the GUI. Mutations CLI-audited (#537).
//
// User-approved (2026-06-30) operator parity for an existing audited endpoint.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/logaccess"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// logTypeCatalog mirrors api/logs.go listTypes (the binding oneof set).
var logTypeCatalog = []struct{ Type, Name, Description string }{
	{"access", "Access Logs", "Nginx access logs showing HTTP requests"},
	{"error", "Error Logs", "Nginx error logs showing server errors"},
	{"goaccess", "GoAccess Report", "Real-time web log analyzer dashboard"},
}

func isValidLogType(t string) bool {
	for _, lt := range logTypeCatalog {
		if lt.Type == t {
			return true
		}
	}
	return false
}

func logAccessRepoFromDB() repository.LogAccessStreamRepository {
	return repository.NewLogAccessStreamRepository(sharedDB)
}

func newLogCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "log",
		Short: "Inspect log types and mint/revoke log-stream access grants",
	}
	cmd.AddCommand(newLogTypesCmd(), newLogAccessCmd())
	return cmd
}

func newLogTypesCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "types",
		Short:   "List the supported log types",
		Args:    cobra.NoArgs,
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if jsonOutput {
				return printJSON(logTypeCatalog)
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "TYPE\tNAME\tDESCRIPTION")
			for _, lt := range logTypeCatalog {
				fmt.Fprintf(w, "%s\t%s\t%s\n", lt.Type, lt.Name, lt.Description)
			}
			return w.Flush()
		},
	}
}

func newLogAccessCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "access",
		Short: "Mint or revoke log-stream access grants",
	}
	cmd.AddCommand(newLogAccessCreateCmd(), newLogAccessRevokeCmd())
	return cmd
}

func newLogAccessCreateCmd() *cobra.Command {
	var user, logType, domainRef string
	cmd := &cobra.Command{
		Use:     "create --user <user> --type <access|error|goaccess> [--domain <domain>]",
		Short:   "Mint a 15-minute log-stream access grant for a user",
		Args:    cobra.NoArgs,
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if user == "" || logType == "" {
				return fmt.Errorf("--user and --type are required")
			}
			if !isValidLogType(logType) {
				return fmt.Errorf("unsupported log type %q (want access|error|goaccess)", logType)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			u, err := resolveUser(ctx, user)
			if err != nil {
				return err
			}
			repo := logAccessRepoFromDB()

			// JAB-303: enforce the SAME grant-scope policy the HTTP handler uses.
			// The beneficiary is --user (u); a nil-domain grant is server-wide, so
			// it is admin-only. Without this an operator could mint a server-wide
			// grant owned by a tenant (cross-tenant / system log disclosure).
			var domainID *string
			domainProvided := domainRef != ""
			domainOwned := false
			if domainProvided {
				dom, derr := resolveDomainCLI(ctx, domainRef)
				if derr != nil {
					return derr
				}
				domainOwned = dom.UserID == u.ID
				domainID = &dom.ID
			}
			if err := logaccess.ValidateGrantScope(u.IsAdmin, domainProvided, domainOwned); err != nil {
				if errors.Is(err, logaccess.ErrDomainNotOwned) {
					return fmt.Errorf("domain %s does not belong to %s", domainRef, u.Email)
				}
				return fmt.Errorf("%w (user %s is not an admin)", err, u.Email)
			}

			// Per-user concurrency cap (5), same as the REST handler.
			if count, cerr := repo.CountByUserID(ctx, u.ID); cerr == nil && count >= 5 {
				return fmt.Errorf("user already has %d active streams (max 5) — revoke one first", count)
			}

			keyBytes := make([]byte, 16) // 32 hex chars
			if _, err := rand.Read(keyBytes); err != nil {
				return fmt.Errorf("rng: %w", err)
			}
			streamKey := hex.EncodeToString(keyBytes)
			expiresAt := time.Now().Add(15 * time.Minute)
			stream := &models.LogAccessStream{
				ID:        ids.NewULID(),
				UserID:    u.ID,
				DomainID:  domainID,
				LogType:   logType,
				StreamKey: streamKey,
				ExpiresAt: expiresAt,
			}
			if err := repo.Create(ctx, stream); err != nil {
				cliAuditErr(ctx, "log_access.create", "log_access_stream", stream.ID, &u.ID)
				return fmt.Errorf("create stream: %w", err)
			}
			cliAuditOK(ctx, "log_access.create", "log_access_stream", stream.ID, &u.ID)

			wsURL := fmt.Sprintf("wss://%s/api/v1/logs/stream/%s", panelHostname(ctx), streamKey)
			if jsonOutput {
				return printJSON(map[string]any{"stream_key": streamKey, "expires_at": expiresAt, "websocket_url": wsURL})
			}
			fmt.Printf("Log-stream grant for %s (%s%s) — SENSITIVE, expires %s:\n  stream_key: %s\n  websocket:  %s\n",
				u.Email, logType, domainSuffix(domainRef), expiresAt.UTC().Format("15:04:05Z"), streamKey, wsURL)
			return nil
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "grant owner (email|username|id, required)")
	cmd.Flags().StringVar(&logType, "type", "", "log type: access|error|goaccess (required)")
	cmd.Flags().StringVar(&domainRef, "domain", "", "scope to one domain (optional)")
	return cmd
}

func domainSuffix(ref string) string {
	if ref == "" {
		return ", server-wide"
	}
	return ", " + ref
}

// panelHostname returns the panel hostname for building the WS URL (the same
// host the browser connects to). Falls back to "localhost" if unavailable.
func panelHostname(ctx context.Context) string {
	if s, err := repository.NewServerSettingsRepository(sharedDB).Get(ctx); err == nil && s != nil && s.Hostname != "" {
		return s.Hostname
	}
	return "localhost"
}

func newLogAccessRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "revoke <stream-key>",
		Short:   "Revoke a log-stream access grant",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			repo := logAccessRepoFromDB()
			stream, err := repo.FindByStreamKey(ctx, args[0])
			if err != nil || stream == nil {
				fmt.Printf("no active grant for that stream key (already revoked or expired)\n")
				return nil
			}
			if err := repo.DeleteByID(ctx, stream.ID); err != nil {
				cliAuditErr(ctx, "log_access.revoke", "log_access_stream", stream.ID, &stream.UserID)
				return fmt.Errorf("revoke stream: %w", err)
			}
			cliAuditOK(ctx, "log_access.revoke", "log_access_stream", stream.ID, &stream.UserID)
			fmt.Printf("revoked log-stream grant %s\n", stream.ID)
			return nil
		},
	}
}
