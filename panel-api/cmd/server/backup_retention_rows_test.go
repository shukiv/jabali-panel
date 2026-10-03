package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sort"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func finishedRow(id, dest, snapshotID string, finished time.Time) *models.BackupJob {
	return &models.BackupJob{ID: id, Kind: models.BackupJobKindAccountBackup, Status: models.BackupJobStatusSucceeded,
		DestinationID: &dest, SnapshotID: snapshotID, CreatedAt: finished.Add(-time.Minute), FinishedAt: &finished}
}

func TestDeleteStaleBackupRows_DeletesOnlyRowsWithNothingLeft(t *testing.T) {
	snaps := jobSet("J1", "u1", "2026-01-01T01:00:00Z", "2026-01-01T01:01:00Z", "2026-01-01T01:02:00Z")
	snaps = append(snaps, resticSnapshot{ID: fullID('u'), Time: time.Now(), Hostname: "box", Tags: []string{"stage=home"}}) // no job-id
	calls := fakeRestic(t, snaps)
	old := time.Now().Add(-48 * time.Hour)
	jobs := &fakeJobStore{rows: map[string]*models.BackupJob{
		"J1": finishedRow("J1", "d1", "", old),          // its snapshots are there
		"J2": finishedRow("J2", "d1", "", old),          // nothing left: stale
		"J3": finishedRow("J3", "d1", fullID('u'), old), // untagged snapshot still there by ID
		"J4": finishedRow("J4", "d2", "", old),          // another destination
	}}
	cmd := newRetentionTestCmd()
	if err := deleteStaleBackupRows(context.Background(), cmd, testDest(), jobs, false); err != nil {
		t.Fatal(err)
	}
	if !eq(jobs.deleted, []string{"J2"}) {
		t.Fatalf("deleted = %v, want only J2", jobs.deleted)
	}
	if limit := time.Now().Add(-staleRowMinAge); jobs.listedBefore[models.BackupJobStatusSucceeded].After(limit.Add(time.Second)) {
		t.Errorf("rows were judged up to %v; a backup must have finished an hour before the listing", jobs.listedBefore)
	}
	for _, c := range *calls {
		if hasArg(c, "snapshots") && hasArg(c, "--tag") {
			t.Errorf("the listing must cover every snapshot in the repository, not one tag: %v", c)
		}
	}
	if out := cmd.OutOrStdout().(*bytes.Buffer).String(); !strings.Contains(out, "deleted the row of backup J2") {
		t.Errorf("each deleted row should be logged, got:\n%s", out)
	}
}

func TestDeleteStaleBackupRows_EmptyRepositoryJudgesNothing(t *testing.T) {
	fakeRestic(t, []resticSnapshot{})
	jobs := &fakeJobStore{rows: map[string]*models.BackupJob{
		"J1": finishedRow("J1", "d1", "", time.Now().Add(-48*time.Hour)),
	}}
	if err := deleteStaleBackupRows(context.Background(), newRetentionTestCmd(), testDest(), jobs, false); err != nil {
		t.Fatal(err)
	}
	if len(jobs.deleted) != 0 {
		t.Errorf("an empty repository (or a wrong path) must not delete rows: %v", jobs.deleted)
	}
}

func TestDeleteStaleBackupRows_DryRunAndListingFailureDeleteNothing(t *testing.T) {
	fakeRestic(t, jobSet("J1", "u1", "2026-01-01T01:00:00Z", "2026-01-01T01:01:00Z", "2026-01-01T01:02:00Z"))
	jobs := &fakeJobStore{rows: map[string]*models.BackupJob{
		"J2": finishedRow("J2", "d1", "", time.Now().Add(-48*time.Hour)),
	}}
	cmd := newRetentionTestCmd()
	if err := deleteStaleBackupRows(context.Background(), cmd, testDest(), jobs, true); err != nil {
		t.Fatal(err)
	}
	if len(jobs.deleted) != 0 {
		t.Errorf("dry-run must not delete: %v", jobs.deleted)
	}
	if out := cmd.OutOrStdout().(*bytes.Buffer).String(); !strings.Contains(out, "[dry-run] would delete the row of backup J2") {
		t.Errorf("dry-run should list the rows it would delete, got:\n%s", out)
	}

	orig := retentionExec
	t.Cleanup(func() { retentionExec = orig })
	retentionExec = func(_ context.Context, _ []string, _, _ io.Writer, _ string, _ ...string) error {
		return errors.New("Fatal: unable to open repository")
	}
	if err := deleteStaleBackupRows(context.Background(), newRetentionTestCmd(), testDest(), jobs, false); err == nil {
		t.Error("a failed listing must be reported")
	}
	if len(jobs.deleted) != 0 {
		t.Errorf("a failed listing must not delete rows: %v", jobs.deleted)
	}
}

func failedRow(id, dest, status string, finished time.Time) *models.BackupJob {
	r := finishedRow(id, dest, "", finished)
	r.Status = status
	return r
}

