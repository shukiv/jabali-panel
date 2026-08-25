package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// `jabali mailbox` groups the M6 mailbox-management subcommands. The
// same five surfaces as the admin UI (list / create / delete / set-quota
// / passwd) plus no UDS indirection — this is direct DB + agent from
// inside the panel binary, so CLI runs work without a browser Kratos
// session.
//
// All subcommands resolve --domain by name OR ID (ULID). Name form is
// the primary UX; ID form exists for scripts that pipe
// `jabali domain list --json` output.
func newMailboxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mailbox",
		Short: "Manage mailboxes (M6 Email via Stalwart)",
		Long: `Per-domain mailbox CRUD + password rotation. Requires email to
be enabled on the target domain — use "jabali domain email-enable" first.

All subcommands bypass HTTP auth (direct DB + agent), so they only run
for operators with access to the panel's config and UDS.`,
	}
	cmd.AddCommand(
		newMailboxListCmd(),
		newMailboxCreateCmd(),
		newMailboxDeleteCmd(),
		newMailboxSetQuotaCmd(),
		newMailboxPasswdCmd(),
		newMailboxAutoresponderCmd(),
		newMailboxForwarderCmd(),
		newMailboxSharesCmd(),
	)
	return cmd
}

// ---- list ----

func newMailboxListCmd() *cobra.Command {
	var domainSpec string
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List mailboxes in a domain",
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()

			dom, err := resolveDomainSpec(ctx, domainRepoFromDB(), domainSpec)
			if err != nil {
				return err
			}
			rows, err := listMailboxesDirect(ctx, mailboxRepoFromDB(), dom.ID)
			if err != nil {
				return err
			}
			if jsonOutput {
				return printJSON(map[string]any{
					"domain":    dom.Name,
					"mailboxes": rows,
					"total":     len(rows),
				})
			}
			if len(rows) == 0 {
				fmt.Printf("No mailboxes in %s\n", dom.Name)
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "EMAIL\tQUOTA\tUSED\tDISABLED\tCREATED")
			for _, mb := range rows {
				disabled := "no"
				if mb.IsDisabled {
					disabled = "yes"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					mb.EmailCached,
					fmtBytesHuman(mb.QuotaBytes),
					fmtBytesHuman(mb.LastUsageBytes),
					disabled,
					mb.CreatedAt.UTC().Format("2006-01-02"),
				)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&domainSpec, "domain", "", "Domain name or ID (required)")
	_ = cmd.MarkFlagRequired("domain")
	return cmd
}

// ---- create ----

