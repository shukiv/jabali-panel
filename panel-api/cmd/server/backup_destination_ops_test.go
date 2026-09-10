package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeBackupDestRepo implements repository.BackupDestinationRepository for the
// CLI testable core. Only Create carries behavior; the embedded interface makes
// every other method a compile-time stub the core never calls.
type fakeBackupDestRepo struct {
	repository.BackupDestinationRepository
	createErr    error
	createCalled int
	updateErr    error
	updateCalled int
}

func (f *fakeBackupDestRepo) Create(_ context.Context, d *models.BackupDestination) error {
	f.createCalled++
	return f.createErr
}

func (f *fakeBackupDestRepo) Update(_ context.Context, d *models.BackupDestination) error {
	f.updateCalled++
	return f.updateErr
}

// recordingAgent records each agent command (and its params), and can be told to
// fail a specific command.
type recordingAgent struct {
	cmds    []string
	params  map[string]map[string]any
	failCmd string
	failErr error
}

func (r *recordingAgent) call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	r.cmds = append(r.cmds, cmd)
	if r.params == nil {
		r.params = map[string]map[string]any{}
	}
	if m, ok := params.(map[string]any); ok {
		r.params[cmd] = m
	}
	if cmd == r.failCmd {
		return nil, r.failErr
	}
	return json.RawMessage(`{}`), nil
}

func (r *recordingAgent) fired(cmd string) bool {
	for _, c := range r.cmds {
		if c == cmd {
			return true
		}
	}
	return false
}

func newDest() *models.BackupDestination {
	return &models.BackupDestination{ID: "dst-1", Name: "off-site", Kind: "sftp", URL: "sftp:u@h:/b"}
}

// TestCreateBackupDestinationDirect_CompensatesOnNonConflict is the load-bearing
// guard for JAB-310 AC2: a transient (non-conflict) DB failure must remove the
// just-written credential file. Before this slice the CLI compensated only on a
// name-conflict, so this exact path leaked a root:root 0600 secrets file.
func TestCreateBackupDestinationDirect_CompensatesOnNonConflict(t *testing.T) {
	agent := &recordingAgent{}
	repo := &fakeBackupDestRepo{createErr: errors.New("connection reset")}
	d := newDest()

	err := createBackupDestinationDirect(context.Background(), agent.call, repo, d, map[string]string{"SSHPASS": "s3cr3t"})
	if err == nil {
		t.Fatal("expected the create failure to propagate")
	}
	if !agent.fired("backup.dest.creds_delete") {
		t.Fatal("credential file was NOT cleaned up on a non-conflict failure (AC2 leak)")
	}
	if got := agent.params["backup.dest.creds_delete"]["dest_id"]; got != "dst-1" {
		t.Fatalf("creds_delete dest_id = %v, want dst-1", got)
	}
}

// TestCreateBackupDestinationDirect_CompensatesOnConflict pins that the fix keeps
// the pre-existing conflict-branch cleanup — and that the wrapped error still
// resolves to repository.ErrConflict so the RunE can pick its own message.
func TestCreateBackupDestinationDirect_CompensatesOnConflict(t *testing.T) {
	agent := &recordingAgent{}
	repo := &fakeBackupDestRepo{createErr: repository.ErrConflict}
	d := newDest()

	err := createBackupDestinationDirect(context.Background(), agent.call, repo, d, map[string]string{"SSHPASS": "s3cr3t"})
	if !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("want wrapped ErrConflict (so the RunE selects the name-in-use message), got %v", err)
	}
	if !agent.fired("backup.dest.creds_delete") {
		t.Fatal("conflict-branch cleanup was lost")
	}
}

// TestCreateBackupDestinationDirect_NoEnvNoCredCalls: with no env credentials
// there is no file to write or clean up, even when the persist fails. The
// CredentialsRef gate must hold so we never delete what we never wrote.
func TestCreateBackupDestinationDirect_NoEnvNoCredCalls(t *testing.T) {
	agent := &recordingAgent{}
	repo := &fakeBackupDestRepo{createErr: errors.New("boom")}
	d := newDest()

	if err := createBackupDestinationDirect(context.Background(), agent.call, repo, d, nil); err == nil {
		t.Fatal("expected the create failure to propagate")
	}
	if agent.fired("backup.dest.creds_write") || agent.fired("backup.dest.creds_delete") {
		t.Fatalf("no credential agent calls expected without env, got %v", agent.cmds)
	}
	if d.CredentialsRef != nil {
		t.Fatalf("CredentialsRef must stay nil without env, got %q", *d.CredentialsRef)
	}
}

// TestCreateBackupDestinationDirect_CredsWriteFailsBeforePersist: a failed
// credential write returns before the row is persisted (nothing to roll back)
// and never deletes.
func TestCreateBackupDestinationDirect_CredsWriteFailsBeforePersist(t *testing.T) {
	agent := &recordingAgent{failCmd: "backup.dest.creds_write", failErr: errors.New("agent down")}
	repo := &fakeBackupDestRepo{}
	d := newDest()

	err := createBackupDestinationDirect(context.Background(), agent.call, repo, d, map[string]string{"SSHPASS": "s3cr3t"})
	if err == nil || !strings.Contains(err.Error(), "write credentials") {
		t.Fatalf("want a write-credentials error, got %v", err)
	}
	if repo.createCalled != 0 {
		t.Fatalf("row must NOT be persisted after a creds_write failure, Create called %d", repo.createCalled)
	}
	if agent.fired("backup.dest.creds_delete") {
		t.Fatal("nothing was written, so nothing must be deleted")
	}
}

