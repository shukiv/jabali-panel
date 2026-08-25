// mailbox_extras_cmd.go — M6.5 CLI surfaces for autoresponder + forwarder.
// Mirrors the HTTP handlers in panel-api/internal/api/mailbox_*.go but
// goes direct DB + agent so operators can drive the panel from a script
// without a Kratos session. Same lifecycle: panel writes the row, the
// reconciler converges Stalwart on the next tick; this file additionally
// fires a best-effort inline agent call so the change is visible without
// waiting for the next reconcile.

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/autoresponderops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// optStr returns nil for an empty flag value, else a pointer to it — so an
// omitted --subject/--body reaches autoresponderops as a nil (absent) field,
// not an empty string that would masquerade as content.
func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ---- repo helpers ---------------------------------------------------------

// applyForwardersCLI pushes a mailbox's full forwarder state to Stalwart
// via forwarder.apply — the same convergence the HTTP handler does. The CLI
// previously poked domain.email_apply, which does NOT converge forwarders,
// so CLI-created aliases/forwards never reached Stalwart (GH #237).
func applyForwardersCLI(ctx context.Context, mbID, mailboxEmail string) {
	rows, _, err := forwarderRepoFromDB().ListByMailboxID(ctx, mbID, repository.ListOptions{Limit: 500})
	if err != nil {
		return
	}
	aliases := []map[string]string{}
	externals := []map[string]any{}
	for _, f := range rows {
		if !f.Enabled {
			continue
		}
		switch f.Type {
		case "alias":
			if f.LocalPart != nil {
				aliases = append(aliases, map[string]string{"local_part": *f.LocalPart})
			}
		case "external":
			externals = append(externals, map[string]any{"target": f.Target, "keep_copy": f.KeepCopy})
		}
	}
	notifyAgentMailbox(ctx, "forwarder.apply", map[string]any{
		"mailbox_email": mailboxEmail,
		"aliases":       aliases,
		"externals":     externals,
	})
}

func forwarderRepoFromDB() repository.EmailForwarderRepository {
	return repository.NewEmailForwarderRepository(sharedDB)
}

func autoresponderRepoFromDB() repository.EmailAutoresponderRepository {
	return repository.NewEmailAutoresponderRepository(sharedDB)
}

// findMailboxByEmailCLI is the lookup that every M6.5 subcommand needs.
// Returns the mailbox row + the parent domain (autoresponder/forwarder
// agent payloads need the domain name for routing).
func findMailboxByEmailCLI(ctx context.Context, email string) (*models.Mailbox, *models.Domain, error) {
	mb, err := mailboxRepoFromDB().FindByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, nil, fmt.Errorf("mailbox %s not found", email)
		}
		return nil, nil, fmt.Errorf("lookup mailbox: %w", err)
	}
	dom, err := domainRepoFromDB().FindByID(ctx, mb.DomainID)
	if err != nil {
		return nil, nil, fmt.Errorf("lookup domain: %w", err)
	}
	return mb, dom, nil
}

// ---- autoresponder --------------------------------------------------------

func newMailboxAutoresponderCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "autoresponder",
		Short: "Manage per-mailbox vacation responders",
	}
	cmd.AddCommand(
		newMailboxAutoresponderSetCmd(),
		newMailboxAutoresponderClearCmd(),
		newMailboxAutoresponderShowCmd(),
	)
	return cmd
}

