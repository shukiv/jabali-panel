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
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/sslorigin"
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
		newSSLRetryCmd(),
		newSSLSetCustomCmd(),
		newSSLSharedCmd(),
		newSSLReadinessCmd(),
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
	var noWait bool
	var waitFor time.Duration
	var nginxDir string

	cmd := &cobra.Command{
		Use:   "enable <domain>",
		Short: "Enable SSL for a domain and wait until the vhost serves the real certificate",
		Long: "Marks the domain for issuance and then WAITS until nginx is actually serving the\n" +
			"Let's Encrypt certificate, because issuance and vhost repoint happen on separate\n" +
			"reconciler ticks. Returning at the first tick reports success while the origin is\n" +
			"still self-signed — which is what makes a Cloudflare Full (strict) cutover return\n" +
			"526 (JAB-224). Use --no-wait for the old fire-and-forget behaviour.",
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
			cliAuditOK(ctx, "ssl.enable", "domain", dom.ID, &dom.UserID)

			if noWait {
				if jsonOutput {
					return printJSON(map[string]any{
						"domain": dom.Name,
						"status": models.SSLStatusPending,
						"detail": "marked for issuance; not waiting",
					})
				}
				fmt.Printf("SSL enabled for %s — reconciler will issue cert within ≤60s (not waiting).\n", dom.Name)
				return nil
			}

			// Wait for the END state, not the first tick. Issuance and the vhost
			// repoint land on different reconciler passes, so a command that
			// returns after marking the row reports success while the origin is
			// still self-signed. The check reads the vhost — the file nginx
			// actually serves — rather than the certificate row, because the row
			// says "issued" one tick before the repoint happens.
			if !jsonOutput {
				fmt.Printf("SSL enabled for %s — waiting for nginx to serve the real certificate (up to %s)...\n",
					dom.Name, waitFor)
			}

			deadline := time.Now().Add(waitFor)
			var last sslorigin.Kind
			for {
				o := sslorigin.Classify(dom.Name, nginxDir)
				if o.Kind != last && !jsonOutput {
					fmt.Printf("  origin: %s\n", o.Kind)
					last = o.Kind
				}
				if o.Kind == sslorigin.KindLetsEncrypt {
					if jsonOutput {
						return printJSON(map[string]any{
							"domain": dom.Name, "status": models.SSLStatusIssued,
							"origin": string(o.Kind), "cert_path": o.CertPath,
						})
					}
					fmt.Printf("Done — %s is serving a Let's Encrypt certificate (%s).\n", dom.Name, o.CertPath)
					return nil
				}
				if time.Now().After(deadline) {
					return fmt.Errorf(
						"timed out after %s: %s origin is still %q (%s).\n"+
							"  The domain is marked for issuance, so the reconciler may still complete it —\n"+
							"  re-check with: jabali ssl readiness --all\n"+
							"  HTTP-01 cannot succeed until the domain resolves to THIS host, so if it has\n"+
							"  not cut over yet this is expected, not a failure",
						waitFor, dom.Name, o.Kind, o.Detail)
				}
				time.Sleep(5 * time.Second)
			}
		},
	}
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return as soon as the domain is marked, without waiting for the certificate")
	cmd.Flags().DurationVar(&waitFor, "wait-timeout", 3*time.Minute, "how long to wait for the vhost to serve the real certificate")
	cmd.Flags().StringVar(&nginxDir, "nginx-dir", defaultNginxSitesDir, "directory holding the enabled nginx vhosts")
	return cmd
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
				hint := "wait for the reconciler to finish issuing, or check `jabali ssl list`"
				if cert.Status == models.SSLStatusPendingACMERetry || cert.Status == models.SSLStatusFailed {
					hint = fmt.Sprintf("run `jabali ssl retry %s` to reset the certificate and re-attempt issuance now", dom.Name)
				}
				return fmt.Errorf("cert for %s is in status %q (expected 'issued') — %s", dom.Name, cert.Status, hint)
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

// newSSLRetryCmd unblocks a certificate stuck in 'failed' or 'pending_acme_retry'
// — e.g. a migrated domain that could not pass a challenge until its DNS
// delegation was corrected (GH #1221). It resets the row to pending with a fresh
// retry budget (repo.ResetForRetry); the long-lived reconciler's retry ticker
// picks a 'pending' row up on its next pass and re-attempts ACME. DB-only: this
// is a separate process from the running panel, so it never calls the agent or
// the in-process reconciler directly.
func newSSLRetryCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "retry <domain>",
		Short:   "Reset a stuck cert (failed / pending_acme_retry) and re-attempt ACME issuance now",
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
			cert, cerr := sslRepoFromDB().FindByDomainID(ctx, dom.ID)
			if cerr != nil {
				if errors.Is(cerr, repository.ErrNotFound) {
					return fmt.Errorf("no cert for %s — run `jabali ssl enable %s` first to create + issue", dom.Name, dom.Name)
				}
				return fmt.Errorf("lookup cert: %w", cerr)
			}
			if cert.Status != models.SSLStatusFailed && cert.Status != models.SSLStatusPendingACMERetry {
				return fmt.Errorf("cert for %s is in status %q — retry only applies to 'failed' or 'pending_acme_retry' (an issued cert renews with `jabali ssl renew`)", dom.Name, cert.Status)
			}
			if err := sslRepoFromDB().ResetForRetry(ctx, cert.ID, time.Now().UTC()); err != nil {
				return fmt.Errorf("reset cert for retry: %w", err)
			}
			cliAuditOK(ctx, "ssl.retry", "domain", dom.ID, &dom.UserID)
			if jsonOutput {
				return printJSON(map[string]any{"domain": dom.Name, "status": models.SSLStatusPending, "queued": true})
			}
			fmt.Printf("Reset %s for re-issuance (status → pending, retry budget restored).\n"+
				"The reconciler will attempt ACME within about a minute — watch `jabali ssl list`.\n", dom.Name)
			return nil
		},
	}
}
