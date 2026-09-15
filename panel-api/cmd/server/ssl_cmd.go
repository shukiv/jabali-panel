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

// sslEnableIsOperatorLineage reports whether a domain already serves an
// operator-provided certificate that the legacy `ssl enable` door (GH #246,
// "enable ACME") must not overwrite: `custom` (an uploaded cert/key pair) and
// `shared` (a JAB-170 shared certificate). Enabling ACME on such a domain would
// clobber the operator's certificate with a fresh issuance (the 2026-05-09
// LE-clobber class; see internal/reconciler/ssl_san_drift.go) — and for
// `shared`, which the reconciler routes through the `le` path, marking a cert
// row pending would even drive certbot. So the CLI treats enable as a no-op
// there. All other modes (none / self / le / empty) switch to Let's Encrypt.
func sslEnableIsOperatorLineage(mode string) bool {
	return mode == models.SSLModeCustom || mode == models.SSLModeShared
}

// sslDisableRefusal enforces the protected-domain TLS invariants on the legacy
// `ssl disable` door, mirroring the set-mode HTTP door (internal/api/domains.go
// UpdateSSLMode branch): the panel-primary hostname must keep TLS, and a
// mail-enabled domain cannot drop to no-TLS. Before JAB-356 the CLI disable
// only flipped ssl_enabled and left ssl_mode stale, and the reconciler follows
// ssl_mode (GH #246) — so disable was a silent no-op and needed no guard. Now
// that disable writes the authoritative ssl_mode=none and the reconciler
// revokes for real, dropping TLS on these domains would lock them out (the
// #1507 self-signed/HSTS lockout class), so the same invariants must gate here.
func sslDisableRefusal(dom *models.Domain) error {
	if dom.IsPanelPrimary {
		return fmt.Errorf("refusing to disable TLS for %s: the panel hostname must keep TLS", dom.Name)
	}
	if dom.EmailEnabled {
		return fmt.Errorf("refusing to disable TLS for %s: disable mail before removing TLS", dom.Name)
	}
	return nil
}

