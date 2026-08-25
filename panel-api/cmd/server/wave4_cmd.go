// Wave 4 CLI parity (Gitea #589 mail logs, #585 htaccess preview, #582 limits
// usage + override). Mail logs are agent passthroughs; htaccess preview is the
// stateless Go converter (internal/htaccess); limits override is a repo write
// the live reconciler converges (feedback_cli_reconcile_apply).
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/limits"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/htaccess"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ---- #589 mail logs ----

func newMailLogsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "logs", Short: "Search / inspect mail delivery logs"}
	var (
		limit, offset     int
		sender, recipient string
		fromDate, toDate  string
	)
	queryCmd := &cobra.Command{
		Use: "query", Short: "Search mail logs", Args: cobra.NoArgs, PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, _ []string) error {
			params := map[string]any{"limit": limit, "offset": offset}
			if sender != "" {
				params["sender_prefix"] = sender
			}
			if recipient != "" {
				params["recipient_prefix"] = recipient
			}
			if fromDate != "" {
				params["from_date"] = fromDate
			}
			if toDate != "" {
				params["to_date"] = toDate
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			return csCall(ctx, "mail.logs_query", params)
		},
	}
	queryCmd.Flags().IntVar(&limit, "limit", 50, "max entries")
	queryCmd.Flags().IntVar(&offset, "offset", 0, "offset")
	queryCmd.Flags().StringVar(&sender, "sender", "", "sender prefix filter")
	queryCmd.Flags().StringVar(&recipient, "recipient", "", "recipient prefix filter")
	queryCmd.Flags().StringVar(&fromDate, "from", "", "from date (YYYY-MM-DD)")
	queryCmd.Flags().StringVar(&toDate, "to", "", "to date (YYYY-MM-DD)")

	detailCmd := &cobra.Command{
		Use: "detail <id>", Short: "Show a mail log entry's detail", Args: cobra.ExactArgs(1), PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			return csCall(ctx, "mail.logs_detail", map[string]any{"id": args[0]})
		},
	}
	cmd.AddCommand(queryCmd, detailCmd)
	return cmd
}

// ---- #585 .htaccess conversion preview ----

func newDomainHtaccessPreviewCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:     "htaccess-preview <domain-name|domain-id>",
		Short:   "Preview the nginx translation of a .htaccess file (stateless; nothing applied)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			if file == "" {
				return fmt.Errorf("--file <.htaccess path> is required")
			}
			content, err := os.ReadFile(file)
			if err != nil {
				return fmt.Errorf("read .htaccess: %w", err)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			dom, derr := resolveDomainCLI(ctx, args[0])
			if derr != nil {
				return derr
			}
			// basePath = the domain docroot, same as the GUI preview.
			res := htaccess.Convert(string(content), dom.DocRoot)
			return printJSON(res)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "path to a .htaccess file to convert (required)")
	return cmd
}

// ---- #582 per-user limit usage + override ----

func newLimitsUsageCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "usage <username|user-id>",
		Short:   "Show a user's live limit usage (disk/cpu/mem/io/tasks)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			u, err := resolveUser(ctx, args[0])
			if err != nil {
				return err
			}
			if u.Username == nil || *u.Username == "" {
				return fmt.Errorf("user %s has no Linux account", u.ID)
			}
			return csCall(ctx, "user.limits.report", map[string]any{"username": *u.Username, "quota_mount": "/home"})
		},
	}
}

func newLimitsOverrideCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "override", Short: "Per-user limit overrides (set / clear)"}
	var disk, cpu, mem, ioR, ioW, tasks uint32
	setCmd := &cobra.Command{
		Use: "set <username|user-id>", Short: "Set/replace a user's limit override", Args: cobra.ExactArgs(1), PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
			defer cancel()
			u, err := resolveUser(ctx, args[0])
			if err != nil {
				return err
			}
			o := &models.UserLimitOverride{UserID: u.ID}
			set := false
			if cmd.Flags().Changed("disk-mb") {
				o.DiskQuotaMB = &disk
				set = true
			}
			if cmd.Flags().Changed("cpu") {
				o.CPUQuotaPercent = &cpu
				set = true
			}
			if cmd.Flags().Changed("memory-mb") {
				o.MemoryLimitMB = &mem
				set = true
			}
			if cmd.Flags().Changed("io-read-mbps") {
				o.IOReadMbps = &ioR
				set = true
			}
			if cmd.Flags().Changed("io-write-mbps") {
				o.IOWriteMbps = &ioW
				set = true
			}
			if cmd.Flags().Changed("max-tasks") {
				o.MaxTasks = &tasks
				set = true
			}
			if !set {
				return fmt.Errorf("specify at least one override (--disk-mb/--cpu/--memory-mb/--io-read-mbps/--io-write-mbps/--max-tasks)")
			}
			// JAB-309: enforce the same bounds the REST handler does — the CLI
			// previously wrote overrides unvalidated, so an out-of-range value
			// could be persisted and dispatched to the agent.
			if err := limits.ValidateOverrideBounds(o.CPUQuotaPercent, o.MemoryLimitMB, o.IOReadMbps, o.IOWriteMbps, o.MaxTasks); err != nil {
				return fmt.Errorf("invalid override: %w", err)
			}
			if err := repository.NewUserLimitOverrideRepository(sharedDB).Upsert(ctx, o); err != nil {
				return fmt.Errorf("upsert override: %w", err)
			}
			cliAuditOK(ctx, "user.limits_override_set", "user", u.ID, &u.ID)
			fmt.Fprintf(cmd.OutOrStdout(), "Override set for %s. Converges on the next reconcile pass.\n", u.ID)
			return nil
		},
	}
	setCmd.Flags().Uint32Var(&disk, "disk-mb", 0, "disk quota MB")
	setCmd.Flags().Uint32Var(&cpu, "cpu", 0, "CPU quota percent")
	setCmd.Flags().Uint32Var(&mem, "memory-mb", 0, "memory limit MB")
	setCmd.Flags().Uint32Var(&ioR, "io-read-mbps", 0, "IO read MB/s")
	setCmd.Flags().Uint32Var(&ioW, "io-write-mbps", 0, "IO write MB/s")
	setCmd.Flags().Uint32Var(&tasks, "max-tasks", 0, "max tasks")

	clearCmd := &cobra.Command{
		Use: "clear <username|user-id>", Short: "Remove a user's override (revert to package)", Args: cobra.ExactArgs(1), PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
			defer cancel()
			u, err := resolveUser(ctx, args[0])
			if err != nil {
				return err
			}
			if err := repository.NewUserLimitOverrideRepository(sharedDB).Delete(ctx, u.ID); err != nil {
				return fmt.Errorf("clear override: %w", err)
			}
			cliAuditOK(ctx, "user.limits_override_clear", "user", u.ID, &u.ID)
			fmt.Fprintf(cmd.OutOrStdout(), "Override cleared for %s — reverts to package limits on the next reconcile pass.\n", u.ID)
			return nil
		},
	}
	cmd.AddCommand(setCmd, clearCmd)
	return cmd
}

// ---- #590 per-domain bandwidth report ----

func newDomainBandwidthCmd() *cobra.Command {
	var days int
	cmd := &cobra.Command{
		Use:     "bandwidth <domain-name|domain-id>",
		Short:   "Per-domain bandwidth report (bytes + requests, daily series)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			if days < 1 || days > 365 {
				return fmt.Errorf("--days must be 1..365")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			dom, err := resolveDomainCLI(ctx, args[0])
			if err != nil {
				return err
			}
			repo := repository.NewBWDailyRepository(sharedDB)
			now := time.Now().UTC()
			from := now.AddDate(0, 0, -(days - 1))
			bytesTotal, reqsTotal, err := repo.SumForDomain(ctx, dom.ID, from, now)
			if err != nil {
				return err
			}
			daily, err := repo.SumPerDayForDomain(ctx, dom.ID, from, now)
			if err != nil {
				return err
			}
			if jsonOutput {
				return printJSON(map[string]any{
					"domain": dom.Name, "days": days,
					"bytes_total": bytesTotal, "requests_total": reqsTotal, "daily": daily,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s — last %dd: %d bytes, %d requests\n", dom.Name, days, bytesTotal, reqsTotal)
			for _, p := range daily {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s  %d bytes\n", p.Day.Format("2006-01-02"), p.BytesTotal)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 30, "lookback window in days (1..365)")
	return cmd
}