func newMailboxAutoresponderSetCmd() *cobra.Command {
	var (
		subject  string
		body     string
		htmlBody string
		from     string
		to       string
	)
	cmd := &cobra.Command{
		Use:     "set <email>",
		Short:   "Enable an autoresponder for a mailbox",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			email := args[0]
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()

			mb, dom, err := findMailboxByEmailCLI(ctx, email)
			if err != nil {
				return err
			}

			// Flag-specific hints for the operator; the shared lifecycle
			// (autoresponderops.Set) is the actual enforcement backstop, so CLI
			// and REST reject the same intake — this just phrases it in terms of
			// the flags the operator typed.
			if subject == "" {
				return fmt.Errorf("--subject is required")
			}
			if body == "" && htmlBody == "" {
				return fmt.Errorf("at least one of --body or --html-body is required")
			}

			// The date-range rule is enforced centrally too (from must be <= to).
			var fromT, toT *time.Time
			if from != "" {
				t, err := time.Parse(time.RFC3339, from)
				if err != nil {
					return fmt.Errorf("--from must be RFC3339 (e.g. 2026-05-01T00:00:00Z): %w", err)
				}
				fromT = &t
			}
			if to != "" {
				t, err := time.Parse(time.RFC3339, to)
				if err != nil {
					return fmt.Errorf("--to must be RFC3339: %w", err)
				}
				toT = &t
			}
			subjP, bodyP, htmlP := optStr(subject), optStr(body), optStr(htmlBody)

			// callAgentMailbox returns the agent error so Set can turn a failed
			// push into a warning while keeping the DB the desired-state truth.
			push := func(pctx context.Context, cmd string, params map[string]any) error {
				_, err := callAgentMailbox(pctx, cmd, params)
				return err
			}
			_, warning, err := autoresponderops.Set(ctx,
				autoresponderops.Deps{Autoresponders: autoresponderRepoFromDB()},
				autoresponderops.SetInput{
					MailboxID:    mb.ID,
					MailboxEmail: mb.LocalPart + "@" + dom.Name,
					Enabled:      true,
					Subject:      subjP,
					TextBody:     bodyP,
					HTMLBody:     htmlP,
					FromDate:     fromT,
					ToDate:       toT,
				}, push)
			if err != nil {
				return fmt.Errorf("set autoresponder: %w", err)
			}

			if jsonOutput {
				return printJSON(map[string]any{
					"mailbox": email,
					"enabled": true,
					"subject": subject,
				})
			}
			cliAuditOK(ctx, "mailbox.autoresponder_set", "mailbox", email, nil)
			fmt.Printf("Autoresponder enabled for %s\n", email)
			if warning != "" {
				fmt.Printf("Note: %s\n", warning)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&subject, "subject", "", "Subject line (required)")
	cmd.Flags().StringVar(&body, "body", "", "Plain text body (optional if --html-body set)")
	cmd.Flags().StringVar(&htmlBody, "html-body", "", "HTML body (optional)")
	cmd.Flags().StringVar(&from, "from", "", "Start date (RFC3339, e.g. 2026-05-01T00:00:00Z)")
	cmd.Flags().StringVar(&to, "to", "", "End date (RFC3339)")
	return cmd
}

func newMailboxAutoresponderClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "clear <email>",
		Short:   "Disable + delete the autoresponder for a mailbox",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			email := args[0]
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()

			mb, dom, err := findMailboxByEmailCLI(ctx, email)
			if err != nil {
				return err
			}
			push := func(pctx context.Context, cmd string, params map[string]any) error {
				_, err := callAgentMailbox(pctx, cmd, params)
				return err
			}
			if err := autoresponderops.Clear(ctx,
				autoresponderops.Deps{Autoresponders: autoresponderRepoFromDB()},
				mb.ID, mb.LocalPart+"@"+dom.Name, push); err != nil {
				return fmt.Errorf("clear autoresponder: %w", err)
			}
			cliAuditOK(ctx, "mailbox.autoresponder_clear", "mailbox", email, nil)
			fmt.Printf("Autoresponder cleared for %s\n", email)
			return nil
		},
	}
}

func newMailboxAutoresponderShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "show <email>",
		Short:   "Print the current autoresponder for a mailbox",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			email := args[0]
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			mb, _, err := findMailboxByEmailCLI(ctx, email)
			if err != nil {
				return err
			}
			ar, err := autoresponderRepoFromDB().FindByMailboxID(ctx, mb.ID)
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					if jsonOutput {
						return printJSON(map[string]any{"mailbox": email, "enabled": false})
					}
					fmt.Printf("No autoresponder set for %s\n", email)
					return nil
				}
				return fmt.Errorf("lookup autoresponder: %w", err)
			}
			if jsonOutput {
				return printJSON(ar)
			}
			fmt.Printf("Mailbox:   %s\n", email)
			fmt.Printf("Enabled:   %v\n", ar.Enabled)
			if ar.Subject != nil {
				fmt.Printf("Subject:   %s\n", *ar.Subject)
			}
			if ar.FromDate != nil {
				fmt.Printf("From:      %s\n", ar.FromDate.UTC().Format(time.RFC3339))
			}
			if ar.ToDate != nil {
				fmt.Printf("To:        %s\n", ar.ToDate.UTC().Format(time.RFC3339))
			}
			if ar.TextBody != nil {
				fmt.Printf("Text body: %s\n", *ar.TextBody)
			}
			if ar.HTMLBody != nil {
				fmt.Printf("HTML body: %d bytes\n", len(*ar.HTMLBody))
			}
			return nil
		},
	}
}

// ---- forwarder ------------------------------------------------------------

func newMailboxForwarderCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "forwarder",
		Short: "Manage per-mailbox aliases + external forwards",
	}
	cmd.AddCommand(
		newMailboxForwarderAddCmd(),
		newMailboxForwarderListCmd(),
		newMailboxForwarderRemoveCmd(),
	)
	return cmd
}

