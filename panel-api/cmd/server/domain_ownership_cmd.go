package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ownershipops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1816 / ADR-0170: `jabali domain ownership …` — the root CLI's view of
// the ownership proof, and the administrator's approve, revoke and policy
// switch. The CLI has no reconciler: the panel applies a change on its next
// reconcile pass.

func newDomainOwnershipCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ownership",
		Short: "Domain ownership proof: status, verify, approve, revoke, pending, policy",
	}
	cmd.AddCommand(
		newDomainOwnershipStatusCmd(),
		newDomainOwnershipVerifyCmd(),
		newDomainOwnershipApproveCmd(),
		newDomainOwnershipRevokeCmd(),
		newDomainOwnershipPendingCmd(),
		newDomainOwnershipPolicyCmd(),
	)
	return cmd
}

// cliOwnershipService builds the ownership service for one CLI command. Its
// own changes (a DNS proof, the parent-rule cascade) go to the audit log
// through newCLIAuditRecorder; the command audits the admin action itself.
func cliOwnershipService() *ownershipops.Service {
	d := ownershipops.Deps{
		Store:    repository.NewDomainOwnershipRepository(sharedDB),
		Domains:  domainRepoFromDB(),
		Aliases:  repository.NewWebDomainAliasRepository(sharedDB),
		Settings: repository.NewServerSettingsRepository(sharedDB),
		Audit:    newCLIAuditRecorder(),
	}
	// Set up by the commands that need the agent (revoke: the mail login
	// cache flush). A nil pointer must not become a non-nil interface.
	if sharedAgent != nil {
		d.Agent = sharedAgent
	}
	return ownershipops.New(d)
}

func fmtOwnershipTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04 MST")
}

