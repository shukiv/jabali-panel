package backupfinalizer

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeStatusAgent returns a canned backup.status reply.
type fakeStatusAgent struct{ reply string }

func (a *fakeStatusAgent) Call(_ context.Context, _ string, _ any) (json.RawMessage, error) {
	return json.RawMessage(a.reply), nil
}

// captureJobs records the MarkFinished call. Everything else embeds the
// interface so an unexpected call panics (checkOne must only MarkFinished).
type captureJobs struct {
	repository.BackupJobRepository
	gotStatus   string
	gotErrText  string
	gotWarnings json.RawMessage
	gotBytes    uint64
	called      bool
}

func (j *captureJobs) MarkFinished(_ context.Context, _, status string, _, _ string,
	_, bytesTotal uint64, _, warnings json.RawMessage, errText string) error {
	j.called = true
	j.gotStatus = status
	j.gotErrText = errText
	j.gotWarnings = warnings
	j.gotBytes = bytesTotal
	return nil
}

func newTestFinalizer(agentReply string) (*Finalizer, *captureJobs) {
	jobs := &captureJobs{}
	f := &Finalizer{deps: Deps{
		Jobs:  jobs,
		Agent: &fakeStatusAgent{reply: agentReply},
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}
	return f, jobs
}

func runningJob() models.BackupJob {
	started := time.Now().Add(-time.Minute)
	return models.BackupJob{
		ID:        "01HJOBJOBJOBJOBJOBJOBJOB00",
		UserID:    "01HUSERUSERUSERUSERUSER000",
		Kind:      models.BackupJobKindAccountBackup,
		Status:    models.BackupJobStatusRunning,
		StartedAt: &started,
	}
}

// GH #454: a manifest with a failed stage must downgrade the job to
// "partial" (not "succeeded") and record the reason — otherwise a full
// backup silently missing its home dir reports success.
func TestCheckOne_FailedStage_MarksPartial(t *testing.T) {
	reply := `{"manifest_found":true,"bytes_added":1500000,"bytes_total":1500000,` +
		`"failed_stages":["home"],"warnings":["home: repository is already locked"],` +
		`"snapshots":[{"id":"snapM","tags":["stage=manifest"]}]}`
	f, jobs := newTestFinalizer(reply)

	f.checkOne(context.Background(), runningJob())

	if !jobs.called {
		t.Fatal("MarkFinished was not called")
	}
	if jobs.gotStatus != models.BackupJobStatusPartial {
		t.Errorf("status = %q, want partial", jobs.gotStatus)
	}
	if jobs.gotErrText == "" {
		t.Error("expected an error_text explaining the failed stage")
	}
	var warns []string
	if err := json.Unmarshal(jobs.gotWarnings, &warns); err != nil || len(warns) == 0 {
		t.Errorf("expected warnings JSON, got %q (err %v)", jobs.gotWarnings, err)
	}
}

// A clean manifest (no failed stages) still reports "succeeded".
func TestCheckOne_AllStagesOK_MarksSucceeded(t *testing.T) {
	reply := `{"manifest_found":true,"bytes_added":3600000000,"bytes_total":3600000000,` +
		`"snapshots":[{"id":"snapM","tags":["stage=manifest"]}]}`
	f, jobs := newTestFinalizer(reply)

	f.checkOne(context.Background(), runningJob())

	if !jobs.called {
		t.Fatal("MarkFinished was not called")
	}
	if jobs.gotStatus != models.BackupJobStatusSucceeded {
		t.Errorf("status = %q, want succeeded", jobs.gotStatus)
	}
	if jobs.gotErrText != "" {
		t.Errorf("clean backup should have no error_text, got %q", jobs.gotErrText)
	}
}

// A manifest not yet present means the run is still going — no MarkFinished.
func TestCheckOne_NoManifest_NoOp(t *testing.T) {
	f, jobs := newTestFinalizer(`{"manifest_found":false}`)
	f.checkOne(context.Background(), runningJob())
	if jobs.called {
		t.Error("MarkFinished should not be called while the manifest is absent")
	}
}
