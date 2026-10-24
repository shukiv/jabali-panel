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

	done, ok := r.cancel("job")
	if !ok {
		t.Fatal("cancel of a running job returned false")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("cancel did not cancel the job's context")
	}
	select {
	case <-done:
		t.Fatal("done closed before the job finished")
	default:
	}
	r.finish("job", ctx.Err())
	select {
	case <-done:
	default:
		t.Fatal("finish did not close done")
	}
	if out, _ := r.lookup("job"); out.State != backupJobCancelled {
		t.Fatalf("cancelled job state = %q, want cancelled", out.State)
	}
	if _, ok := r.cancel("job"); ok {
		t.Fatal("cancel of a finished job returned true")
	}
	if _, ok := r.cancel("unknown"); ok {
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
	if got := res.(backupCancelResult); !got.Stopped || got.JobState != backupJobCancelled {
		t.Fatalf("cancel reply = %+v, want stopped and cancelled", got)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("backup.cancel did not stop the job")
	}
	if st := callBackupStatus(t, jobID); st.JobState != backupJobCancelled {
		t.Fatalf("job_state after cancel = %q, want cancelled", st.JobState)
	}

	// A second cancel finds nothing running, and says how the job ended.
	res, _ = backupCancelHandler(context.Background(), raw)
	if got := res.(backupCancelResult); got.Stopped || got.JobState != backupJobCancelled {
		t.Fatalf("second cancel reply = %+v, want not stopped, job_state cancelled", got)
	}

	// No record at all: job_state is empty, so the panel knows nothing runs.
	other, _ := json.Marshal(map[string]string{"job_id": "01HZ2245000000000000000009"})
	res, _ = backupCancelHandler(context.Background(), other)
	if got := res.(backupCancelResult); got.Stopped || got.JobState != "" {
		t.Fatalf("unknown job cancel reply = %+v, want not stopped, no job_state", got)
	}
}

// jsonKinds maps each top-level key of a JSON object to its JSON kind
// (string, bool, number, array, object, null).
func jsonKinds(t *testing.T, raw []byte) map[string]string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	out := map[string]string{}
	for k, v := range m {
		switch v.(type) {
		case string:
			out[k] = "string"
		case bool:
			out[k] = "bool"
		case float64:
			out[k] = "number"
		case []any:
			out[k] = "array"
		case map[string]any:
			out[k] = "object"
		default:
			out[k] = "null"
		}
	}
	return out
}

// assertWireMatchesFixture pins a reply's keys and their JSON kinds to the
// fixture panel-api decodes (GH #2245 contract: backupfinalizer.agentStatus
// and api.agentCancelReply read these same files).
func assertWireMatchesFixture(t *testing.T, reply any, fixture string) {
	t.Helper()
	got, err := json.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	gk, wk := jsonKinds(t, got), jsonKinds(t, want)
	for k, kind := range wk {
		if gk[k] != kind {
			t.Errorf("%s: key %q is %q on the wire, fixture says %q", fixture, k, gk[k], kind)
		}
	}
	for k := range gk {
		if _, ok := wk[k]; !ok {
			t.Errorf("%s: reply has key %q the fixture (and so panel-api) does not know", fixture, k)
		}
	}
}

func TestBackupStatusAndCancel_WireMatchesPanelFixtures(t *testing.T) {
	const failedID = "01HZ2245000000000000000005"
	defaultBackupJobs.start(failedID, time.Minute)
	defaultBackupJobs.finish(failedID, &backupDestinationError{err: errors.New("ssh: connect to host 203.0.113.1 port 22: Connection timed out")})
	raw, _ := json.Marshal(map[string]string{"job_id": failedID})
	st, err := backupStatusHandler(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	assertWireMatchesFixture(t, st, "backup_status_failed_reply.json")

	const runningID = "01HZ2245000000000000000006"
	ctx := defaultBackupJobs.start(runningID, time.Minute)
	go func() {
		<-ctx.Done()
		defaultBackupJobs.finish(runningID, ctx.Err())
	}()
	raw, _ = json.Marshal(map[string]string{"job_id": runningID})
	res, err := backupCancelHandler(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	assertWireMatchesFixture(t, res, "backup_cancel_reply.json")
}

// stubBackupJob runs fn in place of the backup orchestrator for one test.
func stubBackupJob(t *testing.T, fn func(context.Context, backupCreateParams) error) {
	t.Helper()
	prev := runBackupJob
	runBackupJob = fn
	t.Cleanup(func() { runBackupJob = prev })
}

// GH #2245 follow-up: Cancel answered as soon as it had asked the job to
// stop, so the panel showed Cancelled while restic was still unwinding, and a
// delete right after found the job slot held ("another backup operation (…)
// is running on this repository"). backup.cancel now answers once the job has
// stopped and its slot is free.
func TestBackupCancel_AnswersOnceTheJobHasStoppedAndFreedItsSlot(t *testing.T) {
	const (
		jobID  = "01HZ2245000000000000000010"
		userID = "01HZ2245000000000000000011"
		repo   = "/var/backups/gh2245-cancel-wait"
	)
	unwound := make(chan struct{})
	stubBackupJob(t, func(ctx context.Context, _ backupCreateParams) error {
		<-ctx.Done()
		time.Sleep(200 * time.Millisecond) // restic cleaning up after SIGINT
		close(unwound)
		return ctx.Err()
	})
	raw, _ := json.Marshal(map[string]string{"job_id": jobID, "user_id": userID, "username": "gh2245", "repo_url": repo})
	if _, err := backupCreateHandler(context.Background(), raw); err != nil {
		t.Fatalf("backup.create: %v", err)
	}

	raw, _ = json.Marshal(map[string]string{"job_id": jobID})
	res, err := backupCancelHandler(context.Background(), raw)
	if err != nil {
		t.Fatalf("backup.cancel: %v", err)
	}
	if got := res.(backupCancelResult); !got.Stopped || got.JobState != backupJobCancelled {
		t.Fatalf("cancel reply = %+v, want stopped and cancelled", got)
	}
	select {
	case <-unwound:
	default:
		t.Fatal("backup.cancel answered before the job finished unwinding")
	}
	if existing, ok := defaultJobSlots.acquire("backup", userID, repo, "01HZ2245000000000000000012"); !ok {
		t.Fatalf("job slot still held by %s after backup.cancel answered", existing)
	}
	defaultJobSlots.release("backup", userID, repo)
}

// A job that has not stopped by backupCancelWait is reported still running,
// so the panel says it is cancelling instead of claiming it stopped.
func TestBackupCancel_JobStillStopping_ReportsRunning(t *testing.T) {
	const jobID = "01HZ2245000000000000000013"
	prev := backupCancelWait
	backupCancelWait = 50 * time.Millisecond
	t.Cleanup(func() { backupCancelWait = prev })

	ctx := defaultBackupJobs.start(jobID, time.Minute)
	release := make(chan struct{})
	go func() {
		<-ctx.Done()
		<-release
		defaultBackupJobs.finish(jobID, ctx.Err())
	}()
	defer close(release)

	raw, _ := json.Marshal(map[string]string{"job_id": jobID})
	res, err := backupCancelHandler(context.Background(), raw)
	if err != nil {
		t.Fatalf("backup.cancel: %v", err)
	}
	if got := res.(backupCancelResult); !got.Stopped || got.JobState != backupJobRunning {
		t.Fatalf("cancel reply = %+v, want stopped=true while the job is still running", got)
	}
}
