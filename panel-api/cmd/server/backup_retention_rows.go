package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// Stale backup rows.
//
// Sweeps before whole-job retention forgot snapshots but never deleted the
// backup_jobs rows of the backups they removed. Those rows are still listed in
// the panel as backups to restore, and a restore of one fails because its
// snapshots are gone; they also count toward a plan's backup limit. Found on
// the fleet: 2,659 rows against 42 backups in one repository, 4,201 against
// 121 in another.
//
// So after the forget pass the sweep lists every snapshot in the destination
// and deletes the row of each finished backup that has nothing left there.

// staleRowMinAge is how long before the snapshot listing a backup must have
// finished for its row to be judged. A backup that finished after the listing
// was taken would otherwise look as if it had no snapshots.
const staleRowMinAge = time.Hour

// deleteStaleBackupRows deletes the row of every finished (succeeded or
// partial) account or system backup on destination r of which no snapshot is
// left in its repository: none tagged with its job-id, and none matching the
// row's snapshot_id (which covers snapshots written before job-id tags).
//
// It judges nothing when the repository lists no snapshots at all (a wrong
// path or credentials would look the same as an empty repository), or when
// the listing fails.
func deleteStaleBackupRows(ctx context.Context, cmd *cobra.Command, r resticRepo, jobs retentionJobStore, dryRun bool) error {
	out := cmd.OutOrStdout()
	listedAt := time.Now()
	raw, err := runResticCapture(ctx, cmd, r, append(r.args(), "snapshots", "--json"))
	if err != nil {
		return fmt.Errorf("list snapshots: %w", err)
	}
	var snaps []resticSnapshot
	if err := json.Unmarshal(bytes.TrimSpace(raw), &snaps); err != nil {
		return fmt.Errorf("parse restic snapshots: %w", err)
	}
	if len(snaps) == 0 {
		fmt.Fprintf(out, "dest %s (%s): the repository lists no snapshots; its backup rows are left alone\n", r.ID, r.Name)
		return nil
	}
	jobIDs := make(map[string]bool, len(snaps))
	ids := make(map[string]bool, len(snaps))
	for _, s := range snaps {
		ids[s.ID] = true
		if j := tagValue(s.Tags, internalbackup.TagKeyJobID); j != "" {
			jobIDs[j] = true
		}
	}

	rows, err := jobs.ListFinishedBackupsForDestination(ctx, r.ID, listedAt.Add(-staleRowMinAge))
	if err != nil {
		return fmt.Errorf("list backup rows: %w", err)
	}
	var stale []models.BackupJob
	for _, row := range rows {
		if jobIDs[row.ID] || (row.SnapshotID != "" && snapshotPresent(ids, row.SnapshotID)) {
			continue
		}
		stale = append(stale, row)
	}
	fmt.Fprintf(out, "dest %s (%s): %d finished backup row(s), %d with no snapshot left in the repository\n",
		r.ID, r.Name, len(rows), len(stale))

	failed := 0
	for _, row := range stale {
		if dryRun {
			fmt.Fprintf(out, "[dry-run] would delete the row of backup %s (%s, %s)\n", row.ID, row.Kind, rowTime(row))
			continue
		}
		if err := jobs.Delete(ctx, row.ID); err != nil {
			failed++
			fmt.Fprintf(cmd.ErrOrStderr(), "delete the row of backup %s: %v\n", row.ID, err)
			continue
		}
		fmt.Fprintf(out, "deleted the row of backup %s (%s, %s): no snapshot left\n", row.ID, row.Kind, rowTime(row))
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d stale backup row(s) could not be deleted", failed, len(stale))
	}
	return nil
}

func rowTime(j models.BackupJob) string {
	t := j.CreatedAt
	if j.FinishedAt != nil {
		t = *j.FinishedAt
	}
	return t.UTC().Format(time.RFC3339)
}
