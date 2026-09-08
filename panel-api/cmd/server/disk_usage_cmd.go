// `jabali disk-usage` (Gitea #568). Operator-side mirror of the tenant
// /me/disk-usage surface (api/me_disk_usage.go):
//
//   - show <user>    — the last stored snapshot (cheap DB read), the same data
//     GET /me/disk-usage returns.
//   - refresh <user> — recompute live via api.ComputeDiskUsage (the SAME
//     aggregation POST /me/disk-usage/refresh runs: home quota
//     report + cached mailbox usage + db.size) and persist it
//     as the new snapshot.
//
// Uses the exported api.ComputeDiskUsage / api.DiskUsageResult so the numbers
// are identical to the GUI — one implementation, no drift.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/limits"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func diskUsageSnapshotRepoFromDB() repository.DiskUsageSnapshotRepository {
	return repository.NewDiskUsageSnapshotRepository(sharedDB)
}

// diskUsageCfg builds the same DiskUsageConfig serve.go wires for the endpoint.
func diskUsageCfg() api.DiskUsageConfig {
	quotaMount := ""
	if m, err := limits.QuotaMountFor("/home"); err == nil {
		quotaMount = m
	}
	return api.DiskUsageConfig{
		Users:      userRepo(),
		Domains:    repository.NewDomainRepository(sharedDB),
		Mailboxes:  repository.NewMailboxRepository(sharedDB),
		Databases:  repository.NewDatabaseRepository(sharedDB),
		Agent:      sharedAgent,
		QuotaMount: quotaMount,
		Snapshots:  diskUsageSnapshotRepoFromDB(),
	}
}

func newDiskUsageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "disk-usage",
		Short: "Inspect / refresh a tenant's disk-usage breakdown (files / email / databases)",
	}
	cmd.AddCommand(newDiskUsageShowCmd(), newDiskUsageRefreshCmd(), newDiskUsageRefreshAllCmd())
	return cmd
}

func printDiskUsage(res api.DiskUsageResult) {
	computed := "never (run refresh)"
	if res.ComputedAt != nil {
		computed = res.ComputedAt.UTC().Format("2006-01-02 15:04Z")
	}
	fmt.Printf("Total: %s   (home quota: %s)   computed %s\n",
		humanBytes(res.TotalBytes), fmtBytesLimit(res.QuotaBytes), computed)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "CATEGORY\tBYTES")
	fmt.Fprintf(w, "files\t%s\n", humanBytes(res.Files.Bytes))
	fmt.Fprintf(w, "email\t%s\n", humanBytes(res.Email.Bytes))
	fmt.Fprintf(w, "databases\t%s\n", humanBytes(res.Databases.Bytes))
	w.Flush()
}

func newDiskUsageShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "show <user-email|username|id>",
		Short:   "Show a tenant's last stored disk-usage snapshot",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			u, err := resolveUser(ctx, args[0])
			if err != nil {
				return err
			}
			var res api.DiskUsageResult
			res.Files.Items = []api.DiskUsageItem{}
			res.Email.Items = []api.DiskUsageItem{}
			res.Databases.Items = []api.DiskUsageItem{}
			if snap, serr := diskUsageSnapshotRepoFromDB().Get(ctx, u.ID); serr == nil && snap != nil {
				if uerr := json.Unmarshal([]byte(snap.Payload), &res); uerr == nil {
					ca := snap.ComputedAt
					res.ComputedAt = &ca
				}
			}
			if jsonOutput {
				return printJSON(res)
			}
			printDiskUsage(res)
			return nil
		},
	}
}

func newDiskUsageRefreshCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "refresh <user-email|username|id>",
		Short:   "Recompute a tenant's disk usage live and store the snapshot",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 90*time.Second)
			defer cancel()
			u, err := resolveUser(ctx, args[0])
			if err != nil {
				return err
			}
			res, err := api.ComputeDiskUsage(ctx, diskUsageCfg(), u.ID)
			if err != nil {
				return fmt.Errorf("compute disk usage: %w", err)
			}
			now := time.Now()
			res.ComputedAt = &now
			if payload, merr := json.Marshal(res); merr == nil {
				_ = diskUsageSnapshotRepoFromDB().Upsert(ctx, u.ID, string(payload), now)
			}
			if jsonOutput {
				return printJSON(res)
			}
			printDiskUsage(res)
			return nil
		},
	}
}

