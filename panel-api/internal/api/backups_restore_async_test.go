package api

// GH #1044: restores moved out of the HTTP request into background job
// runners. The runners own the row-sealing now, so their semantics are the
// contract: an agent failure seals `failed`, a partial stage set seals
// `partial`, success persists the outcome the drawer renders — and every
// seal happens on a FRESH context, because the request context these used
// to borrow is long dead by the time a big restore finishes.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type sealCapture struct {
	repository.BackupJobRepository
	mu       sync.Mutex
	status   string
	errText  string
	manifest json.RawMessage
	warnings json.RawMessage
	sealed   chan struct{}
}

func newSealCapture() *sealCapture { return &sealCapture{sealed: make(chan struct{})} }

func (r *sealCapture) MarkFinished(_ context.Context, _, status string, _, _ string,
	_, _ uint64, manifest, warnings json.RawMessage, errText string) error {
	r.mu.Lock()
	r.status, r.errText, r.manifest, r.warnings = status, errText, manifest, warnings
	r.mu.Unlock()
	close(r.sealed)
	return nil
}

func (r *sealCapture) wait(t *testing.T) {
	t.Helper()
	select {
	case <-r.sealed:
	case <-time.After(5 * time.Second):
		t.Fatal("job was never sealed — a restore that finishes without MarkFinished leaves the row `running` forever")
	}
}

type restoreAgent struct {
	reply json.RawMessage
	err   error
}

func (a restoreAgent) Call(context.Context, string, any) (json.RawMessage, error) {
	return a.reply, a.err
}

func TestRunAccountRestoreJob_AgentFailureSealsFailed(t *testing.T) {
	jobs := newSealCapture()
	h := &backupHandler{cfg: BackupHandlerConfig{
		Jobs:  jobs,
		Agent: restoreAgent{err: errors.New("restic: repo locked")},
	}}
	h.runAccountRestoreJob("job-1", &models.BackupDestination{ID: "d1", Kind: "local"}, map[string]any{})
	jobs.wait(t)
	if jobs.status != models.BackupJobStatusFailed {
		t.Errorf("status = %q, want failed", jobs.status)
	}
	if !strings.Contains(jobs.errText, "repo locked") {
		t.Errorf("the AGENT failure should be the recorded reason, got %q", jobs.errText)
	}
}

func TestRunAccountRestoreJob_PartialStagesSealPartial(t *testing.T) {
	jobs := newSealCapture()
	h := &backupHandler{cfg: BackupHandlerConfig{
		Jobs: jobs,
		Agent: restoreAgent{reply: json.RawMessage(
			`{"job_id":"job-1","stages":[{"name":"home","status":"ok"},{"name":"db","status":"failed","error":"pg_restore: boom"}]}`)},
	}}
	h.runAccountRestoreJob("job-1", &models.BackupDestination{ID: "d1", Kind: "local"}, map[string]any{})
	jobs.wait(t)
	if jobs.status != models.BackupJobStatusPartial {
		t.Errorf("status = %q, want partial — one failed stage out of two", jobs.status)
	}
	if jobs.errText == "" || !strings.Contains(jobs.errText, "pg_restore") {
		t.Errorf("the failed stage's error should surface on the row, got %q", jobs.errText)
	}
}

func TestRunSelectiveRestoreJob_PersistsOutcomeForTheDrawer(t *testing.T) {
	jobs := newSealCapture()
	h := &meBackupHandler{cfg: MeBackupsHandlerConfig{
		Jobs: jobs,
		Agent: restoreAgent{reply: json.RawMessage(
			`{"applied":["db → alice_pg (postgres)"],"skipped":[],"warnings":["mail: 1 message already existed"]}`)},
	}}
	h.runSelectiveRestoreJob("job-2", "snap", "alice",
		meRestoreSelectiveRequest{Databases: []string{"alice_pg"}, Overwrite: true}, nil, nil)
	jobs.wait(t)
	if jobs.status != models.BackupJobStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", jobs.status)
	}
	// The drawer polls the job and renders warnings_json — an empty blob
	// here means the tenant sees "restored" with no detail of WHAT.
	var out selectiveRestoreOutcome
	if err := json.Unmarshal(jobs.warnings, &out); err != nil {
		t.Fatalf("warnings_json is not the outcome shape: %v (%s)", err, jobs.warnings)
	}
	if len(out.Applied) != 1 || !strings.Contains(out.Applied[0], "alice_pg") {
		t.Errorf("applied list lost between agent and row: %#v", out)
	}
	if len(out.Warnings) != 1 {
		t.Errorf("agent warnings lost: %#v", out.Warnings)
	}
}

