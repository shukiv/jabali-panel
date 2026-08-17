package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/userops"
)

// user_suspend_cmd.go — `jabali user suspend/unsuspend <id>` (JAB-127). Shares
// the exact cascade the admin GUI/API uses via userops.Suspend/Unsuspend, so a
// headless/scriptable suspend has the same side effects (DB flag, Kratos
// inactive, domains disabled, Docker stopped, OS user locked).

// cliSuspendDeps wires userops.Deps from the shared CLI singletons.
func cliSuspendDeps() userops.Deps {
	var kc *kratosclient.Client
	if sharedCfg != nil {
		kc = kratosclient.NewClient(sharedCfg.Auth.Kratos.PublicURL, sharedCfg.Auth.Kratos.AdminURL)
	}
	d := userops.Deps{
		Users:        userRepo(),
		Domains:      domainRepoFromDB(),
		DockerApps:   dockerAppRepoFromDB(),
		KratosClient: kc,
		Log:          slog.Default(),
	}
	// Guard the typed-nil trap: only set Agent when the shared client exists.
	if sharedAgent != nil {
		d.Agent = sharedAgent
	}
	return d
}

func cliPrintWarnings(warns ...string) {
	for _, w := range warns {
		if w != "" {
			fmt.Fprintf(os.Stderr, "  warning: %s\n", w)
		}
	}
}

func newUserSuspendCmd() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "suspend <id>",
		Short: "Suspend a user (same cascade as the admin GUI/API)",
		Args:  cobra.ExactArgs(1),
		// requireDBAndAgent, not just requireDB: without it userRepo() runs on a
		// nil *gorm.DB and the command SIGSEGVs (JAB-272). requireDB alone would
		// stop the panic but leave sharedAgent nil, and cliSuspendDeps() drops
		// d.Agent when the shared client is nil — so the suspend would report
		// success while silently skipping the OS-user lock and the
		// ftpaccount.lock_tenant call. Initialising the agent keeps the CLI
		// cascade identical to the panel API path.
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Minute)
			defer cancel()

			id := args[0]
			u, err := userRepo().FindByID(ctx, id)
			if err != nil || u == nil {
				return fmt.Errorf("user %q not found", id)
			}
			if u.IsAdmin {
				return fmt.Errorf("refusing to suspend admin user %q — demote first via the admin panel", id)
			}

			res, err := userops.Suspend(ctx, cliSuspendDeps(), u, reason)
			if err != nil {
				return err
			}
			if res.AlreadySuspended {
				fmt.Printf("user %s is already suspended\n", id)
				return nil
			}
			fmt.Printf("suspended user %s (domains disabled: %d)\n", id, res.DomainsDisabled)
			cliPrintWarnings(res.KratosWarning, res.DomainWarning, res.OSWarning)
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "operator-facing suspend reason (shown in the admin user list)")
	return cmd
}

func newUserUnsuspendCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unsuspend <id>",
		Short: "Unsuspend a user (reverse the cascade)",
		Args:  cobra.ExactArgs(1),
		// Same nil-DB / nil-agent trap as suspend (JAB-272): the unsuspend
		// cascade re-enables domains and unlocks the OS user + FTP subaccounts
		// via the agent, so both DB and agent must be initialised.
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Minute)
			defer cancel()

			id := args[0]
			u, err := userRepo().FindByID(ctx, id)
			if err != nil || u == nil {
				return fmt.Errorf("user %q not found", id)
			}

			res, err := userops.Unsuspend(ctx, cliSuspendDeps(), u)
			if err != nil {
				return err
			}
			if res.AlreadyActive {
				fmt.Printf("user %s is already active\n", id)
				return nil
			}
			fmt.Printf("unsuspended user %s (domains enabled: %d)\n", id, res.DomainsEnabled)
			cliPrintWarnings(res.KratosWarning, res.DomainWarning, res.OSWarning)
			return nil
		},
	}
}
