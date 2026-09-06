// `jabali notification` channel CRUD + test + broadcast (Gitea #563, third
// slice). Mirrors the admin notification API:
//   - channels create/delete  (repo, same ValidateChannelName / ValidateChannelKindConfig)
//   - channels test <id>       (publishes a synthetic envelope to ONE channel)
//   - broadcast                (publishes an envelope that fans out to every enabled channel)
//
// test/broadcast publish to the SAME Redis stream (jabali:notifications:queue)
// via notifications.Queue.Publish that the REST /test + /broadcast handlers use
// — the live dispatcher then delivers. Mutations CLI-audited (#537).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifchannelops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func notificationChannelRepoFromDB() repository.NotificationChannelRepository {
	// Seal channel secrets at rest when a key is available (JAB-171), so a
	// CLI-created channel stores its token/password encrypted just like the API.
	if key := ssoKeyForCLI(); key != nil {
		return repository.NewNotificationChannelRepository(sharedDB, repository.WithSealKey(*key))
	}
	return repository.NewNotificationChannelRepository(sharedDB)
}

func newNotificationChannelCreateCmd() *cobra.Command {
	var name, kind, configJSON, userID string
	var disabled bool
	cmd := &cobra.Command{
		Use:     "create --name <n> --kind <email|slack|discord|ntfy|webhook|webpush|sms> --config <json>",
		Short:   "Create a notification channel",
		Args:    cobra.NoArgs,
		PreRunE: requireDB,
		RunE: func(c *cobra.Command, _ []string) error {
			if name == "" || kind == "" {
				return fmt.Errorf("--name and --kind are required")
			}
			var cfg models.NotificationChannelConfig
			if configJSON != "" {
				if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
					return fmt.Errorf("--config must be valid JSON: %w", err)
				}
			}
			if err := api.ValidateChannelName(name); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(c.Context(), 15*time.Second)
			defer cancel()
			// --user makes the channel tenant-owned (JAB-171); omit for a
			// server-wide admin channel. A tenant-owned channel is subject to the
			// same owner policy the tenant HTTP handler enforces: master gate +
			// kind allowlist, then own-address email forcing (anti-relay/SSRF),
			// then the per-user quota — so the operator CLI can't create a tenant
			// channel the API would refuse. Order mirrors the handler: allowlist
			// before the owner-email lookup, forcing before per-kind config
			// validation, quota last. For a custom SMTP host, create a server-wide
			// channel (omit --user). The users FK still rejects an unknown id.
			uid := strings.TrimSpace(userID)
			if uid != "" {
				st, serr := repository.NewServerSettingsRepository(sharedDB).Get(ctx)
				if serr != nil {
					return fmt.Errorf("load server settings: %w", serr)
				}
				if err := notifchannelops.CheckKindAllowed(st, kind); err != nil {
					return fmt.Errorf("tenant channel policy: %w", err)
				}
				if kind == models.NotificationChannelKindEmail {
					// Surface a real lookup failure instead of folding it into an
					// empty owner email, which ForceOwnEmailConfig would misreport
					// as "owner account has no email address".
					u, uerr := repository.NewUserRepository(sharedDB).FindByID(ctx, uid)
					if uerr != nil {
						return fmt.Errorf("load owner %s: %w", uid, uerr)
					}
					ownerEmail := ""
					if u != nil {
						ownerEmail = u.Email
					}
					if err := notifchannelops.ForceOwnEmailConfig(ownerEmail, &cfg); err != nil {
						return fmt.Errorf("tenant channel policy: %w", err)
					}
				}
			}
			if err := api.ValidateChannelKindConfig(kind, cfg); err != nil {
				return err
			}
			if uid != "" {
				existing, lerr := notificationChannelRepoFromDB().ListByUser(ctx, uid)
				if lerr != nil {
					return fmt.Errorf("count owner channels: %w", lerr)
				}
				if err := notifchannelops.CheckQuota(len(existing)); err != nil {
					return fmt.Errorf("tenant channel policy: %w", err)
				}
			}
			row := &models.NotificationChannel{
				ID:      ids.NewULID(),
				Name:    name,
				Kind:    kind,
				Config:  cfg,
				Enabled: !disabled,
			}
			if uid != "" {
				row.UserID = &uid
			}
			if err := notificationChannelRepoFromDB().Create(ctx, row); err != nil {
				cliAuditErr(ctx, "notification_channel.create", "notification_channel", row.ID, nil)
				return fmt.Errorf("create channel: %w", err)
			}
			cliAuditOK(ctx, "notification_channel.create", "notification_channel", row.ID, nil)
			owner := "server-wide"
			if row.UserID != nil {
				owner = "user " + *row.UserID
			}
			fmt.Printf("created %s channel %q (%s, %s)\n", kind, name, row.ID, owner)
			return nil
		},
	}
	cmd.Flags().StringVar(&userID, "user", "", "owner user id (tenant-owned channel; omit for server-wide)")
	cmd.Flags().StringVar(&name, "name", "", "channel name (required)")
	cmd.Flags().StringVar(&kind, "kind", "", "email|slack|discord|ntfy|webhook|webpush|sms (required)")
	cmd.Flags().StringVar(&configJSON, "config", "", "per-kind config JSON (e.g. '{\"url\":\"https://…\"}')")
	cmd.Flags().BoolVar(&disabled, "disabled", false, "create in the disabled state")
	return cmd
}

func newNotificationChannelDeleteCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:     "delete <id>",
		Short:   "Delete a notification channel",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(c *cobra.Command, args []string) error {
			if !force {
				return fmt.Errorf("refusing to delete channel %q without --force", args[0])
			}
			ctx, cancel := context.WithTimeout(c.Context(), 15*time.Second)
			defer cancel()
			if err := notificationChannelRepoFromDB().Delete(ctx, args[0]); err != nil {
				cliAuditErr(ctx, "notification_channel.delete", "notification_channel", args[0], nil)
				return fmt.Errorf("delete channel: %w", err)
			}
			cliAuditOK(ctx, "notification_channel.delete", "notification_channel", args[0], nil)
			fmt.Printf("deleted channel %s\n", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "confirm deletion")
	return cmd
}

func newNotificationChannelTestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "test <id>",
		Short:   "Send a synthetic test notification to one channel",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireRedis, // requireRedis also opens DB (for audit) + loads panel.env
		RunE: func(c *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(c.Context(), 15*time.Second)
			defer cancel()
			ch, err := notificationChannelRepoFromDB().FindByID(ctx, args[0])
			if err != nil || ch == nil {
				return fmt.Errorf("no channel with id %q", args[0])
			}
			// Refuse a disabled channel at the adapter, mirroring the HTTP
			// testChannel 409. The dispatcher already skips disabled targets
			// (resolveTargets drops !ch.Enabled), so testing one only queued an
			// envelope that was silently dropped while the CLI reported success.
			if !ch.Enabled {
				cliAuditErr(ctx, "notification_channel.test", "notification_channel", ch.ID, nil)
				return fmt.Errorf("channel %q is disabled — enable it before testing", ch.Name)
			}
			env := notifications.Envelope{
				EventKind:  "notifications.channel.test",
				Severity:   models.NotificationSeverityInfo,
				Title:      fmt.Sprintf("Test notification for %s", ch.Name),
				Body:       "Synthetic envelope fired by `jabali notification channels test`. If you see it, the channel is wired up correctly.",
				Deeplink:   "/admin/notifications/channels/" + ch.ID,
				ChannelIDs: []string{ch.ID},
			}
			streamID, perr := notifications.NewQueue(sharedRedis).Publish(ctx, env)
			if perr != nil {
				cliAuditErr(ctx, "notification_channel.test", "notification_channel", ch.ID, nil)
				return fmt.Errorf("publish test: %w", perr)
			}
			cliAuditOK(ctx, "notification_channel.test", "notification_channel", ch.ID, nil)
			fmt.Printf("published test envelope to %s (%s); the dispatcher will deliver (stream id %s)\n", ch.Name, ch.Kind, streamID)
			return nil
		},
	}
	return cmd
}

func newNotificationBroadcastCmd() *cobra.Command {
	var title, body, severity, deeplink string
	cmd := &cobra.Command{
		Use:     "broadcast --title <t> [--body <b>] [--severity info|warning|error|critical]",
		Short:   "Broadcast a notification to every enabled channel",
		Args:    cobra.NoArgs,
		PreRunE: requireRedis,
		RunE: func(c *cobra.Command, _ []string) error {
			if title == "" || len(title) > 200 {
				return fmt.Errorf("--title is required, max 200 chars")
			}
			if len(body) > 2000 {
				return fmt.Errorf("--body max 2000 chars")
			}
			if severity == "" {
				severity = models.NotificationSeverityInfo
			}
			switch severity {
			case models.NotificationSeverityInfo, models.NotificationSeverityWarning,
				models.NotificationSeverityError, models.NotificationSeverityCritical:
			default:
				return fmt.Errorf("--severity must be info|warning|error|critical")
			}
			ctx, cancel := context.WithTimeout(c.Context(), 15*time.Second)
			defer cancel()
			env := notifications.Envelope{
				EventKind: "notifications.broadcast",
				Severity:  severity,
				Title:     title,
				Body:      body,
				Deeplink:  deeplink,
			}
			streamID, perr := notifications.NewQueue(sharedRedis).Publish(ctx, env)
			if perr != nil {
				cliAuditErr(ctx, "notification.broadcast", "notification", "*", nil)
				return fmt.Errorf("publish broadcast: %w", perr)
			}
			cliAuditOK(ctx, "notification.broadcast", "notification", "*", nil)
			fmt.Printf("broadcast published (%s); fans out to every enabled channel (stream id %s)\n", severity, streamID)
			return nil
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "notification title (required, <=200)")
	cmd.Flags().StringVar(&body, "body", "", "notification body (<=2000)")
	cmd.Flags().StringVar(&severity, "severity", "", "info|warning|error|critical (default info)")
	cmd.Flags().StringVar(&deeplink, "deeplink", "", "optional in-panel deeplink")
	return cmd
}
