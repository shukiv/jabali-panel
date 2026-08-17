package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
)

// session_cmd.go — `jabali session list/revoke/revoke-user` (JAB-120). Headless
// equivalent of the admin Sessions page: audit + kill interactive Kratos login
// sessions during an incident or when the GUI is unavailable. Backed by the same
// kratosclient the API uses (ListActiveSessions / RevokeSession /
// RevokeIdentitySessions over the Kratos admin socket).

func cliKratos() (*kratosclient.Client, error) {
	if sharedCfg == nil {
		return nil, fmt.Errorf("config not loaded")
	}
	return kratosclient.NewClient(sharedCfg.Auth.Kratos.PublicURL, sharedCfg.Auth.Kratos.AdminURL), nil
}

func newSessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Audit and revoke active login sessions (Kratos)",
	}
	cmd.AddCommand(newSessionListCmd(), newSessionRevokeCmd(), newSessionRevokeUserCmd())
	return cmd
}

func newSessionListCmd() *cobra.Command {
	var userFilter string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List active login sessions (optionally filtered by --user email)",
		// requireConfig: cliKratos() needs sharedCfg for the Kratos admin URL.
		// Nothing loads config globally (no root PersistentPreRun), so without
		// this the command fails with "config not loaded" (JAB-272 sibling).
		PreRunE: requireConfig,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
			defer cancel()
			kc, err := cliKratos()
			if err != nil {
				return err
			}
			sessions, err := kc.ListActiveSessions(ctx)
			if err != nil {
				return fmt.Errorf("list sessions: %w", err)
			}
			filter := strings.ToLower(strings.TrimSpace(userFilter))

			type row struct {
				ID, Email, Username, IP, AAL, AuthAt string
			}
			var rows []row
			for _, s := range sessions {
				email, username := "", ""
				if s.Identity != nil {
					email = s.Identity.GetTraitEmail()
					username = s.Identity.GetTraitUsername()
				}
				if filter != "" && strings.ToLower(email) != filter {
					continue
				}
				ip := ""
				if len(s.Devices) > 0 {
					ip = s.Devices[0].IPAddress
				}
				rows = append(rows, row{s.ID, email, username, ip, s.AAL, s.AuthenticatedAt})
			}

			if jsonOutput {
				return printJSON(map[string]interface{}{"sessions": rows, "total": len(rows)})
			}
			if len(rows) == 0 {
				fmt.Println("no active sessions")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "SESSION ID\tEMAIL\tUSERNAME\tIP\tAAL\tAUTHENTICATED")
			for _, r := range rows {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Email, r.Username, r.IP, r.AAL, r.AuthAt)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&userFilter, "user", "", "only show sessions for this user email")
	return cmd
}

func newSessionRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <session-id>",
		Short: "Revoke a single login session",
		Args:  cobra.ExactArgs(1),
		// requireConfig: only Kratos is touched (no DB), but sharedCfg must be
		// loaded first or cliKratos() returns "config not loaded" (JAB-272).
		PreRunE: requireConfig,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
			defer cancel()
			kc, err := cliKratos()
			if err != nil {
				return err
			}
			if err := kc.RevokeSession(ctx, args[0]); err != nil {
				return fmt.Errorf("revoke session: %w", err)
			}
			fmt.Printf("revoked session %s\n", args[0])
			return nil
		},
	}
}

func newSessionRevokeUserCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke-user <email-or-id>",
		Short: "Revoke ALL login sessions for a user (sign out everywhere)",
		Args:  cobra.ExactArgs(1),
		// requireDB: resolveUser() hits userRepo() on the shared DB — without it
		// the command SIGSEGVs on a nil *gorm.DB (the same JAB-272 trap as
		// `user suspend`). requireDB also loads config, so cliKratos() works.
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
			defer cancel()

			ident := args[0]
			u, err := resolveUser(ctx, ident)
			if err != nil || u == nil {
				return fmt.Errorf("user %q not found", ident)
			}
			if u.KratosIdentityID == nil || *u.KratosIdentityID == "" {
				return fmt.Errorf("user %q has no linked Kratos identity", ident)
			}

			kc, err := cliKratos()
			if err != nil {
				return err
			}
			if err := kc.RevokeIdentitySessions(ctx, *u.KratosIdentityID); err != nil {
				return fmt.Errorf("revoke user sessions: %w", err)
			}
			fmt.Printf("revoked all sessions for user %s\n", ident)
			return nil
		},
	}
}
