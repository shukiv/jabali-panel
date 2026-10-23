package backupfinalizer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #2245: a backup whose destination was down failed on the agent within
// seconds, but the row stayed "running" until the 4h stall sweep, and a
// cancelled one never left "running" either. backup.status now reports the
// agent's own outcome (job_state), and the finalizer seals on it.

func TestCheckOne_AgentFailed_SealsFailedWithTheReason(t *testing.T) {
	f, jobs := newTestFinalizer(`{"manifest_found":false,"job_state":"failed",` +
		`"error":"stage home: restic: exit status 1","destination_error":false}`)
	dest := "d1"
	j := runningJob()
	j.DestinationID = &dest
	f.deps.Destinations = &countingDests{}

	trip := f.checkOne(context.Background(), j)

	if jobs.gotStatus != models.BackupJobStatusFailed {
		t.Fatalf("status = %q, want failed", jobs.gotStatus)
	}
	if jobs.gotErrText != "stage home: restic: exit status 1" {
		t.Errorf("error_text = %q, want the agent's reason", jobs.gotErrText)
	}
	if trip != "" {
		t.Errorf("a stage failure tripped destination %q; only a destination failure counts", trip)
	}
}

func TestCheckOne_AgentDestinationFailure_TripsTheBreaker(t *testing.T) {
	f, jobs := newTestFinalizer(`{"manifest_found":false,"job_state":"failed",` +
		`"error":"ensure repo: ssh: connect to host 203.0.113.1 port 22: Connection timed out",` +
		`"destination_error":true}`)
	dest := "d1"
	j := runningJob()
	j.DestinationID = &dest
	f.deps.Destinations = &countingDests{}

	trip := f.checkOne(context.Background(), j)

	if jobs.gotStatus != models.BackupJobStatusFailed {
		t.Fatalf("status = %q, want failed", jobs.gotStatus)
	}
	if trip != dest {
		t.Errorf("destination to trip = %q, want %q", trip, dest)
	}
}

func TestCheckOne_AgentCancelled_SealsCancelled(t *testing.T) {
	f, jobs := newTestFinalizer(`{"manifest_found":false,"job_state":"cancelled"}`)
	f.checkOne(context.Background(), runningJob())
	if jobs.gotStatus != models.BackupJobStatusCancelled {
		t.Fatalf("status = %q, want cancelled", jobs.gotStatus)
	}
}

func TestCheckOne_AgentRunning_NoOp(t *testing.T) {
	f, jobs := newTestFinalizer(`{"manifest_found":false,"job_state":"running",` +
		`"agent_started_at":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}`)
	f.checkOne(context.Background(), runningJob())
	if jobs.called {
		t.Fatalf("a running job was sealed %q", jobs.gotStatus)
	}
}

// The agent restarted after the job started and has no record of it: the
// backup goroutine died with the old process, so the job can never finish.
func TestCheckOne_AgentRestartedMidJob_SealsInterrupted(t *testing.T) {
	f, jobs := newTestFinalizer(`{"manifest_found":false,` +
		`"agent_started_at":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}`)
	f.checkOne(context.Background(), runningJob()) // started a minute ago
	if jobs.gotStatus != models.BackupJobStatusFailed || jobs.gotErrText != agentRestartedText {
		t.Fatalf("got status=%q error=%q, want failed %q", jobs.gotStatus, jobs.gotErrText, agentRestartedText)
	}
}

// An agent that was already up when the job started is not the restart
// case; without a record or a manifest the job is left for the next tick.
func TestCheckOne_AgentStartedBeforeJob_NoOp(t *testing.T) {
	f, jobs := newTestFinalizer(`{"manifest_found":false,` +
		`"agent_started_at":"` + time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano) + `"}`)
	f.checkOne(context.Background(), runningJob())
	if jobs.called {
		t.Fatalf("job sealed %q although the agent started before it", jobs.gotStatus)
	}
}

// An older agent sends none of the GH #2245 fields. Its reply must keep the
// old meaning: no manifest yet = still running.
func TestCheckOne_OlderAgentReply_NoOp(t *testing.T) {
	f, jobs := newTestFinalizer(`{"job_id":"01HJOBJOBJOBJOBJOBJOBJOB00","stages":["home"],"manifest_found":false,"snapshots":[]}`)
	j := runningJob()
	old := time.Now().Add(-2 * time.Hour)
	j.StartedAt = &old
	f.checkOne(context.Background(), j)
	if jobs.called {
		t.Fatalf("older agent reply sealed the job %q", jobs.gotStatus)
	}
}

type countingDests struct {
	repository.BackupDestinationRepository
	recordFailures map[string]int
}

func (d *countingDests) Get(_ context.Context, id string) (*models.BackupDestination, error) {
	return &models.BackupDestination{ID: id, Kind: "sftp", URL: "sftp:backup@203.0.113.1:/repo"}, nil
}

func (d *countingDests) RecordFailure(_ context.Context, id string, _ int, _ time.Time) error {
	if d.recordFailures == nil {
		d.recordFailures = map[string]int{}
	}
	d.recordFailures[id]++
	return nil
}

// A schedule fans out one job per account. When their destination is down
// they all fail in the same tick; the breaker must count that outage once,
// not once per account.
func TestTickOnce_DestinationOutageTripsTheBreakerOncePerTick(t *testing.T) {
	dest := "d1"
	started := time.Now().Add(-time.Minute)
	var running []models.BackupJob
	for _, id := range []string{"j1", "j2", "j3"} {
		running = append(running, models.BackupJob{
			ID: id, Kind: models.BackupJobKindAccountBackup, Status: models.BackupJobStatusRunning,
			StartedAt: &started, DestinationID: &dest,
		})
	}
	jobs := &breakerJobs{running: running}
	dests := &countingDests{}
	f := &Finalizer{deps: Deps{
		Jobs:         jobs,
		Destinations: dests,
		Agent: &fakeStatusAgent{reply: `{"manifest_found":false,"job_state":"failed",` +
			`"error":"ensure repo: dial tcp 203.0.113.1:22: i/o timeout","destination_error":true}`},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}

	f.tickOnce(context.Background())

	if len(jobs.finished) != 3 {
		t.Fatalf("finished = %v, want all three jobs sealed", jobs.finished)
	}
	if got := dests.recordFailures[dest]; got != 1 {
		t.Fatalf("breaker tripped %d times for one outage, want 1", got)
	}
}

// Contract with the agent: decode the fixture the agent's own test pins its
// backup.status reply to (panel-agent commands/backup_job_registry_test.go).
// An unknown key means the agent sends something agentStatus does not read.
func TestAgentStatus_MatchesTheAgentFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "panel-agent", "internal", "commands", "testdata", "backup_status_failed_reply.json"))
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var st agentStatus
	if err := dec.Decode(&st); err != nil {
		t.Fatalf("agent backup.status reply does not decode into agentStatus: %v", err)
	}
	if st.JobState != agentJobFailed || st.Error == "" || !st.DestinationError || st.AgentStartedAt == nil {
		t.Fatalf("decoded %+v, want failed with an error, destination_error and agent_started_at", st)
	}
}
