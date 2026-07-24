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

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func sslRepoFromDB() repository.SSLCertificateRepository {
	return repository.NewSSLCertificateRepository(sharedDB)
}

func newSSLCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ssl",
		Short: "Manage Let's Encrypt SSL certificates",
	}
	cmd.AddCommand(
		newSSLListCmd(),
		newSSLEnableCmd(),
		newSSLDisableCmd(),
		newSSLRenewCmd(),
		newSSLSetCustomCmd(),
		newSSLSharedCmd(),
	)
	return cmd
}

func newSSLListCmd() *cobra.Command {
	var userLookup string
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List SSL certificates (optionally filtered by user)",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			repo := sslRepoFromDB()
			var rows []repository.SSLCertificateWithDomain
			var err error
			if userLookup != "" {
				u, uerr := resolveUser(ctx, userLookup)
				if uerr != nil {
					return uerr
				}
				rows, err = repo.ListByUserID(ctx, u.ID)
			} else {
				rows, err = repo.ListAll(ctx)
			}
			if err != nil {
				return fmt.Errorf("list certs: %w", err)
			}
			if jsonOutput {
				return printJSON(map[string]any{"certificates": rows, "total": len(rows)})
			}
			if len(rows) == 0 {
				fmt.Println("No SSL certificates.")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "DOMAIN\tUSER\tSTATUS\tEXPIRES\tSTAGING\tRENEWED")
			for _, c := range rows {
				exp := "-"
				if c.ExpiresAt != nil {
					exp = c.ExpiresAt.Format("2006-01-02")
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\n",
					c.DomainName, c.UserUsername, c.Status, exp, boolYN(c.Staging), c.RenewalCount)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&userLookup, "user", "", "filter by user (id|email|username)")
	return cmd
}

func newSSLEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "enable <domain>",
		Short:   "Enable SSL for a domain (reconciler will issue cert within ≤60s)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			dom, err := domainRepoFromDB().FindByName(ctx, args[0])
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return fmt.Errorf("domain %q not found", args[0])
				}
				return fmt.Errorf("lookup domain: %w", err)
			}
			repo := sslRepoFromDB()
			cert, err := repo.FindByDomainID(ctx, dom.ID)
			if err != nil && !errors.Is(err, repository.ErrNotFound) {
				return fmt.Errorf("lookup cert: %w", err)
			}
			dom.SSLEnabled = true
			if err := domainRepoFromDB().Update(ctx, dom); err != nil {
				return fmt.Errorf("update domain: %w", err)
			}
			if cert == nil {
				cert = &models.SSLCertificate{
					ID:       ids.NewULID(),
					DomainID: dom.ID,
					Status:   models.SSLStatusPending,
				}
				if err := repo.Create(ctx, cert); err != nil {
					return fmt.Errorf("create cert row: %w", err)
				}
			} else {
				if err := repo.UpdateStatus(ctx, cert.ID, models.SSLStatusPending, nil); err != nil {
					return fmt.Errorf("update cert status: %w", err)
				}
			}
			if jsonOutput {
				return printJSON(map[string]any{
					"domain": dom.Name,
					"status": models.SSLStatusPending,
					"detail": "reconciler tick will issue cert",
				})
			}
			cliAuditOK(ctx, "ssl.enable", "domain", dom.ID, &dom.UserID)
			fmt.Printf("SSL enabled for %s — reconciler will issue cert within ≤60s.\n", dom.Name)
			return nil
		},
	}
}

func newSSLDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "disable <domain>",
		Short:   "Disable SSL for a domain (reconciler will revoke cert)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			dom, err := domainRepoFromDB().FindByName(ctx, args[0])
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return fmt.Errorf("domain %q not found", args[0])
				}
				return fmt.Errorf("lookup domain: %w", err)
			}
			dom.SSLEnabled = false
			if err := domainRepoFromDB().Update(ctx, dom); err != nil {
				return fmt.Errorf("update domain: %w", err)
			}
			if jsonOutput {
				return printJSON(map[string]any{"domain": dom.Name, "ssl_enabled": false})
			}
			cliAuditOK(ctx, "ssl.disable", "domain", dom.ID, &dom.UserID)
			fmt.Printf("SSL disabled for %s — reconciler will revoke + clean up.\n", dom.Name)
			return nil
		},
	}
}

func newSSLRenewCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:     "renew <domain>",
		Short:   "Renew SSL cert via certbot (synchronous, calls agent)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			dom, err := domainRepoFromDB().FindByName(ctx, args[0])
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return fmt.Errorf("domain %q not found", args[0])
				}
				return fmt.Errorf("lookup domain: %w", err)
			}
			cert, cerr := sslRepoFromDB().FindByDomainID(ctx, dom.ID)
			if cerr != nil && !errors.Is(cerr, repository.ErrNotFound) {
				return fmt.Errorf("lookup cert: %w", cerr)
			}
			if cert == nil {
				return fmt.Errorf("no cert for %s — run `jabali ssl enable %s` first to create + issue", dom.Name, dom.Name)
			}
			if cert.Status != models.SSLStatusIssued && cert.Status != models.SSLStatusRenewing {
				return fmt.Errorf("cert for %s is in status %q (expected 'issued') — wait for reconciler to finish issuing or check `jabali ssl list`", dom.Name, cert.Status)
			}
			raw, err := sharedAgent.Call(ctx, "ssl.renew", map[string]any{
				"domain": dom.Name,
				"force":  force,
			})
			if err != nil {
				return fmt.Errorf("ssl.renew: %w", err)
			}
			var resp struct {
				CertPath  string `json:"cert_path"`
				KeyPath   string `json:"key_path"`
				IssuedAt  string `json:"issued_at"`
				ExpiresAt string `json:"expires_at"`
				Skipped   bool   `json:"skipped"`
			}
			_ = json.Unmarshal(raw, &resp)
			if jsonOutput {
				return printJSON(resp)
			}
			if resp.Skipped {
				fmt.Printf("Renewal skipped for %s (cert not yet within renewal window — use --force to override).\n", dom.Name)
				return nil
			}
			cliAuditOK(ctx, "ssl.renew", "domain", dom.ID, &dom.UserID)
			fmt.Printf("Renewed %s\n  cert:    %s\n  key:     %s\n  issued:  %s\n  expires: %s\n",
				dom.Name, resp.CertPath, resp.KeyPath, resp.IssuedAt, resp.ExpiresAt)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "force renewal even if cert is not due")
	return cmd
}