func newMailboxForwarderAddCmd() *cobra.Command {
	var (
		fwdType   string
		localPart string
		target    string
		keepCopy  bool
	)
	cmd := &cobra.Command{
		Use:     "add <email>",
		Short:   "Add an alias or external forwarder to a mailbox",
		Long:    `Type 'alias' delivers <local>@<domain> mail to <email>. Type 'external' forwards <email> to <target>.`,
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			email := args[0]
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()

			mb, dom, err := findMailboxByEmailCLI(ctx, email)
			if err != nil {
				return err
			}
			if fwdType != "alias" && fwdType != "external" {
				return fmt.Errorf("--type must be 'alias' or 'external'")
			}
			if fwdType == "alias" && localPart == "" {
				return fmt.Errorf("--local is required for alias")
			}
			if fwdType == "external" && target == "" {
				return fmt.Errorf("--target is required for external")
			}

			f := &models.EmailForwarder{
				ID:        ids.NewULID(),
				MailboxID: &mb.ID,
				DomainID:  dom.ID,
				Type:      fwdType,
				Enabled:   true,
				ManagedBy: "m6.5",
			}
			if fwdType == "alias" {
				lp := localPart
				f.LocalPart = &lp
				// Alias target = the alias's OWN address, matching the HTTP
				// handler. It had regressed to the mailbox address, so a
				// mailbox's 2nd alias collided on uq_external_forward and
				// failed (GH #280 / JAB-319). Shared helper = no re-drift.
				f.Target = models.AliasForwarderTarget(localPart, dom.Name)
			} else {
				f.Target = target
				f.KeepCopy = keepCopy
			}
			if err := forwarderRepoFromDB().Create(ctx, f); err != nil {
				return fmt.Errorf("create forwarder: %w", err)
			}
			applyForwardersCLI(ctx, mb.ID, mb.LocalPart+"@"+dom.Name)
			if jsonOutput {
				return printJSON(f)
			}
			cliAuditOK(ctx, "mailbox.forwarder_add", "forwarder", f.ID, nil)
			fmt.Printf("Forwarder %s added (id=%s)\n", fwdType, f.ID)
			if fwdType == "alias" {
				fmt.Printf("  %s@%s -> %s\n", localPart, dom.Name, f.Target)
			} else {
				fmt.Printf("  %s -> %s\n", email, target)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&fwdType, "type", "", "alias | external (required)")
	cmd.Flags().StringVar(&localPart, "local", "", "Alias local part (required for type=alias)")
	cmd.Flags().StringVar(&target, "target", "", "External destination email (required for type=external)")
	cmd.Flags().BoolVar(&keepCopy, "keep-copy", false, "type=external: keep a copy in the mailbox (Sieve redirect :copy)")
	_ = cmd.MarkFlagRequired("type")
	return cmd
}

func newMailboxForwarderListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list <email>",
		Short:   "List forwarders attached to a mailbox",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			email := args[0]
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			mb, dom, err := findMailboxByEmailCLI(ctx, email)
			if err != nil {
				return err
			}
			rows, _, err := forwarderRepoFromDB().ListByMailboxID(ctx, mb.ID, repository.ListOptions{Limit: 200})
			if err != nil {
				return fmt.Errorf("list forwarders: %w", err)
			}
			if jsonOutput {
				return printJSON(map[string]any{"mailbox": email, "forwarders": rows, "total": len(rows)})
			}
			if len(rows) == 0 {
				fmt.Printf("No forwarders for %s\n", email)
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tTYPE\tFROM\tTO\tENABLED")
			for _, f := range rows {
				lp := ""
				if f.LocalPart != nil {
					lp = *f.LocalPart
				}
				from := f.Target
				if f.Type == "alias" {
					from = lp + "@" + dom.Name
				} else {
					from = email
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%v\n", f.ID, f.Type, from, f.Target, f.Enabled)
			}
			return w.Flush()
		},
	}
}

func newMailboxForwarderRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove <forwarder-id>",
		Short:   "Delete a forwarder by ID (find via 'forwarder list')",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			f, err := forwarderRepoFromDB().FindByID(ctx, id)
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return fmt.Errorf("forwarder %s not found", id)
				}
				return fmt.Errorf("lookup forwarder: %w", err)
			}
			dom, err := domainRepoFromDB().FindByID(ctx, f.DomainID)
			if err != nil {
				return fmt.Errorf("lookup domain: %w", err)
			}
			if err := forwarderRepoFromDB().Delete(ctx, id); err != nil {
				return fmt.Errorf("delete forwarder: %w", err)
			}
			if f.MailboxID != nil {
				if mb, merrr := mailboxRepoFromDB().FindByID(ctx, *f.MailboxID); merrr == nil {
					applyForwardersCLI(ctx, mb.ID, mb.LocalPart+"@"+dom.Name)
				}
			}
			cliAuditOK(ctx, "mailbox.forwarder_delete", "forwarder", id, nil)
			fmt.Printf("Forwarder %s deleted\n", id)
			return nil
		},
	}
}
