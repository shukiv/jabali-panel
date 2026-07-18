// `jabali retention-sweep` — one daily pass that prunes expired rows from the
// unbounded log / report tables that had no retention (JAB-100, 101, 103, 122,
// 123, 125, and the legacy db_admin_audit half of 105). Run by
// jabali-retention-sweep.timer; idempotent; honors --dry-run.
//
// DELIBERATELY EXCLUDED — these need their own design, not a blind DELETE:
//   - audit_events: a hash-chained tamper-evident log (prev_hash/row_hash).
//     Deleting old rows dangles the chain; retention there must archive or
//     re-anchor the chain, so it is out of scope here (the core of JAB-105).
//   - bw_daily: bandwidth billing ledger — JAB-124 wants a rollup decision
//     (aggregate to monthly) rather than a delete, so it is left for that.
//   - migration_jobs / migration_stages: owned by `migrate reap-secrets`
//     (JAB-102) which already reaps terminal jobs + their staging.
package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
	"gorm.io/gorm"
)

const retentionDay = 24 * time.Hour

// retentionTarget is one leaf table pruned by the sweep. Rows whose tsColumn is
// older than now-window are deleted in batches. table + tsColumn are compile-
// time constants from this file only — never user input — so they are safe to
// interpolate into the DELETE.
type retentionTarget struct {
	table    string
	tsColumn string
	window   time.Duration
	why      string
}

// retentionTargets — every entry is a leaf log/report table with no hash chain
// and no active-state rows, so an age-based DELETE is safe. Security-sensitive
// tables (malware, WAF incidents, db-admin audit) get a generous 1-year window
// so forensics survive while disk stays bounded.
var retentionTargets = []retentionTarget{
	{"log_access_streams", "expires_at", 1 * retentionDay, "expired log-view stream keys (JAB-103)"},
	{"terminal_sessions", "created_at", 90 * retentionDay, "root-terminal session rows; .cast files rotate via logrotate (JAB-103)"},
	{"notification_history", "created_at", 90 * retentionDay, "delivered-notification log (JAB-100)"},
	{"update_history", "started_at", 180 * retentionDay, "update-center run history + stored log excerpts (JAB-101)"},
	{"dmarc_aggregate", "created_at", 180 * retentionDay, "DMARC aggregate reports (JAB-125)"},
	{"tlsrpt_aggregate", "created_at", 180 * retentionDay, "TLS-RPT aggregate reports (JAB-125)"},
	{"arf_report", "received_at", 180 * retentionDay, "ARF failure reports (JAB-125)"},
	{"malware_events", "created_at", 365 * retentionDay, "malware raw event log — security, 1y window (JAB-123)"},
	{"snuffleupagus_incidents", "ts", 365 * retentionDay, "Snuffleupagus WAF incidents — security, 1y window (JAB-122)"},
	{"db_admin_audit", "ts", 365 * retentionDay, "legacy DB-admin audit — 1y window (JAB-105; hash-chained audit_events excluded)"},
}

// retentionBatchSize bounds each DELETE so a large backlog can't hold a long
// row lock on the table. The loop repeats until a batch clears fewer rows.
const retentionBatchSize = 5000

// pruneRetentionTarget deletes (or, under dryRun, counts) rows in t older than
// now-window. Returns the number removed / would-remove.
func pruneRetentionTarget(ctx context.Context, db *gorm.DB, t retentionTarget, dryRun bool, out io.Writer) (int64, error) {
	cutoff := time.Now().Add(-t.window)
	if dryRun {
		var n int64
		if err := db.WithContext(ctx).Table(t.table).Where(t.tsColumn+" < ?", cutoff).Count(&n).Error; err != nil {
			return 0, err
		}
		if n > 0 {
			fmt.Fprintf(out, "[dry-run] %s: %d rows older than %s — %s\n", t.table, n, cutoff.Format(time.RFC3339), t.why)
		}
		return n, nil
	}
	var total int64
	for {
		res := db.WithContext(ctx).Exec(
			"DELETE FROM "+t.table+" WHERE "+t.tsColumn+" < ? LIMIT ?", cutoff, retentionBatchSize)
		if res.Error != nil {
			return total, res.Error
		}
		total += res.RowsAffected
		if res.RowsAffected < retentionBatchSize {
			break
		}
	}
	if total > 0 {
		fmt.Fprintf(out, "%s: pruned %d rows older than %s\n", t.table, total, cutoff.Format(time.RFC3339))
	}
	return total, nil
}

func newRetentionSweepCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "retention-sweep",
		Short: "Prune expired rows from unbounded log/report tables",
		Long: `Deletes aged rows from the log/report tables that previously had
no retention (notification_history, update_history, dmarc/tlsrpt/arf reports,
malware_events, snuffleupagus_incidents, db_admin_audit, log_access_streams,
terminal_sessions). Each table has its own window; security tables keep a
1-year window. Batched DELETEs (5000/loop) so a big backlog can't long-lock a
table. Idempotent; run by jabali-retention-sweep.timer daily. --dry-run counts
without deleting.

Not touched here: audit_events (hash-chained — needs archive/re-anchor, not a
blind delete), bw_daily (billing rollup decision), migration_jobs/stages
(owned by 'migrate reap-secrets').`,
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
			defer cancel()
			var grand int64
			for _, t := range retentionTargets {
				n, err := pruneRetentionTarget(ctx, sharedDB, t, dryRun, cmd.OutOrStdout())
				if err != nil {
					// One table failing (e.g. absent on an older schema) must
					// not skip the rest — log and continue.
					fmt.Fprintf(cmd.ErrOrStderr(), "%s: prune failed: %v\n", t.table, err)
					continue
				}
				grand += n
			}
			fmt.Fprintf(cmd.OutOrStdout(), "retention-sweep: %d rows (dry-run=%v)\n", grand, dryRun)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "count would-delete rows without removing them")
	return cmd
}