// TestBackupDestinationCreate_RoutesThroughCore source-pins that the RunE hands
// the production agent caller to the core, rather than hand-rolling the
// write/persist/compensate sequence inline again (the only thing guarding the
// AC2 wiring, since the RunE itself is not unit-testable).
func TestBackupDestinationCreate_RoutesThroughCore(t *testing.T) {
	// The positive routing pin is the load-bearing one: it guarantees the create
	// RunE hands the production agent caller to the core (where the AC2
	// compensation lives). A whole-file negative on the creds_write/creds_delete
	// literals would over-reach — the update and delete commands in the same file
	// legitimately call those verbs inline (JAB-339 scar: don't pin a literal a
	// sibling handler shares).
	src := readOpsSource(t, "backup_destination_cmd.go")
	if !strings.Contains(src, "createBackupDestinationDirect(ctx, sharedAgent.Call,") {
		t.Error("create RunE must route through createBackupDestinationDirect with the production agent caller")
	}
}

// TestUpdateBackupDestinationDirect_CompensatesOrphanOnPersistFail is the
// load-bearing guard: this update wrote a brand-new credential file (the row had
// none before), and the persist failed — the orphaned secrets file must be
// removed so it isn't left behind a NULL row.
func TestUpdateBackupDestinationDirect_CompensatesOrphanOnPersistFail(t *testing.T) {
	agent := &recordingAgent{}
	repo := &fakeBackupDestRepo{updateErr: errors.New("connection reset")}
	d := newDest()
	ref := "/var/lib/jabali/creds/dst-1.env"
	d.CredentialsRef = &ref // a creds_write earlier in this update set it

	err := updateBackupDestinationDirect(context.Background(), agent.call, repo, d, false)
	if err == nil {
		t.Fatal("expected the update failure to propagate")
	}
	if !agent.fired("backup.dest.creds_delete") {
		t.Fatal("newly-written credential file was NOT cleaned up on persist failure (orphan leak)")
	}
	if got := agent.params["backup.dest.creds_delete"]["dest_id"]; got != "dst-1" {
		t.Fatalf("creds_delete dest_id = %v, want dst-1", got)
	}
}

// TestUpdateBackupDestinationDirect_PreExistingRefNotDeletedOnFail is the other
// load-bearing guard: a credential file the DB row ALREADY referenced must NOT be
// removed on a persist failure — the surviving row still points at it, so
// deleting it would break the live destination.
func TestUpdateBackupDestinationDirect_PreExistingRefNotDeletedOnFail(t *testing.T) {
	agent := &recordingAgent{}
	repo := &fakeBackupDestRepo{updateErr: errors.New("connection reset")}
	d := newDest()
	ref := "/var/lib/jabali/creds/dst-1.env"
	d.CredentialsRef = &ref

	if err := updateBackupDestinationDirect(context.Background(), agent.call, repo, d, true); err == nil {
		t.Fatal("expected the update failure to propagate")
	}
	if agent.fired("backup.dest.creds_delete") {
		t.Fatal("a pre-existing credential file (still referenced by the surviving row) must NOT be deleted")
	}
}

// TestUpdateBackupDestinationDirect_NoRefNoDelete: nothing to clean up when the
// update wrote no credential file, even on a persist failure.
func TestUpdateBackupDestinationDirect_NoRefNoDelete(t *testing.T) {
	agent := &recordingAgent{}
	repo := &fakeBackupDestRepo{updateErr: errors.New("boom")}
	d := newDest() // CredentialsRef nil

	if err := updateBackupDestinationDirect(context.Background(), agent.call, repo, d, false); err == nil {
		t.Fatal("expected the update failure to propagate")
	}
	if agent.fired("backup.dest.creds_delete") {
		t.Fatalf("no credential file was written, so nothing must be deleted, got %v", agent.cmds)
	}
}

// TestUpdateBackupDestinationDirect_SuccessNoCompensation: a successful persist
// touches no agent cleanup.
func TestUpdateBackupDestinationDirect_SuccessNoCompensation(t *testing.T) {
	agent := &recordingAgent{}
	repo := &fakeBackupDestRepo{}
	d := newDest()
	ref := "/var/lib/jabali/creds/dst-1.env"
	d.CredentialsRef = &ref

	if err := updateBackupDestinationDirect(context.Background(), agent.call, repo, d, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.updateCalled != 1 {
		t.Fatalf("Update called %d times, want 1", repo.updateCalled)
	}
	if agent.fired("backup.dest.creds_delete") {
		t.Fatal("no cleanup expected on success")
	}
}

// TestBackupDestinationUpdate_RoutesThroughCore source-pins that the update RunE
// hands the production agent caller to the compensating core, and that it
// captures origHadCredsFile BEFORE the --clear-creds branch — capturing it later
// would misread a cleared ref as "no pre-existing file" and wrongly compensate a
// file the surviving row still references.
func TestBackupDestinationUpdate_RoutesThroughCore(t *testing.T) {
	src := readOpsSource(t, "backup_destination_cmd.go")
	start := strings.Index(src, "func newBackupDestinationUpdateCmd(")
	if start < 0 {
		t.Fatal("update command not found")
	}
	body := src[start:]
	if end := strings.Index(body, "\nfunc newBackupDestinationRotatePasswordCmd("); end > 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "updateBackupDestinationDirect(ctx, sharedAgent.Call,") {
		t.Error("update RunE must route the persist through updateBackupDestinationDirect with the production agent caller")
	}
	capIdx := strings.Index(body, "origHadCredsFile := d.CredentialsRef != nil")
	clearIdx := strings.Index(body, "if clearCreds {")
	if capIdx < 0 {
		t.Fatal("origHadCredsFile capture not found")
	}
	if clearIdx < 0 || capIdx > clearIdx {
		t.Error("origHadCredsFile must be captured BEFORE the --clear-creds branch mutates CredentialsRef")
	}
}

func readOpsSource(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
