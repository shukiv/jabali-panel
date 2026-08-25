package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/userops"
)

// cliRenameDeps wires userops.Deps for `user rename` from the CLI singletons.
func cliRenameDeps() userops.Deps {
	var kc *kratosclient.Client
	if sharedCfg != nil {
		kc = kratosclient.NewClient(sharedCfg.Auth.Kratos.PublicURL, sharedCfg.Auth.Kratos.AdminURL)
	}
	d := userops.Deps{
		Users:        userRepo(),
		Domains:      domainRepoFromDB(),
		KratosClient: kc,
		Log:          slog.Default(),
	}
	if sharedAgent != nil {
		d.Agent = sharedAgent
	}
	return d
}

func newUserRenameCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "rename <email|username|user-id> <new-username>",
		Short: "Rename a tenant's Linux/login username in place (GH #1238)",
		Long: `Rename a tenant's Linux account (and Kratos login identifier) in place.

The uid is preserved, so file ownership is unchanged; the account name and the
home directory (/home/<old> -> /home/<new>) move, and every owned domain's docroot
is repointed and re-rendered.

v1 refuses the rename when the tenant has FTP/SFTP subaccounts or Python apps
(their jails/app-roots need move handling a later version adds). Their databases
and DB SSO shadow roles keep the old-username prefix (the panel keys them by id).`,
		Args:    cobra.ExactArgs(2),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()

			target, err := resolveUser(ctx, args[0])
			if err != nil {
				return err
			}
			newName := args[1]
			old := ""
			if target.Username != nil {
				old = *target.Username
			}

			if !yes {
				ok, cerr := confirm(fmt.Sprintf(
					"Rename user %q -> %q? This renames the Linux account, moves /home/%s -> /home/%s, and re-renders their sites.",
					old, newName, old, newName))
				if cerr != nil {
					return cerr
				}
				if !ok {
					fmt.Println("Aborted.")
					return nil
				}
			}

			rd := userops.RenameDeps{
				FtpAccounts: repository.NewFtpAccountRepository(sharedDB),
				PythonApps:  pythonAppRepoFromDB(),
				// Reconciler is nil: the CLI is a separate process, so the running
				// panel's periodic reconcile re-renders the owned domains.
			}
			if err := userops.RenameUser(ctx, cliRenameDeps(), rd, target, newName); err != nil {
				cliAuditErr(ctx, "user.rename", "user", target.ID, &target.ID)
				return err
			}
			cliAuditOK(ctx, "user.rename", "user", target.ID, &target.ID)
			fmt.Printf("Renamed %q -> %q.\nThe reconciler re-renders their sites within ~a minute; verify with a page load or `jabali domain list`.\n", old, newName)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}