// sslEnableAlreadyIssued reports whether `ssl enable` is a no-op because the
// domain is already enabled on Let's Encrypt with a valid, comfortably
// unexpired certificate. AC3 (JAB-356): enabling with a valid issued cert must
// be idempotent. The old CLI unconditionally reset the cert row to pending,
// which drives the reconciler to re-run certbot and burns Let's Encrypt's
// duplicate-certificate rate limit for no gain.
//
// The mode MUST be `le`. `ssl disable` writes ssl_mode=none but leaves the cert
// row `issued` (only the reconciler tick revokes it), so a guard keyed on the
// cert status alone would make the next `ssl enable` a wrong-direction no-op —
// the operator asked for TLS on, but it would stay off. `self` (a DNS-01
// CF-Full fallback) must likewise proceed to `le`. Operator lineage
// (custom/shared) is already no-op'd by sslEnableIsOperatorLineage above, so
// `== le` is exactly the already-enabled steady state. The 30-day floor mirrors
// the HTTP enableSSL guard (internal/api/ssl.go — "issued cert with >30d to
// expiry"); hoisting this predicate into models is a follow-up.
func sslEnableAlreadyIssued(mode string, cert *models.SSLCertificate, now time.Time) bool {
	if mode != models.SSLModeLE || cert == nil {
		return false
	}
	if cert.Status != models.SSLStatusIssued || cert.ExpiresAt == nil {
		return false
	}
	return cert.ExpiresAt.Sub(now).Hours()/24 > 30
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
			// JAB-356: `ssl enable` is the legacy "enable ACME" door (GH #246).
			// A domain already on an operator-provided lineage (custom uploaded
			// pair, shared cert) must NOT be switched to le — that would clobber
			// its certificate with a fresh ACME issuance. Return a no-op without
			// touching the mode or marking a cert row pending (for `shared`,
			// which the reconciler routes through the le path, a pending row
			// would even drive certbot), which also avoids the wait-loop below
			// spinning for a Let's Encrypt cert that will never land.
			if sslEnableIsOperatorLineage(dom.SSLMode) {
				cliAuditOK(ctx, "ssl.enable", "domain", dom.ID, &dom.UserID)
				if jsonOutput {
					return printJSON(map[string]any{
						"domain":   dom.Name,
						"ssl_mode": dom.SSLMode,
						"detail":   "already serving an operator-provided certificate; ssl enable (ACME) is a no-op",
						"hint":     fmt.Sprintf("to switch this domain to Let's Encrypt, run `jabali domain set %s --ssl-mode=le` first", dom.Name),
					})
				}
				fmt.Printf("%s is on ssl_mode=%s (operator-provided certificate) — `ssl enable` manages Let's Encrypt and will not overwrite it.\n"+
					"To switch this domain to Let's Encrypt, run `jabali domain set %s --ssl-mode=le` first.\n", dom.Name, dom.SSLMode, dom.Name)
				return nil
			}
			// AC3 (JAB-356): enabling a domain already on Let's Encrypt with a
			// valid issued certificate is idempotent — do NOT reset the cert row
			// to pending (which would drive the reconciler to re-run certbot and
			// burn LE's duplicate-certificate rate limit). Mirrors the HTTP
			// enableSSL idempotency guard (internal/api/ssl.go). Returning here
			// before any write also schedules no convergence for a no-op (AC4:
			// convergence is scheduled only for a real mutation).
			if sslEnableAlreadyIssued(dom.SSLMode, cert, time.Now()) {
				cliAuditOK(ctx, "ssl.enable", "domain", dom.ID, &dom.UserID)
				if jsonOutput {
					out := map[string]any{
						"domain":     dom.Name,
						"ssl_mode":   dom.SSLMode,
						"status":     models.SSLStatusIssued,
						"expires_at": cert.ExpiresAt,
						"detail":     "already enabled with a valid Let's Encrypt certificate; ssl enable is a no-op",
					}
					if cert.CertPath != nil {
						out["cert_path"] = *cert.CertPath
					}
					return printJSON(out)
				}
				fmt.Printf("%s is already enabled with a valid Let's Encrypt certificate (expires %s) — `ssl enable` is a no-op.\n",
					dom.Name, cert.ExpiresAt.Format("2006-01-02"))
				return nil
			}
			// Persist the authoritative TLS mode through the dedicated
			// UpdateSSLMode writer, mirroring the HTTP enableSSL door
			// (internal/api/ssl.go). ssl_mode is authoritative (ADR-0141) but is
			// NOT in the general domainRepo.Update column allowlist, so the old
			// `dom.SSLEnabled = true; Update(dom)` left ssl_mode stale: a domain
			// at ssl_mode=none then `ssl enable` became ssl_enabled=true /
			// ssl_mode=none — contradictory, and the reconciler follows the
			// authoritative mode. UpdateSSLMode writes ssl_mode AND the
			// ssl_enabled shadow (SSLEnabledForMode) atomically.
			if err := domainRepoFromDB().UpdateSSLMode(ctx, dom.ID, models.SSLModeLE); err != nil {
				return fmt.Errorf("update ssl mode: %w", err)
			}
			dom.SSLMode = models.SSLModeLE
			dom.SSLEnabled = true
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
			// JAB-356: enforce the protected-domain TLS invariants BEFORE the
			// authoritative write (mirroring the set-mode HTTP door,
			// internal/api/domains.go). The old disable only flipped ssl_enabled
			// and left ssl_mode stale, so the reconciler (which follows ssl_mode,
			// GH #246) never actually dropped TLS — disable was a silent no-op
			// and this refusal was unnecessary. Now that disable persists
			// ssl_mode=none and the reconciler revokes for real, dropping TLS on
			// the panel hostname or a mail-enabled domain would lock it out
			// (#1507 class), so the same invariants gate this door.
			if err := sslDisableRefusal(dom); err != nil {
				return err
			}
			// Drive TLS off through the authoritative UpdateSSLMode writer,
			// mirroring the HTTP disableSSL door (internal/api/ssl.go). The
			// general Update allowlist drops ssl_mode, so `ssl disable` used to
			// leave the mode stale (e.g. still `le`) while only flipping
			// ssl_enabled. `disable` means remove TLS, so the authoritative mode
			// is `none` (UpdateSSLMode also sets ssl_enabled=false via
			// SSLEnabledForMode). No ACME runs on this door, so there is no
			// custom-lineage clobber.
			if err := domainRepoFromDB().UpdateSSLMode(ctx, dom.ID, models.SSLModeNone); err != nil {
				return fmt.Errorf("update ssl mode: %w", err)
			}
			dom.SSLMode = models.SSLModeNone
			dom.SSLEnabled = false
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