// domainOwnershipJSON is the --json shape of one domain's ownership state.
type domainOwnershipJSON struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	UserID         string     `json:"user_id"`
	Status         string     `json:"status"`
	Method         string     `json:"method"`
	ChallengeName  string     `json:"challenge_name"`
	ChallengeValue string     `json:"challenge_value"`
	LastResult     string     `json:"last_result"`
	PendingSince   *time.Time `json:"pending_since,omitempty"`
	VerifiedAt     *time.Time `json:"verified_at,omitempty"`
	CheckedAt      *time.Time `json:"checked_at,omitempty"`
	NextCheckAt    *time.Time `json:"next_check_at,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
}

func domainOwnershipOut(d *models.Domain) domainOwnershipJSON {
	st := d.OwnershipState
	status := st.OwnershipStatus
	if status != models.OwnershipVerified {
		status = models.OwnershipPending
	}
	return domainOwnershipJSON{
		ID: d.ID, Name: d.Name, UserID: d.UserID, Status: status, Method: st.OwnershipMethod,
		ChallengeName:  domainops.OwnershipChallengeName(d.Name),
		ChallengeValue: domainops.OwnershipChallengeValue(st.OwnershipToken),
		LastResult:     st.OwnershipLastResult, PendingSince: st.OwnershipPendingSince,
		VerifiedAt: st.OwnershipVerifiedAt, CheckedAt: st.OwnershipCheckedAt,
		NextCheckAt: st.OwnershipNextCheckAt, ExpiresAt: domainops.DomainOwnershipExpires(d),
	}
}

func printDomainOwnership(d *models.Domain) {
	if jsonOutput {
		_ = printJSON(domainOwnershipOut(d))
		return
	}
	st := d.OwnershipState
	status := st.OwnershipStatus
	if status != models.OwnershipVerified {
		status = models.OwnershipPending
	}
	method := st.OwnershipMethod
	if method == "" {
		method = "-"
	}
	fmt.Printf("Domain:      %s\n", d.Name)
	fmt.Printf("Status:      %s\n", status)
	fmt.Printf("Method:      %s\n", method)
	if status == models.OwnershipVerified {
		fmt.Printf("Verified at: %s\n", fmtOwnershipTime(st.OwnershipVerifiedAt))
		return
	}
	fmt.Printf("Record:      %s TXT \"%s\"\n", domainops.OwnershipChallengeName(d.Name), domainops.OwnershipChallengeValue(st.OwnershipToken))
	last := st.OwnershipLastResult
	if last == "" {
		last = "-"
	}
	fmt.Printf("Last check:  %s (%s)\n", last, fmtOwnershipTime(st.OwnershipCheckedAt))
	fmt.Printf("Next check:  %s\n", fmtOwnershipTime(st.OwnershipNextCheckAt))
	if exp := domainops.DomainOwnershipExpires(d); exp != nil {
		fmt.Printf("Expires:     %s (never-verified names are released)\n", fmtOwnershipTime(exp))
	} else {
		fmt.Printf("Expires:     never (an administrator resolves it)\n")
	}
}

func newDomainOwnershipStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "status <domain-name|domain-id>",
		Short:   "Show a domain's ownership state and the TXT record that proves it",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			d, err := resolveDomainSpec(ctx, domainRepoFromDB(), args[0])
			if err != nil {
				return err
			}
			printDomainOwnership(d)
			return nil
		},
	}
}

func newDomainOwnershipVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "verify <domain-name|domain-id>",
		Short:   "Check a pending domain's TXT record through the public resolvers now",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 45*time.Second)
			defer cancel()
			domains := domainRepoFromDB()
			d, err := resolveDomainSpec(ctx, domains, args[0])
			if err != nil {
				return err
			}
			result, err := cliOwnershipService().CheckDomain(ctx, d)
			if err != nil {
				return fmt.Errorf("check %s: %w", d.Name, err)
			}
			if !jsonOutput {
				fmt.Printf("Result:      %s\n", result)
			}
			if fresh, err := domains.FindByID(ctx, d.ID); err == nil {
				printDomainOwnership(fresh)
			}
			return nil
		},
	}
}

func newDomainOwnershipApproveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "approve <domain-name|domain-id>",
		Short:   "Approve a pending domain as an administrator (audited)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			d, err := resolveDomainSpec(ctx, domainRepoFromDB(), args[0])
			if err != nil {
				return err
			}
			changed, err := cliOwnershipService().ApproveDomain(ctx, d.ID)
			if err != nil {
				cliAuditErr(ctx, "domain.ownership.approve", "domain", d.ID, &d.UserID)
				return fmt.Errorf("approve %s: %w", d.Name, err)
			}
			if !changed {
				return fmt.Errorf("%s is not waiting for proof", d.Name)
			}
			cliAuditOK(ctx, "domain.ownership.approve", "domain", d.ID, &d.UserID)
			fmt.Printf("Domain %s approved. The panel publishes it on its next reconcile pass.\n", d.Name)
			return nil
		},
	}
}

func newDomainOwnershipRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <domain-name|domain-id>",
		Short: "Send a verified domain back to pending as an administrator (audited)",
		Long: "Send a verified domain back to pending. Its mailboxes stop signing in at once; " +
			"its zone, mail and certificate are taken down on the next reconcile pass, and the " +
			"parent-proven domains and aliases under it follow. The panel's own domain and " +
			"docker-app domains are refused.",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			d, err := resolveDomainSpec(ctx, domainRepoFromDB(), args[0])
			if err != nil {
				return err
			}
			changed, err := cliOwnershipService().RevokeDomain(ctx, d.ID)
			switch {
			case errors.Is(err, ownershipops.ErrPanelPrimary), errors.Is(err, ownershipops.ErrDockerAppDomain):
				return err
			case changed:
				cliAuditOK(ctx, "domain.ownership.revoke", "domain", d.ID, &d.UserID)
			}
			if err != nil {
				return fmt.Errorf("revoke %s: some names under it were not updated, run the revoke again: %w", d.Name, err)
			}
			if !changed {
				return fmt.Errorf("%s is already waiting for proof", d.Name)
			}
			fmt.Printf("Domain %s is pending again. The panel takes it offline on its next reconcile pass.\n", d.Name)
			return nil
		},
	}
}

func newDomainOwnershipPendingCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "pending",
		Short:   "List the domains waiting for ownership proof",
		Args:    cobra.NoArgs,
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			rows, err := repository.NewDomainOwnershipRepository(sharedDB).ListPendingDomains(ctx)
			if err != nil {
				return err
			}
			if jsonOutput {
				out := make([]domainOwnershipJSON, 0, len(rows))
				for i := range rows {
					out = append(out, domainOwnershipOut(&rows[i]))
				}
				return printJSON(out)
			}
			if len(rows) == 0 {
				fmt.Println("No domain is waiting for proof.")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tUSER ID\tPENDING SINCE\tLAST RESULT\tEXPIRES")
			for i := range rows {
				d := &rows[i]
				last := d.OwnershipLastResult
				if last == "" {
					last = "-"
				}
				exp := "never"
				if e := domainops.DomainOwnershipExpires(d); e != nil {
					exp = fmtOwnershipTime(e)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", d.Name, d.UserID, fmtOwnershipTime(d.OwnershipPendingSince), last, exp)
			}
			return tw.Flush()
		},
	}
}

func newDomainOwnershipPolicyCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "policy [on|off]",
		Short: "Show or switch whether new domain names need ownership proof (audited)",
		Long: "Without an argument, show the switch. 'off' lets tenants add any name without " +
			"proof, including names that belong to someone else; names added while it is off " +
			"stay live when it is switched back on. 'off' needs --yes.",
		Args:    cobra.MaximumNArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			repo := repository.NewDomainOwnershipRepository(sharedDB)
			if len(args) == 0 {
				s, err := repo.GetSettings(ctx)
				if err != nil {
					return err
				}
				state := "on"
				if !s.RequireProof {
					state = "off"
				}
				fmt.Printf("Ownership proof required: %s\n", state)
				return nil
			}
			var require bool
			switch strings.ToLower(args[0]) {
			case "on":
				require = true
			case "off":
				if !yes {
					return fmt.Errorf("switching ownership proof off lets tenants claim names they do not control; repeat with --yes")
				}
			default:
				return fmt.Errorf("want on or off, got %q", args[0])
			}
			if err := repo.SetRequireProof(ctx, require, "cli", time.Now().UTC()); err != nil {
				cliAuditErr(ctx, "domain.ownership.policy", "server", "domain_ownership", nil)
				return err
			}
			cliAuditOK(ctx, "domain.ownership.policy", "server", "domain_ownership", nil)
			if require {
				fmt.Println("Ownership proof is required for new domain names.")
			} else {
				fmt.Println("Ownership proof is OFF: new domain names are live at once. Domains already waiting for proof stay pending.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm switching ownership proof off")
	return cmd
}
