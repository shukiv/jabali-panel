package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/userops"
)

func newDomainChownCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "chown <domain> <new-owner>",
		Short: "Reassign a domain to a different owner, moving its docroot (GH #1238)",
		Long: `Reassign a domain to a different tenant.

The domain row is preserved (its SSL lineage, DNS zone, and mailboxes stay); its
docroot tree is moved into the new owner's home and re-owned to the new uid, then
the reconciler re-binds the PHP pool and re-renders the vhost under the new owner.

v1 refuses a domain that has an app install: its config carries the current
owner's database credentials, so it must be detached/migrated to the new owner
first (a cross-tenant credential leak otherwise).`,
		Args:    cobra.ExactArgs(2),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()

			dom, err := resolveDomainCLI(ctx, args[0])
			if err != nil {
				return err
			}
			newOwner, err := resolveUser(ctx, args[1])
			if err != nil {
				return err
			}

			if !yes {
				ok, cerr := confirm(fmt.Sprintf(
					"Reassign %q to %q? This moves its docroot into the new owner's home and re-owns the files.",
					dom.Name, args[1]))
				if cerr != nil {
					return cerr
				}
				if !ok {
					fmt.Println("Aborted.")
					return nil
				}
			}

			d := userops.Deps{
				Users:   userRepo(),
				Domains: domainRepoFromDB(),
				Log:     slog.Default(),
			}
			if sharedAgent != nil {
				d.Agent = sharedAgent
			}
			cd := userops.ChownDeps{
				AppInstalls: repository.NewApplicationInstallRepository(sharedDB),
				// Reconciler nil: the CLI is a separate process; the running panel's
				// periodic reconcile re-binds the pool + re-renders the vhost.
			}
			if err := userops.ChangeDomainOwner(ctx, d, cd, dom, newOwner); err != nil {
				cliAuditErr(ctx, "domain.chown", "domain", dom.ID, &dom.UserID)
				return err
			}
			cliAuditOK(ctx, "domain.chown", "domain", dom.ID, &newOwner.ID)
			fmt.Printf("Reassigned %q to %q.\nThe reconciler re-renders it under the new owner within ~a minute.\n", dom.Name, *newOwner.Username)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}
