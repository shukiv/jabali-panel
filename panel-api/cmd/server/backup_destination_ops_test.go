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
}

func (f *fakeBackupDestRepo) Create(_ context.Context, d *models.BackupDestination) error {
	f.createCalled++
	return f.createErr
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

func readOpsSource(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