func newMailboxCreateCmd() *cobra.Command {
	var (
		domainSpec  string
		localPart   string
		password    string
		quotaMB     uint64
		displayName string
		sendOnly    bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a mailbox (password shown once if auto-generated)",
		Long: `Creates a mailbox in the named domain. Omit --password to have a
strong one generated and printed once — it is NOT stored in plaintext
and cannot be recovered.`,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()

			dom, err := resolveDomainSpec(ctx, domainRepoFromDB(), domainSpec)
			if err != nil {
				return err
			}
			quotaBytes := quotaMB * 1024 * 1024
			mb, generatedPassword, err := createMailboxDirect(ctx, mailboxRepoFromDB(), notifyAgentMailbox, ssoKeyForCLI(), dom, localPart, password, quotaBytes, displayName, sendOnly)
			if err != nil {
				return err
			}

			if jsonOutput {
				return printJSON(map[string]any{
					"id":          mb.ID,
					"email":       mb.EmailCached,
					"quota_bytes": mb.QuotaBytes,
					"password":    generatedPassword, // empty when caller supplied
				})
			}
			cliAuditOK(ctx, "mailbox.create", "mailbox", mb.EmailCached, &dom.UserID)
			fmt.Printf("Created %s (quota %s)\n", mb.EmailCached, fmtBytesHuman(mb.QuotaBytes))
			if generatedPassword != "" {
				fmt.Printf("Password: %s\n", generatedPassword)
				fmt.Println("(This is shown once — copy it now.)")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&domainSpec, "domain", "", "Domain name or ID (required)")
	cmd.Flags().StringVar(&localPart, "local", "", "Local part, e.g. \"alice\" (required)")
	cmd.Flags().StringVar(&password, "password", "", "Explicit password (omit to auto-generate)")
	cmd.Flags().Uint64Var(&quotaMB, "quota-mb", 0, "Disk quota in MiB (default 1024)")
	cmd.Flags().StringVar(&displayName, "display-name", "", "Human-readable display name (GH #197)")
	cmd.Flags().BoolVar(&sendOnly, "send-only", false, "SMTP-submission only — authenticates to send but never receives (GH #371)")
	_ = cmd.MarkFlagRequired("domain")
	_ = cmd.MarkFlagRequired("local")
	return cmd
}

// ---- delete ----

func newMailboxDeleteCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:     "delete <email>",
		Short:   "Delete a mailbox (agent destroys Stalwart account first)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			email := args[0]
			if !force {
				fmt.Printf("Delete mailbox %s? (type 'yes' to confirm): ", email)
				var confirm string
				_, _ = fmt.Scanln(&confirm)
				if confirm != "yes" {
					fmt.Println("Cancelled")
					return nil
				}
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			if err := deleteMailboxDirect(ctx, mailboxRepoFromDB(), callAgentMailbox, email); err != nil {
				return err
			}
			cliAuditOK(ctx, "mailbox.delete", "mailbox", email, nil)
			fmt.Printf("Mailbox %s deleted\n", email)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt")
	return cmd
}

// ---- set-quota ----

func newMailboxSetQuotaCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "set-quota <email> <mb>",
		Short:   "Update a mailbox disk quota (in MiB)",
		Args:    cobra.ExactArgs(2),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			email := args[0]
			mb, err := parseUint64(args[1])
			if err != nil {
				return fmt.Errorf("invalid quota %q: %w", args[1], err)
			}
			quotaBytes := mb * 1024 * 1024
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			row, err := setMailboxQuotaDirect(ctx, mailboxRepoFromDB(), notifyAgentMailbox, email, quotaBytes)
			if err != nil {
				return err
			}
			fmt.Printf("%s quota set to %s\n", row.EmailCached, fmtBytesHuman(row.QuotaBytes))
			return nil
		},
	}
}

// ---- passwd ----

func newMailboxPasswdCmd() *cobra.Command {
	var password string
	cmd := &cobra.Command{
		Use:   "passwd <email>",
		Short: "Rotate a mailbox password (auto-generated and shown once if --password omitted)",
		Args:  cobra.ExactArgs(1),
		Long: `Generates a new strong password (or uses --password) and stores the
bcrypt hash. Stalwart's SqlDirectory re-reads on every auth, so the
change takes effect immediately — no daemon reload needed.`,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			email := args[0]
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			generated, err := rotateMailboxPasswordDirect(ctx, mailboxRepoFromDB(), notifyAgentMailbox, ssoKeyForCLI(), email, password)
			if err != nil {
				return err
			}
			if jsonOutput {
				return printJSON(map[string]any{
					"email":    email,
					"password": generated,
				})
			}
			if generated != "" {
				fmt.Printf("New password for %s: %s\n", email, generated)
				fmt.Println("(This is shown once — copy it now.)")
			} else {
				fmt.Printf("Password rotated for %s\n", email)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&password, "password", "", "Explicit new password (omit to auto-generate)")
	return cmd
}

// ---- small helpers local to this file ----

// parseUint64 wraps strconv so each caller doesn't have to import it.
func parseUint64(s string) (uint64, error) {
	var v uint64
	_, err := fmt.Sscanf(s, "%d", &v)
	if err != nil {
		return 0, err
	}
	return v, nil
}

// fmtBytesHuman prints MiB / GiB for mailbox quotas and usage. Keeps
// output compact for list views.
func fmtBytesHuman(b uint64) string {
	const (
		KiB = 1024
		MiB = 1024 * KiB
		GiB = 1024 * MiB
	)
	switch {
	case b == 0:
		return "0"
	case b >= GiB:
		return fmt.Sprintf("%.1f GiB", float64(b)/float64(GiB))
	case b >= MiB:
		return fmt.Sprintf("%d MiB", b/MiB)
	default:
		return fmt.Sprintf("%d B", b)
	}
}