func TestRunSelectiveRestoreJob_AgentFailureIsGenericOnTheRow(t *testing.T) {
	jobs := newSealCapture()
	h := &meBackupHandler{cfg: MeBackupsHandlerConfig{
		Jobs:  jobs,
		Agent: restoreAgent{err: errors.New("restic: /var/lib secret path leaked")},
	}}
	h.runSelectiveRestoreJob("job-3", "snap", "alice",
		meRestoreSelectiveRequest{Databases: []string{"x"}, Overwrite: true}, nil, nil)
	jobs.wait(t)
	if jobs.status != models.BackupJobStatusFailed {
		t.Fatalf("status = %q, want failed", jobs.status)
	}
	// JAB-114: agent errors carry host internals. The tenant-visible row
	// must NOT echo them.
	if strings.Contains(jobs.errText, "/var/lib") {
		t.Errorf("agent internals leaked onto the tenant-visible row: %q", jobs.errText)
	}
}

// JAB-312: the finalizer applies the returned panel-metadata bundle. A bundle
// that cannot be applied must NOT leave the job reporting a clean success — the
// operator would think the panel was reconstructed when it wasn't.
func TestRunAccountRestoreJob_MetadataApplyFailureDowngradesSuccess(t *testing.T) {
	jobs := newSealCapture()
	h := &backupHandler{cfg: BackupHandlerConfig{
		Jobs: jobs,
		// All stages ok, but the metadata bundle is not a valid AccountMetadata
		// object — Apply cannot parse it, so the finalizer must downgrade.
		Agent: restoreAgent{reply: json.RawMessage(
			`{"job_id":"job-1","stages":[{"name":"home","status":"ok"}],"metadata":"not-an-object"}`)},
	}}
	h.runAccountRestoreJob("job-1", &models.BackupDestination{ID: "d1", Kind: "local"}, map[string]any{})
	jobs.wait(t)
	if jobs.status != models.BackupJobStatusPartial {
		t.Errorf("status = %q, want partial — host stages ok but metadata apply failed", jobs.status)
	}
	if !strings.Contains(jobs.errText, "metadata apply") {
		t.Errorf("the metadata-apply failure must surface on the row, got %q", jobs.errText)
	}
}

// An old snapshot (schema_version=1) carries no metadata bundle. The finalizer
// must treat that as the documented fallback — a no-op — not a failure.
func TestRunAccountRestoreJob_NoMetadataStaysSucceeded(t *testing.T) {
	jobs := newSealCapture()
	h := &backupHandler{cfg: BackupHandlerConfig{
		Jobs: jobs,
		Agent: restoreAgent{reply: json.RawMessage(
			`{"job_id":"job-1","stages":[{"name":"home","status":"ok"},{"name":"db","status":"ok"}]}`)},
	}}
	h.runAccountRestoreJob("job-1", &models.BackupDestination{ID: "d1", Kind: "local"}, map[string]any{})
	jobs.wait(t)
	if jobs.status != models.BackupJobStatusSucceeded {
		t.Errorf("status = %q, want succeeded — an old snapshot with no metadata is not a failure", jobs.status)
	}
	if jobs.errText != "" {
		t.Errorf("no error expected on a clean restore without a bundle, got %q", jobs.errText)
	}
}

// recordingAgent captures the params of the last call so wire-contract
// assertions can look inside.
type recordingAgent struct {
	reply  json.RawMessage
	mu     sync.Mutex
	params map[string]any
}

func (a *recordingAgent) Call(_ context.Context, _ string, p any) (json.RawMessage, error) {
	a.mu.Lock()
	if m, ok := p.(map[string]any); ok {
		a.params = m
	}
	a.mu.Unlock()
	return a.reply, nil
}

// The snapshot lives in the SOURCE backup's destination repo. Found live:
// without repo_url on the wire, the agent read the legacy default repo and
// every restore from a per-destination backup failed with "failed to find
// snapshot" — a bug the synchronous version had all along.
func TestRunSelectiveRestoreJob_SendsTheSourceDestinationRepo(t *testing.T) {
	jobs := newSealCapture()
	ag := &recordingAgent{reply: json.RawMessage(`{"applied":["db → x"],"skipped":[],"warnings":[]}`)}
	h := &meBackupHandler{cfg: MeBackupsHandlerConfig{Jobs: jobs, Agent: ag}}
	dest := &models.BackupDestination{ID: "d1", Kind: "local", URL: "/var/lib/jabali-backups/custom"}

	h.runSelectiveRestoreJob("job-4", "snap", "alice",
		meRestoreSelectiveRequest{Databases: []string{"x"}, Overwrite: true}, nil, dest)
	jobs.wait(t)

	ag.mu.Lock()
	defer ag.mu.Unlock()
	if ag.params["repo_url"] != "/var/lib/jabali-backups/custom" {
		t.Errorf("repo_url did not reach the agent — the restore reads the WRONG repo: %#v", ag.params["repo_url"])
	}
}
