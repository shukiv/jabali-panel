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
//
// Failed and cancelled backups are judged too, once they are older than
// failedRowMinAge. Nothing else ever deletes their rows: a run that failed
// before it wrote a snapshot has nothing for the keep rules to forget, so its
// row stayed in the panel forever (64 on one fleet box). The age keeps recent
// failures, and their errors, visible in the panel.
//
// A backup whose manifest is gone but whose data snapshots remain loses its row
// too. Restoring from the panel starts from the manifest, so such a backup is
// listed but cannot be restored; the old per-stage sweep left many of these.
// Its data snapshots stay in the repository, where the keep rules still count
// them and forget them when they age out.

// staleRowMinAge is how long before the snapshot listing a backup must have
// finished for its row to be judged. A backup that finished after the listing
// was taken would otherwise look as if it had no snapshots.
const staleRowMinAge = time.Hour

// failedRowMinAge is how old a failed or cancelled backup must be for its row
// to be judged.
const failedRowMinAge = 30 * 24 * time.Hour

// deleteStaleBackupRows deletes the row of every finished account or system
// backup on destination r of which no snapshot is left in its repository: none
// tagged with its job-id, and none matching the row's snapshot_id (which
// covers snapshots written before job-id tags). It also deletes the row of a
// backup whose manifest is gone: the row has a snapshot_id (the manifest the
// backup wrote), that snapshot is not in the repository, and no snapshot with
// its job-id is a manifest. Succeeded and partial backups are judged once they
// finished staleRowMinAge before the listing; failed and cancelled ones once
// they are failedRowMinAge old.
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
	manifestJobIDs := make(map[string]bool)
	ids := make(map[string]bool, len(snaps))
	for _, s := range snaps {
		ids[s.ID] = true
		if j := tagValue(s.Tags, internalbackup.TagKeyJobID); j != "" {
			jobIDs[j] = true
			if tagValue(s.Tags, internalbackup.TagKeyStage) == internalbackup.StageManifest {
				manifestJobIDs[j] = true
			}
		}
	}

	rows, err := jobs.ListFinishedBackupsForDestination(ctx, r.ID,
		[]string{models.BackupJobStatusSucceeded, models.BackupJobStatusPartial}, listedAt.Add(-staleRowMinAge))
	if err != nil {
		return fmt.Errorf("list backup rows: %w", err)
	}
	failedRows, err := jobs.ListFinishedBackupsForDestination(ctx, r.ID,
		[]string{models.BackupJobStatusFailed, models.BackupJobStatusCancelled}, listedAt.Add(-failedRowMinAge))
	if err != nil {
		return fmt.Errorf("list failed backup rows: %w", err)
	}
	rows = append(rows, failedRows...)
	type staleRow struct {
		models.BackupJob
		why string
	}
	var stale []staleRow
	noSnapshot, manifestGone := 0, 0
	for _, row := range rows {
		if manifestJobIDs[row.ID] || (row.SnapshotID != "" && snapshotPresent(ids, row.SnapshotID)) {
			continue
		}
		switch {
		case !jobIDs[row.ID]:
			noSnapshot++
			stale = append(stale, staleRow{row, "no snapshot left"})
		case row.SnapshotID != "":
			manifestGone++
			stale = append(stale, staleRow{row, "its manifest is gone; its data snapshots stay"})
		}
	}
	fmt.Fprintf(out, "dest %s (%s): %d finished backup row(s) (%d failed or cancelled over %d days ago), %d with no snapshot left in the repository, %d whose manifest is gone\n",
		r.ID, r.Name, len(rows), len(failedRows), int(failedRowMinAge/(24*time.Hour)), noSnapshot, manifestGone)

	failed := 0
	for _, row := range stale {
		if dryRun {
			fmt.Fprintf(out, "[dry-run] would delete the row of backup %s (%s, %s, %s): %s\n", row.ID, row.Kind, row.Status, rowTime(row.BackupJob), row.why)
			continue
		}
		if err := jobs.Delete(ctx, row.ID); err != nil {
			failed++
			fmt.Fprintf(cmd.ErrOrStderr(), "delete the row of backup %s: %v\n", row.ID, err)
			continue
		}
		fmt.Fprintf(out, "deleted the row of backup %s (%s, %s, %s): %s\n", row.ID, row.Kind, row.Status, rowTime(row.BackupJob), row.why)
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
