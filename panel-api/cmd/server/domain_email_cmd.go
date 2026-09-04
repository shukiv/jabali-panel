package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainmailops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// domain_email_cmd.go adds the M6 domain email subcommands to the existing
// `jabali domain` group (wired from newDomainCmd()).
//
// The enable/disable lifecycle — agent provisioning (domain.email_enable/
// _disable), DKIM-output validation, DB state persistence, and the M6 DNS
// sync — lives in internal/domainmailops, the same module the REST handler
// (internal/api/domain_email.go) calls. Routing both adapters through it means
// the orchestration can't drift. This file owns CLI concerns only: flag/arg
// parsing, output, and audit.
//
// The CLI wires no SSL deps (Deps.SSLCerts/SSLReconciler stay nil): a
// short-lived CLI process has no running reconciler to schedule, and
// ReconcileSSLSANDrift adds the mail SANs to the domain's cert on its next
// pass regardless — a latency difference, not a correctness gap. This
// preserves the CLI's prior behavior (it never performed the cert-row flip).

// newDomainEmailDepsFromGlobals builds the shared-module deps from the CLI's
// process globals. Call is the mailbox_ops agent caller (same sharedAgent +
// timeout); its signature already matches domainmailops.CallFunc.
func newDomainEmailDepsFromGlobals() domainmailops.Deps {
	return domainmailops.Deps{
		Domains:        domainRepoFromDB(),
		DNSZones:       repository.NewDNSZoneRepository(sharedDB),
		DNSRecords:     repository.NewDNSRecordRepository(sharedDB),
		ServerSettings: repository.NewServerSettingsRepository(sharedDB),
		Call:           callAgentMailbox,
	}
}

// domainEmailSubcommands returns the email-* leaves that belong under
// `jabali domain`. Called from newDomainCmd() so ordering stays with the
// other domain subcommands.
func domainEmailSubcommands() []*cobra.Command {
	return []*cobra.Command{
		newDomainEmailEnableCmd(),
		newDomainEmailDisableCmd(),
		newDomainEmailDKIMRotateCmd(),
	}
}

func newDomainEmailEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "email-enable <domain-name-or-id>",
		Short: "Enable email for a domain (generates DKIM + publishes DNS records)",
		Args:  cobra.ExactArgs(1),
		Long: `Flips email_enabled on the domain, runs the agent's domain.email_enable
command to generate an Ed25519 DKIM keypair and register the domain in
Stalwart, then publishes the M6-managed DNS records (DKIM, autoconfig,
autodiscover) into the panel's DNS zone.

Idempotent — calling it twice is harmless.`,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 45*time.Second)
			defer cancel()
			deps := newDomainEmailDepsFromGlobals()
			dom, err := resolveDomainSpec(ctx, deps.Domains, args[0])
			if err != nil {
				return err
			}
			selector, pubKey, warnings, err := domainmailops.Enable(ctx, deps, dom)
			if err != nil {
				return err
			}
			if jsonOutput {
				return printJSON(map[string]any{
					"domain_name":     dom.Name,
					"email_enabled":   true,
					"dkim_selector":   selector,
					"dkim_public_key": pubKey,
					"warnings":        warnings,
				})
			}
			cliAuditOK(ctx, "domain.email_enable", "domain", dom.ID, &dom.UserID)
			fmt.Printf("Email enabled for %s\n", dom.Name)
			fmt.Printf("DKIM selector:   %s\n", selector)
			fmt.Printf("DKIM public key: %s\n", pubKey)
			for _, w := range warnings {
				fmt.Printf("warning: %s\n", w)
			}
			return nil
		},
	}
}

func newDomainEmailDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "email-disable <domain-name-or-id>",
		Short: "Disable email for a domain (keeps DKIM key per ADR-0043)",
		Args:  cobra.ExactArgs(1),
		Long: `Flips email_enabled off, reloads Stalwart, and removes the M6-managed
DNS records. The DKIM private key is preserved so a later re-enable
doesn't re-roll the key and invalidate cached DKIM signatures at
downstream receivers (ADR-0043).`,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			deps := newDomainEmailDepsFromGlobals()
			dom, err := resolveDomainSpec(ctx, deps.Domains, args[0])
			if err != nil {
				return err
			}
			if err := domainmailops.Disable(ctx, deps, dom); err != nil {
				return err
			}
			cliAuditOK(ctx, "domain.email_disable", "domain", dom.ID, &dom.UserID)
			fmt.Printf("Email disabled for %s\n", dom.Name)
			return nil
		},
	}
}