// Failed and cancelled backups' rows are judged once they are 30 days old: a
// run that failed before it wrote a snapshot has nothing for the keep rules to
// forget, so nothing else ever deletes its row. Younger ones stay visible, and
// one whose snapshots are still in the repository stays with them.
func TestDeleteStaleBackupRows_OldFailedAndCancelledRows(t *testing.T) {
	snaps := jobSet("J1", "u1", "2026-01-01T01:00:00Z", "2026-01-01T01:01:00Z", "2026-01-01T01:02:00Z")
	snaps = append(snaps, snap("p-home", "PARTIALDATA", "u1", "home", "2026-01-02T01:00:00Z"))
	fakeRestic(t, snaps)
	old, recent := time.Now().Add(-40*24*time.Hour), time.Now().Add(-10*24*time.Hour)
	jobs := &fakeJobStore{rows: map[string]*models.BackupJob{
		"OLDFAIL":     failedRow("OLDFAIL", "d1", models.BackupJobStatusFailed, old),
		"OLDCANCEL":   failedRow("OLDCANCEL", "d1", models.BackupJobStatusCancelled, old),
		"NEWFAIL":     failedRow("NEWFAIL", "d1", models.BackupJobStatusFailed, recent),
		"PARTIALDATA": failedRow("PARTIALDATA", "d1", models.BackupJobStatusFailed, old), // its home snapshot is there
		"RUNNING":     failedRow("RUNNING", "d1", models.BackupJobStatusRunning, old),
	}}
	cmd := newRetentionTestCmd()
	if err := deleteStaleBackupRows(context.Background(), cmd, testDest(), jobs, false); err != nil {
		t.Fatal(err)
	}
	sort.Strings(jobs.deleted)
	if !eq(jobs.deleted, []string{"OLDCANCEL", "OLDFAIL"}) {
		t.Fatalf("deleted = %v, want OLDCANCEL and OLDFAIL", jobs.deleted)
	}
	if got, want := jobs.listedBefore[models.BackupJobStatusFailed], time.Now().Add(-failedRowMinAge); got.After(want.Add(time.Second)) {
		t.Errorf("failed rows were judged up to %v; they must be %v old", got, failedRowMinAge)
	}
	if out := cmd.OutOrStdout().(*bytes.Buffer).String(); !strings.Contains(out, "deleted the row of backup OLDFAIL (account_backup, failed,") {
		t.Errorf("a deleted row should be logged with its status, got:\n%s", out)
	}
}

// The sweep cleans the stale rows of each destination it forgot from, before
// its prune.
func TestFinishRetention_DeletesStaleRowsBeforePrune(t *testing.T) {
	alerts := recordRetentionAlerts(t)
	calls := fakeRestic(t, jobSet("J1", "u1", "2026-01-01T01:00:00Z", "2026-01-01T01:01:00Z", "2026-01-01T01:02:00Z"))
	jobs := &fakeJobStore{rows: map[string]*models.BackupJob{
		"J2": finishedRow("J2", "d1", "", time.Now().Add(-48*time.Hour)),
	}}
	d := testDest()
	if err := finishRetention(context.Background(), newRetentionTestCmd(), map[string]resticRepo{d.ID: d}, jobs, nil, false); err != nil {
		t.Fatal(err)
	}
	if !eq(jobs.deleted, []string{"J2"}) {
		t.Errorf("deleted = %v, want J2", jobs.deleted)
	}
	if len(*alerts) != 0 {
		t.Errorf("no failure, no alert: %v", *alerts)
	}
	listed, pruned := -1, -1
	for i, c := range *calls {
		if hasArg(c, "snapshots") && listed < 0 {
			listed = i
		}
		if hasArg(c, "prune") {
			pruned = i
		}
	}
	if listed < 0 || pruned < 0 || listed > pruned {
		t.Errorf("want the snapshot listing before the prune, calls = %v", *calls)
	}
}

// End to end on a real repository: the row of a backup forgotten earlier
// (outside this run) is deleted; the row of a backup still there is kept.
func TestDeleteStaleBackupRows_RealRestic(t *testing.T) {
	r := newTestRepo(t)
	for _, j := range []struct{ id, at string }{{"R1", "2026-01-01 10:00:00"}, {"R2", "2026-01-02 10:00:00"}} {
		r.backupStage(t, j.id, "home", j.at)
		r.backupStage(t, j.id, "manifest", j.at)
	}
	var r1 []string
	for _, s := range r.snapshots(t) {
		if tagValue(s.Tags, "job-id") == "R1" {
			r1 = append(r1, s.ID)
		}
	}
	r.run(t, "", append([]string{"forget"}, r1...)...)

	old := time.Now().Add(-48 * time.Hour)
	jobs := &fakeJobStore{rows: map[string]*models.BackupJob{
		"R1": finishedRow("R1", "d1", "", old),
		"R2": finishedRow("R2", "d1", "", old),
	}}
	dest := resticRepo{&models.BackupDestination{ID: "d1", Name: "local", URL: r.dir}, r.pw}
	if err := deleteStaleBackupRows(context.Background(), newRetentionTestCmd(), dest, jobs, false); err != nil {
		t.Fatal(err)
	}
	if !eq(jobs.deleted, []string{"R1"}) {
		t.Errorf("deleted = %v, want only R1 (its snapshots were forgotten)", jobs.deleted)
	}
}