// newDiskUsageRefreshAllCmd recomputes the disk-usage snapshot for every tenant,
// SERIALLY, so the tenant dashboard + Disk Usage page carry a reasonably fresh
// figure without the user having to click Refresh (GH #1439, lxsdevcode). It is
// the daily-job half of that request; the nightly jabali-disk-maintenance timer
// runs it inside jabali-maintenance.slice (MemoryMax=50%, idle IO, Nice=19).
//
// JAB-273 is the load-bearing constraint here: the fleet outage was a nightly
// `du` fan-out running CONCURRENTLY across ~100 homes, which filled RAM on a
// swapless box into a kswapd death-spiral. This command is deliberately the
// opposite — one tenant's du at a time (never a goroutine fan-out), a fresh-skip
// gate so an account measured within --max-age is not re-du'd, an inter-user
// pause, and it runs only inside the resource-capped maintenance slice. Do NOT
// parallelise it.
func newDiskUsageRefreshAllCmd() *cobra.Command {
	var (
		maxAge         time.Duration
		perUserTimeout time.Duration
		betweenUsers   time.Duration
	)
	cmd := &cobra.Command{
		Use:   "refresh-all",
		Short: "Recompute the disk-usage snapshot for every tenant (serial; skips accounts refreshed within --max-age)",
		Args:  cobra.NoArgs,
		// Same deps as `refresh`: the live compute path needs the agent.
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The command context (cancellable on signal) bounds the whole run;
			// each tenant gets its OWN short timeout so one slow home cannot
			// starve the rest, and a run over many tenants is not held to any
			// single fixed deadline.
			ctx := cmd.Context()
			snaps := diskUsageSnapshotRepoFromDB()
			cfg := diskUsageCfg()

			// Only non-admin users have a Linux home to measure; admins have no
			// username (compute skips them, but filtering keeps the loop honest).
			notAdmin := false
			users, _, err := userRepo().List(ctx, repository.ListOptions{
				IsAdmin: &notAdmin,
				Limit:   100000, // effectively all tenants; a box never has this many
			})
			if err != nil {
				return fmt.Errorf("list tenants: %w", err)
			}

			// refreshOne is the live compute+persist for a single tenant — the
			// exact path `refresh <user>` runs, under a per-tenant timeout.
			refreshOne := func(c context.Context, userID string) error {
				uctx, cancel := context.WithTimeout(c, perUserTimeout)
				defer cancel()
				res, cerr := api.ComputeDiskUsage(uctx, cfg, userID)
				if cerr != nil {
					return cerr
				}
				now := time.Now()
				res.ComputedAt = &now
				payload, merr := json.Marshal(res)
				if merr != nil {
					return merr
				}
				return snaps.Upsert(uctx, userID, string(payload), now)
			}

			r, rerr := runDiskUsageRefreshAll(ctx, users, snaps, refreshAllOpts{
				maxAge:       maxAge,
				betweenUsers: betweenUsers,
			}, refreshOne)
			fmt.Printf("disk-usage refresh-all: refreshed=%d skipped=%d failed=%d (of %d tenants)\n",
				r.refreshed, r.skipped, r.failed, len(users))
			return rerr
		},
	}
	cmd.Flags().DurationVar(&maxAge, "max-age", 20*time.Hour,
		"skip a tenant whose snapshot is younger than this (0 = always refresh)")
	cmd.Flags().DurationVar(&perUserTimeout, "per-user-timeout", 90*time.Second,
		"per-tenant deadline for the live compute")
	cmd.Flags().DurationVar(&betweenUsers, "sleep", 2*time.Second,
		"pause between tenants to keep the sweep gentle")
	return cmd
}

type refreshAllOpts struct {
	maxAge       time.Duration
	betweenUsers time.Duration
}

type refreshAllResult struct {
	refreshed int
	skipped   int
	failed    int
}

// runDiskUsageRefreshAll walks the tenants SERIALLY, applying the fresh-skip
// gate and per-user error tolerance, and calls refreshOne for each tenant that
// needs a new snapshot. It is factored out of the cobra RunE so the sweep logic
// (skip-fresh, one-bad-home-doesn't-abort, honour ctx cancellation) is testable
// without a live agent. Callers MUST keep this serial — it is the JAB-273-safe
// property (see newDiskUsageRefreshAllCmd).
func runDiskUsageRefreshAll(
	ctx context.Context,
	users []models.User,
	snaps repository.DiskUsageSnapshotRepository,
	opts refreshAllOpts,
	refreshOne func(ctx context.Context, userID string) error,
) (refreshAllResult, error) {
	var r refreshAllResult
	for i := range users {
		if ctx.Err() != nil {
			return r, ctx.Err() // operator Ctrl-C / shutdown
		}
		u := users[i]
		if u.Username == nil || *u.Username == "" {
			continue // no Linux account → nothing to du
		}

		// Fresh-skip: a snapshot younger than maxAge (e.g. the tenant clicked
		// Refresh this evening) is left alone — this bounds the nightly du work
		// and keeps the sweep from re-measuring the whole fleet every run.
		if opts.maxAge > 0 {
			if snap, serr := snaps.Get(ctx, u.ID); serr == nil && snap != nil &&
				time.Since(snap.ComputedAt) < opts.maxAge {
				r.skipped++
				continue
			}
		}

		if err := refreshOne(ctx, u.ID); err != nil {
			// Per-user tolerance: one bad home never aborts the sweep.
			r.failed++
			fmt.Fprintf(os.Stderr, "disk-usage refresh-all: %s: %v\n", *u.Username, err)
			continue
		}
		r.refreshed++

		// Yield between tenants so the sweep stays gentle even inside the capped
		// slice; skip the pause after the last user.
		if opts.betweenUsers > 0 && i < len(users)-1 {
			select {
			case <-ctx.Done():
				return r, ctx.Err()
			case <-time.After(opts.betweenUsers):
			}
		}
	}
	return r, nil
}
