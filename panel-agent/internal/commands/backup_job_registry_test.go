package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// GH #2245: Cancel in the panel stopped nothing (backup.cancel was a no-op),
// and a backup that failed against a destination that was down stayed
// "running" for 4h because backup.status could only answer by asking that
// same destination's repo.

func TestBackupJobRegistry_Outcomes(t *testing.T) {
	r := newBackupJobRegistry()

	r.start("ok", time.Minute)
	if out, _ := r.lookup("ok"); out.State != backupJobRunning {
		t.Fatalf("started job state = %q, want running", out.State)
	}
	r.finish("ok", nil)
	if out, _ := r.lookup("ok"); out.State != backupJobSucceeded || out.Err != "" {
		t.Fatalf("finished job = %+v, want succeeded with no error", out)
	}

	r.start("stage", time.Minute)
	r.finish("stage", errors.New("manifest snapshot: boom"))
	out, _ := r.lookup("stage")
	if out.State != backupJobFailed || out.Err != "manifest snapshot: boom" || out.DestinationErr {
		t.Fatalf("stage failure = %+v, want failed, error kept, not a destination error", out)
	}

	r.start("dest", time.Minute)
	r.finish("dest", &backupDestinationError{err: errors.New("dial tcp: i/o timeout")})
	out, _ = r.lookup("dest")
	if out.State != backupJobFailed || !out.DestinationErr || out.Err != "ensure repo: dial tcp: i/o timeout" {
		t.Fatalf("destination failure = %+v, want failed destination error", out)
	}

	if _, ok := r.lookup("never-started"); ok {
		t.Fatal("lookup of an unknown job reported a record")
	}
}

func TestBackupJobRegistry_CancelStopsOnlyRunningJobs(t *testing.T) {
	r := newBackupJobRegistry()
	ctx := r.start("job", time.Minute)

	if !r.cancel("job") {
		t.Fatal("cancel of a running job returned false")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("cancel did not cancel the job's context")
	}
	r.finish("job", ctx.Err())
	if out, _ := r.lookup("job"); out.State != backupJobCancelled {
		t.Fatalf("cancelled job state = %q, want cancelled", out.State)
	}
	if r.cancel("job") {
		t.Fatal("cancel of a finished job returned true")
	}
	if r.cancel("unknown") {
		t.Fatal("cancel of an unknown job returned true")
	}
}

func TestBackupJobRegistry_TimeoutNamesTheLimit(t *testing.T) {
	r := newBackupJobRegistry()
	ctx := r.start("slow", 10*time.Millisecond)
	<-ctx.Done()
	r.finish("slow", ctx.Err())
	out, _ := r.lookup("slow")
	if out.State != backupJobFailed || !strings.HasPrefix(out.Err, "stopped after the 10ms limit: ") {
		t.Fatalf("timed-out job = %+v, want failed naming the 10ms limit", out)
	}
}

func TestBackupJobRegistry_PrunesOldFinishedJobs(t *testing.T) {
	r := newBackupJobRegistry()
	now := time.Now()
	r.now = func() time.Time { return now }
	r.start("old", time.Minute)
	r.finish("old", nil)
	r.start("still-running", time.Minute)

	now = now.Add(backupJobKeep + time.Minute)
	r.start("new", time.Minute)

	if _, ok := r.lookup("old"); ok {
		t.Fatal("finished job older than backupJobKeep was kept")
	}
	if _, ok := r.lookup("still-running"); !ok {
		t.Fatal("a running job was pruned")
	}
}

func callBackupStatus(t *testing.T, jobID string) backupStatusResult {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"job_id": jobID})
	res, err := backupStatusHandler(context.Background(), raw)
	if err != nil {
		t.Fatalf("backup.status: %v", err)
	}
	// Round-trip through JSON: the panel reads the wire form.
	b, _ := json.Marshal(res)
	var st backupStatusResult
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatalf("decode backup.status: %v", err)
	}
	return st
}

// A backup whose destination cannot be opened fails in seconds. backup.status
// must say so (with the reason) without asking that destination's repo.
func TestBackupStatus_ReportsFailedJobWithoutTheRepo(t *testing.T) {
	const jobID = "01HZ2245000000000000000001"
	// An unreadable credentials file fails ensure-repo before any lock or
	// restic call, so the test needs neither root nor restic.
	creds := filepath.Join(t.TempDir(), "creds.env")
	if err := os.WriteFile(creds, []byte("not-an-env-line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{
		"job_id":          jobID,
		"user_id":         "01HZ2245000000000000000002",
		"username":        "gh2245",
		"repo_url":        "sftp:backup@203.0.113.1:/gh2245",
		"credentials_ref": creds,
	})
	if _, err := backupCreateHandler(context.Background(), raw); err != nil {
		t.Fatalf("backup.create: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	var st backupStatusResult
	for {
		st = callBackupStatus(t, jobID)
		if st.JobState != backupJobRunning || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st.JobState != backupJobFailed {
		t.Fatalf("job_state = %q, want failed", st.JobState)
	}
	if !st.DestinationError {
		t.Error("destination_error = false, want true for an ensure-repo failure")
	}
	if !strings.Contains(st.Error, "load creds") {
		t.Errorf("error = %q, want the ensure-repo reason", st.Error)
	}
	if st.AgentStartedAt.IsZero() {
		t.Error("agent_started_at missing")
	}
}

// Cancel in the panel calls backup.cancel. It must stop the job's goroutine,
// and backup.status must then report it cancelled.
func TestBackupCancel_StopsTheRunningJob(t *testing.T) {
	const jobID = "01HZ2245000000000000000003"
	ctx := defaultBackupJobs.start(jobID, time.Minute)
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		defaultBackupJobs.finish(jobID, ctx.Err())
		close(done)
	}()

	raw, _ := json.Marshal(map[string]string{"job_id": jobID})
	res, err := backupCancelHandler(context.Background(), raw)
	if err != nil {
		t.Fatalf("backup.cancel: %v", err)
	}
	if got := res.(map[string]any)["stopped"]; got != true {
		t.Fatalf("stopped = %v, want true", got)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("backup.cancel did not stop the job")
	}
	if st := callBackupStatus(t, jobID); st.JobState != backupJobCancelled {
		t.Fatalf("job_state after cancel = %q, want cancelled", st.JobState)
	}

	// A second cancel finds nothing running.
	res, _ = backupCancelHandler(context.Background(), raw)
	if got := res.(map[string]any)["stopped"]; got != false {
		t.Fatalf("second cancel stopped = %v, want false", got)
	}
}
