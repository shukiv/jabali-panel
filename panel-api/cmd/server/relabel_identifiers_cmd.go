package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// newAdminRelabelIdentifiersCmd implements the M54 Wave-C Kratos re-key. After
// the v2 identity schema (username = password identifier) is deployed and
// Kratos reloaded, every existing identity still has its password identifier
// keyed on email (derived under the old schema). This walks every identity and
// PATCHes its username trait, which makes Kratos re-derive
// credentials.password.identifiers from the new schema — flipping the login
// identifier email -> username, password hash preserved (proven, M54 spike
// 2026-06-10).
//
// Idempotent + dry-run by default. The username comes from the identity's own
// username trait if present, else the panel user row matched by
// kratos_identity_id (backfilled by `admin backfill-usernames`). An identity
// with no resolvable username is SKIPPED, never relabelled to empty — that
// would leave it with no login identifier and lock it out.
func newAdminRelabelIdentifiersCmd() *cobra.Command {
	var apply bool
	cmd := &cobra.Command{
		Use:   "relabel-identifiers",
		Short: "Re-key Kratos login identifiers email -> username",
		Long: `Re-keys every Kratos identity's password login identifier from email to
username, by PATCHing the username trait so Kratos re-derives the identifier
under the deployed v2 schema. Run AFTER the v2 schema is live and AFTER
'admin backfill-usernames --apply'. Dry-run by default; --apply to mutate.`,
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()

			kcfg := sharedCfg.Auth.Kratos
			if kcfg.AdminURL == "" {
				return fmt.Errorf("auth.kratos.admin_url not configured — cannot relabel without admin API access")
			}
			kc := kratosclient.NewClient(kcfg.PublicURL, kcfg.AdminURL)
			users := repository.NewUserRepository(sharedDB)

			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "IDENTITY ID\tEMAIL\t-> USERNAME\tACTION")

			var relabelled, skipped, failed int
			var pageToken string
			for {
				page, next, err := kc.ListIdentitiesPage(ctx, 250, pageToken)
				if err != nil {
					return fmt.Errorf("list identities: %w", err)
				}
				for _, id := range page {
					username := id.Traits.Username
					if username == "" {
						// Fall back to the panel user row keyed on this identity.
						if u, uerr := users.FindByKratosIdentityID(ctx, id.ID); uerr == nil && u.Username != nil {
							username = *u.Username
						}
					}
					if username == "" {
						fmt.Fprintf(tw, "%s\t%s\t(none)\tSKIP (no username — would lock out)\n", id.ID, id.Traits.Email)
						skipped++
						continue
					}
					if !apply {
						fmt.Fprintf(tw, "%s\t%s\t%s\twould-relabel\n", id.ID, id.Traits.Email, username)
						relabelled++
						continue
					}
					if err := kc.UpdateUsernameTrait(ctx, id.ID, username); err != nil {
						if errors.Is(err, kratosclient.ErrIdentityNotFound) {
							fmt.Fprintf(tw, "%s\t%s\t%s\tSKIP (vanished)\n", id.ID, id.Traits.Email, username)
							skipped++
							continue
						}
						fmt.Fprintf(tw, "%s\t%s\t%s\tFAIL: %v\n", id.ID, id.Traits.Email, username, err)
						failed++
						continue
					}
					fmt.Fprintf(tw, "%s\t%s\t%s\trelabelled\n", id.ID, id.Traits.Email, username)
					relabelled++
				}
				if next == "" {
					break
				}
				pageToken = next
			}
			tw.Flush()

			if apply {
				fmt.Printf("\nRelabelled %d, skipped %d, failed %d.\n", relabelled, skipped, failed)
				if failed > 0 {
					return fmt.Errorf("%d identities failed to relabel — re-run after fixing", failed)
				}
			} else {
				fmt.Printf("\n[dry-run] %d identities would be relabelled, %d skipped. Re-run with --apply.\n", relabelled, skipped)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "PATCH the identities (default: dry-run)")
	return cmd
}
