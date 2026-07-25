// `jabali notification` (Gitea #563) — operator inspection + event-toggle
// parity for the notification subsystem. This first slice mirrors the
// read/repo-backed parts of the admin notification API:
//
//   - channels list           (GET /admin/notifications/channels)
//   - events list             (GET /admin/notifications/events)
//   - events set <kind>       (PATCH /admin/notifications/events/:kind)
//
// Channel CRUD / test, broadcast, DLQ replay/drop, and inbox ops are NOT in
// this slice: they need the live notification dispatcher + a Redis-Streams
// client (the DLQ is XADD/XDEL against jabali:notifications:dlq) + channel
// secret handling, which a thin CLI process doesn't hold. Tracked as a
// follow-up. Mutations here are CLI-audited (#537).
package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func newNotificationCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "notification",
		Aliases: []string{"notifications"},
		Short:   "Inspect notification channels and toggle event notifications",
	}
	cmd.AddCommand(newNotificationChannelsCmd(), newNotificationEventsCmd(), newNotificationDLQCmd(), newNotificationBroadcastCmd(), newNotificationInboxCmd())
	return cmd
}

func newNotificationChannelsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "channels", Short: "Notification channels"}
	cmd.AddCommand(newNotificationChannelCreateCmd(), newNotificationChannelDeleteCmd(), newNotificationChannelTestCmd())
	cmd.AddCommand(&cobra.Command{
		Use:     "seal-secrets",
		Short:   "Encrypt any plaintext channel secrets at rest (JAB-171 one-time backfill)",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			key := ssoKeyForCLI()
			if key == nil {
				return fmt.Errorf("SSO signing key not configured; cannot seal secrets")
			}
			repo := repository.NewNotificationChannelRepository(sharedDB, repository.WithSealKey(*key))
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			n, err := repo.SealAllPlaintext(ctx)
			if err != nil {
				return fmt.Errorf("seal secrets: %w", err)
			}
			fmt.Printf("Sealed %d channel(s) that had plaintext secrets.\n", n)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:     "list",
		Short:   "List configured notification channels",
		Args:    cobra.NoArgs,
		PreRunE: requireDB,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(c.Context(), 15*time.Second)
			defer cancel()
			rows, _, err := repository.NewNotificationChannelRepository(sharedDB).
				ListAll(ctx, repository.ListOptions{Limit: 500})
			if err != nil {
				return fmt.Errorf("list channels: %w", err)
			}
			if jsonOutput {
				return printJSON(rows)
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tKIND\tENABLED\tTARGET")
			for i := range rows {
				fmt.Fprintf(w, "%s\t%s\t%s\t%v\t%s\n", rows[i].ID, rows[i].Name, rows[i].Kind, rows[i].Enabled, channelTarget(rows[i]))
			}
			if len(rows) == 0 {
				fmt.Fprintln(w, "(no channels configured)")
			}
			return w.Flush()
		},
	})
	return cmd
}

// channelTarget renders a NON-SECRET hint of where a channel delivers. Secrets
// (hmac_secret, bearer, smtp_password) are never printed.
func channelTarget(ch models.NotificationChannel) string {
	cfg := ch.Config
	switch ch.Kind {
	case models.NotificationChannelKindEmail:
		return cfg.ToEmail
	case models.NotificationChannelKindWebhook, models.NotificationChannelKindSlack,
		models.NotificationChannelKindDiscord, models.NotificationChannelKindNtfy:
		return cfg.URL
	default:
		return ""
	}
}

func newNotificationEventsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "events", Short: "Per-event notification toggles"}
	cmd.AddCommand(
		&cobra.Command{
			Use:     "list",
			Short:   "List notification event kinds and whether each is enabled",
			Args:    cobra.NoArgs,
			PreRunE: requireDB,
			RunE: func(c *cobra.Command, _ []string) error {
				ctx, cancel := context.WithTimeout(c.Context(), 15*time.Second)
				defer cancel()
				rows, err := repository.NewNotificationEventSettingRepository(sharedDB).List(ctx)
				if err != nil {
					return fmt.Errorf("list event settings: %w", err)
				}
				enabled := map[string]bool{}
				for _, r := range rows {
					enabled[r.EventKind] = r.Enabled
				}
				if jsonOutput {
					out := make([]map[string]any, 0, len(models.AllNotificationEventKinds))
					for _, m := range models.AllNotificationEventKinds {
						en, ok := enabled[m.Kind]
						if !ok {
							en = m.DefaultOn
						}
						out = append(out, map[string]any{"kind": m.Kind, "label": m.Label, "enabled": en})
					}
					return printJSON(out)
				}
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "KIND\tENABLED\tLABEL")
				for _, m := range models.AllNotificationEventKinds {
					en, ok := enabled[m.Kind]
					if !ok {
						en = m.DefaultOn
					}
					fmt.Fprintf(w, "%s\t%v\t%s\n", m.Kind, en, m.Label)
				}
				return w.Flush()
			},
		},
		newNotificationEventsSetCmd(),
	)
	return cmd
}

func newNotificationEventsSetCmd() *cobra.Command {
	var enabledFlag string
	cmd := &cobra.Command{
		Use:     "set <event-kind> --enabled <true|false>",
		Short:   "Enable or disable notifications for an event kind",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(c *cobra.Command, args []string) error {
			if enabledFlag == "" {
				return fmt.Errorf("--enabled is required (true|false)")
			}
			en, perr := parseBool(enabledFlag)
			if perr != nil {
				return fmt.Errorf("--enabled: %w", perr)
			}
			kind := args[0]
			known := false
			for _, m := range models.AllNotificationEventKinds {
				if m.Kind == kind {
					known = true
					break
				}
			}
			if !known {
				return fmt.Errorf("unknown event kind %q (see `jabali notification events list`)", kind)
			}
			ctx, cancel := context.WithTimeout(c.Context(), 15*time.Second)
			defer cancel()
			if err := repository.NewNotificationEventSettingRepository(sharedDB).Set(ctx, kind, en); err != nil {
				cliAuditErr(ctx, "notification_event.set", "notification_event", kind, nil)
				return fmt.Errorf("set event: %w", err)
			}
			cliAuditOK(ctx, "notification_event.set", "notification_event", kind, nil)
			fmt.Printf("event %s notifications %s\n", kind, map[bool]string{true: "ENABLED", false: "disabled"}[en])
			return nil
		},
	}
	cmd.Flags().StringVar(&enabledFlag, "enabled", "", "true|false (required)")
	return cmd
}
