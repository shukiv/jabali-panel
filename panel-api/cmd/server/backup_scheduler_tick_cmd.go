// `jabali backup scheduler tick` cobra subcommand.
//
// Triggers one synchronous enqueue + dispatch pass of the backup
// scheduler (same code path serve.go's long-running goroutine
// invokes every 60s + 10s). Useful for bootstrap-script tests that
// want a deterministic 'create-schedule + tick + assert-job-row'
// sequence without waiting on real-time.
//
// Idempotent: ticking with no due schedules is a no-op. Running
// against an already-active panel-api process double-fires the
// dispatch slot but the in-memory inFlight map dedupes — worst
// case a few extra log lines.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/backupscheduler"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func newBackupSchedulerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scheduler",
		Short: "Backup scheduler ops (manual tick / debug)",
	}
	cmd.AddCommand(newBackupSchedulerTickCmd())
	return cmd
}

func newBackupSchedulerTickCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tick",
		Short: "Run one enqueue + dispatch pass of the backup scheduler synchronously",
		Long: `Triggers Scheduler.TickOnce — same code path the serve.go
long-running goroutine fires every 60s (enqueue) + 10s (dispatch).
Useful for bootstrap scripts asserting on schedule firing without
real-time waits.

Builds its own Scheduler with sharedDB + sharedAgent; doesn't talk
to the running panel-api. Both routes hit the same DB rows so an
operator running this against a live panel sees the in-memory
inFlight map dedupe duplicate dispatches.`,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			// Best-effort: wire the notification queue so a manual tick notifies
			// tenants + admins on a retention-cap event too (GH #454). If Redis is
			// unavailable the tick still runs; Notify just stays nil.
			var notify *notifications.Queue
			if requireRedis(cmd, args) == nil && sharedRedis != nil {
				notify = notifications.NewQueue(sharedRedis)
			}
			s := backupscheduler.New(backupscheduler.Deps{
				Schedules:      repository.NewBackupScheduleRepository(sharedDB),
				Jobs:           repository.NewBackupJobRepository(sharedDB),
				Destinations:   repository.NewBackupDestinationRepository(sharedDB),
				Users:          repository.NewUserRepository(sharedDB),
				Databases:      repository.NewDatabaseRepository(sharedDB),
				DatabaseUsers:  repository.NewDatabaseUserRepository(sharedDB),
				DatabaseGrants: repository.NewDatabaseUserGrantRepository(sharedDB),
				Domains:        repository.NewDomainRepository(sharedDB),
				Mailboxes:      repository.NewMailboxRepository(sharedDB),
				AppInstalls:    repository.NewWordPressInstallRepository(sharedDB),
				DockerApps:     repository.NewDockerAppRepository(sharedDB),
				Settings:       repository.NewServerSettingsRepository(sharedDB),
				Packages:       repository.NewPackageRepository(sharedDB),
				Notify:         notify,
				SSLCerts:       repository.NewSSLCertificateRepository(sharedDB),
				PHPPools:       repository.NewPHPPoolRepository(sharedDB),
				PHPPoolIni:     repository.NewPHPPoolIniOverrideRepository(sharedDB),
				Forwarders:     repository.NewEmailForwarderRepository(sharedDB),
				Autoresponders: repository.NewEmailAutoresponderRepository(sharedDB),
				MailboxShares:  repository.NewMailboxShareRepository(sharedDB),
				DNSSECKeys:     repository.NewDNSSECKeyRepository(sharedDB),
				SSHKeys:        repository.NewSSHKeyRepository(sharedDB),
				CronJobs:       repository.NewCronJobRepository(sharedDB),
				FtpAccounts:    repository.NewFtpAccountRepository(sharedDB),
				LimitOverrides: repository.NewUserLimitOverrideRepository(sharedDB),
				EgressPolicies: repository.NewUserEgressPolicyRepository(sharedDB),
				EgressRequests: repository.NewUserEgressRequestRepository(sharedDB),
				Agent:          sharedAgent,
				// TickOnce dispatches too, and dispatch unseals per-destination restic
				// passwords — wire the same SSO key the production scheduler uses, else
				// sealed-password destinations fail before the agent call (Gitea #538).
				SSOKey: ssoKeyForCLI(),
				Log:    sharedLog,
			})
			if s == nil {
				return fmt.Errorf("scheduler.New returned nil — required deps missing (check serve.go's Deps assembly)")
			}
			if ssoKeyForCLI() == nil {
				fmt.Fprintln(cmd.OutOrStderr(), "warning: SSO key unavailable — dispatch of destinations with a sealed per-destination restic password will fail (set sso.key_path / run on the panel host)")
			}
			s.TickOnce(ctx)
			fmt.Fprintln(cmd.OutOrStdout(), "scheduler tick: enqueue + dispatch passes complete")
			return nil
		},
	}
}
