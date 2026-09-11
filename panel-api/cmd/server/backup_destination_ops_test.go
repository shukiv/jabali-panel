package main

import (
	"bytes"
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
	deleteErr    error
	deleteCalled int
}

func (f *fakeBackupDestRepo) Create(_ context.Context, d *models.BackupDestination) error {
	f.createCalled++
	return f.createErr
}

func (f *fakeBackupDestRepo) Update(_ context.Context, d *models.BackupDestination) error {
	f.updateCalled++
	return f.updateErr
}

func (f *fakeBackupDestRepo) Delete(_ context.Context, _ string) error {
	f.deleteCalled++
	return f.deleteErr
}

// agentCredsPath is the on-disk path the fake Agent reports in its creds_write
// reply. It is deliberately NOT filepath.Join(credsDir, "dst-1.env"): the core
// must record the path the Agent returns, not one it computes locally, so a test
// that sees this exact value proves the reply — not a local guess — set the row's
// CredentialsRef (JAB-310 AC5).
const agentCredsPath = "/agent/says/dst-1.env"

// recordingAgent records each agent command (and its params), and can be told to
// fail a specific command.
type recordingAgent struct {
	cmds    []string
	params  map[string]map[string]any
	failCmd string
	failErr error
	// credsWriteNoPath, when true, makes the creds_write reply omit "path"
	// (a malformed Agent reply): the file is on disk but has no referenceable
	// path, which the write helper must treat as a failure and compensate.
	credsWriteNoPath bool
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
	if cmd == "backup.dest.creds_write" && !r.credsWriteNoPath {
		return json.RawMessage(`{"path":"` + agentCredsPath + `"}`), nil
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

// TestCreateBackupDestinationDirect_RefFromAgentReply is the discriminator for
// this slice: the row's CredentialsRef must be the path the Agent returned in its
// creds_write reply, not a path panel-api computed locally from credsDir. The
// Agent owns /etc/jabali-panel/ and is authoritative for the path, so under a
// panel/Agent binary-version skew the two must not disagree (JAB-310 AC5). The
// fake returns a deliberately non-local path.
func TestCreateBackupDestinationDirect_RefFromAgentReply(t *testing.T) {
	agent := &recordingAgent{}
	repo := &fakeBackupDestRepo{} // persist succeeds
	d := newDest()

	if err := createBackupDestinationDirect(context.Background(), agent.call, repo, d, map[string]string{"AWS_SECRET_ACCESS_KEY": "x"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.createCalled != 1 {
		t.Fatalf("Create called %d times, want 1", repo.createCalled)
	}
	if d.CredentialsRef == nil {
		t.Fatal("CredentialsRef must be set from the Agent reply")
	}
	if *d.CredentialsRef != agentCredsPath {
		t.Fatalf("CredentialsRef = %q, want the Agent-reported path %q (not a locally-computed one)", *d.CredentialsRef, agentCredsPath)
	}
}

// TestWriteBackupDestinationCreds_MissingPathCompensates is load-bearing: when the
// Agent write succeeds but the reply carries no usable path, the file is on disk
// yet the row can never reference it. The helper must compensate (creds_delete)
// and return an error rather than persist a row that points nowhere — driven here
// through the create core, which must then NOT persist.
func TestWriteBackupDestinationCreds_MissingPathCompensates(t *testing.T) {
	agent := &recordingAgent{credsWriteNoPath: true}
	repo := &fakeBackupDestRepo{} // would succeed if reached
	d := newDest()

	err := createBackupDestinationDirect(context.Background(), agent.call, repo, d, map[string]string{"AWS_SECRET_ACCESS_KEY": "x"})
	if err == nil {
		t.Fatal("a creds_write reply with no path must return an error")
	}
	if !agent.fired("backup.dest.creds_delete") {
		t.Fatal("the on-disk file left by a path-less reply was NOT compensated (orphan)")
	}
	if repo.createCalled != 0 {
		t.Fatalf("row must NOT be persisted when no referenceable path came back, Create called %d", repo.createCalled)
	}
	if d.CredentialsRef != nil {
		t.Fatalf("CredentialsRef must stay nil when the reply had no path, got %q", *d.CredentialsRef)
	}
}

// TestBackupDestinationUpdate_WritesRouteThroughHelper source-pins that the update
// RunE's two credential-write sites (sftp-password and --env) go through
// writeBackupDestinationCreds and no longer compute the ref locally from credsDir.
// The RunE is not unit-testable, so pin the migration by source: the local
// filepath.Join(credsDir ... computation must be gone from the update command.
func TestBackupDestinationUpdate_WritesRouteThroughHelper(t *testing.T) {
	src := readOpsSource(t, "backup_destination_cmd.go")
	start := strings.Index(src, "func newBackupDestinationUpdateCmd(")
	if start < 0 {
		t.Fatal("update command not found")
	}
	body := src[start:]
	if end := strings.Index(body, "\nfunc newBackupDestinationRotatePasswordCmd("); end > 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "writeBackupDestinationCreds(ctx, sharedAgent.Call,") {
		t.Error("update RunE credential writes must route through writeBackupDestinationCreds")
	}
	if strings.Contains(body, "filepath.Join(credsDir") {
		t.Error("update RunE must not compute CredentialsRef locally from credsDir; use the Agent reply path")
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

	err := updateBackupDestinationDirect(context.Background(), agent.call, repo, d, false, false)
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

	if err := updateBackupDestinationDirect(context.Background(), agent.call, repo, d, true, false); err == nil {
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

	if err := updateBackupDestinationDirect(context.Background(), agent.call, repo, d, false, false); err == nil {
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

	if err := updateBackupDestinationDirect(context.Background(), agent.call, repo, d, false, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.updateCalled != 1 {
		t.Fatalf("Update called %d times, want 1", repo.updateCalled)
	}
	if agent.fired("backup.dest.creds_delete") {
		t.Fatal("no cleanup expected on success")
	}
}

// TestUpdateBackupDestinationDirect_ClearCredsReapDeferredNotDeletedOnFail is the
// load-bearing guard for this slice: --clear-creds dropped the reference and the
// persist then FAILED. The on-disk credential file must NOT be removed — the
// surviving DB row still references it (the RunE only nil'd the in-memory copy),
// so deleting it would leave a dangling reference (the reverse of the orphan
// leak). Before this slice the RunE deleted the file BEFORE persisting, stranding
// the row pointing at a missing file on exactly this path.
func TestUpdateBackupDestinationDirect_ClearCredsReapDeferredNotDeletedOnFail(t *testing.T) {
	agent := &recordingAgent{}
	repo := &fakeBackupDestRepo{updateErr: errors.New("connection reset")}
	d := newDest() // RunE nil'd CredentialsRef; the DB row still has the old file

	// clearedCredsFile true (the row had a file), origHadCredsFile true (pre-existing).
	err := updateBackupDestinationDirect(context.Background(), agent.call, repo, d, true, true)
	if err == nil || !strings.Contains(err.Error(), "update destination") {
		t.Fatalf("want a wrapped update-destination error, got %v", err)
	}
	if repo.updateCalled != 1 {
		t.Fatalf("Update must have been attempted exactly once, called %d", repo.updateCalled)
	}
	if agent.fired("backup.dest.creds_delete") {
		t.Fatal("persist failed — the file the surviving row still references must NOT be reaped (dangling ref)")
	}
}

// TestUpdateBackupDestinationDirect_ClearCredsReapedOnSuccess: --clear-creds
// dropped the reference and the persist SUCCEEDED — now (and only now) the on-disk
// file is reaped, exactly once. The row that no longer references it has
// committed, so the removal cannot strand a surviving reference.
func TestUpdateBackupDestinationDirect_ClearCredsReapedOnSuccess(t *testing.T) {
	agent := &recordingAgent{}
	repo := &fakeBackupDestRepo{}
	d := newDest() // CredentialsRef nil (RunE cleared it)

	if err := updateBackupDestinationDirect(context.Background(), agent.call, repo, d, true, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.updateCalled != 1 {
		t.Fatalf("Update called %d times, want 1", repo.updateCalled)
	}
	if !agent.fired("backup.dest.creds_delete") {
		t.Fatal("a cleared credential file must be reaped after a successful persist")
	}
	if len(agent.cmds) != 1 {
		t.Fatalf("creds_delete must fire exactly once, got %v", agent.cmds)
	}
	if got := agent.params["backup.dest.creds_delete"]["dest_id"]; got != "dst-1" {
		t.Fatalf("creds_delete dest_id = %v, want dst-1", got)
	}
}

// TestUpdateBackupDestinationDirect_ClearThenRewriteNoReap: --clear-creds followed
// by a creds_write in the SAME update (--clear-creds --env) re-creates the file at
// the deterministic path and re-sets CredentialsRef. The reap must be skipped — the
// committed row references the rewritten file, so removing it would break the live
// destination. clearedCredsFile stays true, but a non-nil ref gates the reap off.
func TestUpdateBackupDestinationDirect_ClearThenRewriteNoReap(t *testing.T) {
	agent := &recordingAgent{}
	repo := &fakeBackupDestRepo{}
	d := newDest()
	ref := "/etc/jabali-panel/restic-remotes/dst-1.env" // creds_write re-set it this update
	d.CredentialsRef = &ref

	if err := updateBackupDestinationDirect(context.Background(), agent.call, repo, d, true, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if agent.fired("backup.dest.creds_delete") {
		t.Fatalf("a file re-written in the same update must NOT be reaped, got %v", agent.cmds)
	}
}

// TestUpdateBackupDestinationDirect_WrittenThenClearedOrphanCompensatedOnFail
// guards the interaction of the deferred reap with the orphan compensation:
// --sftp-password/--env writes a credential file on a destination that had none
// (origHadCredsFile false), then --clear-creds in the SAME command nil's the
// reference (clearedCredsFile true), then the persist FAILS. The just-written
// root:root 0600 file must still be removed — the surviving row references
// nothing, so it is a true orphan. Deferring the reap must not open this leak;
// the compensation gate widened to `d.CredentialsRef != nil || clearedCredsFile`
// to cover it.
func TestUpdateBackupDestinationDirect_WrittenThenClearedOrphanCompensatedOnFail(t *testing.T) {
	agent := &recordingAgent{}
	repo := &fakeBackupDestRepo{updateErr: errors.New("connection reset")}
	d := newDest() // creds_write set a ref this call, then --clear-creds nil'd it

	err := updateBackupDestinationDirect(context.Background(), agent.call, repo, d, false, true)
	if err == nil || !strings.Contains(err.Error(), "update destination") {
		t.Fatalf("want a wrapped update-destination error, got %v", err)
	}
	if !agent.fired("backup.dest.creds_delete") {
		t.Fatal("a file written this call then cleared must be compensated on persist failure (orphan leak)")
	}
	if got := agent.params["backup.dest.creds_delete"]["dest_id"]; got != "dst-1" {
		t.Fatalf("creds_delete dest_id = %v, want dst-1", got)
	}
}

// TestBackupDestinationUpdate_RoutesThroughCore source-pins that the update RunE
// hands the production agent caller to the compensating core, captures
// origHadCredsFile BEFORE the --clear-creds branch (capturing it later would
// misread a cleared ref as "no pre-existing file" and wrongly compensate a file
// the surviving row still references), and — for this slice — does NOT eagerly
// delete the credential file inside the --clear-creds branch (the reap is deferred
// to the core, post-persist).
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

	// The eager creds_delete must be GONE from the --clear-creds branch: the file
	// removal is deferred to the core, post-persist (dangling-ref reverse-class).
	// Scope the negative to the clear block only — from `if clearCreds {` to the
	// creds-rewrite comment — because creds_write/creds_delete verbs legitimately
	// appear elsewhere in this RunE (JAB-339 scar: never pin a literal a sibling
	// branch shares).
	rewriteIdx := strings.Index(body, "// Rewrite cloud/SFTP")
	if rewriteIdx < 0 || clearIdx > rewriteIdx {
		t.Fatal("clear-creds block boundaries not found")
	}
	if strings.Contains(body[clearIdx:rewriteIdx], "backup.dest.creds_delete") {
		t.Error("--clear-creds must NOT delete the credential file before persist; the reap is deferred to updateBackupDestinationDirect (dangling-ref reverse-class)")
	}
}

// TestBackupDestinationUpdate_SFTPBlockReplacesNotOverlays pins the JAB-310 AC5
// full-block-replace WIRING in the update RunE (not unit-testable): the
// structural SFTP edit builds a fresh block via buildReplacedSFTPBlock, never
// overlays onto the stored block, and --sftp-password is not a structural field
// (it is an independent credential write). Reverting to the pre-JAB-310 overlay
// — `opts := d.ExtraOptionsTyped().SFTP` seeded from the stored row, or
// re-adding sftp-password to the structural touch list — reddens this. The
// behavior of the extracted helpers themselves is tested in
// backup_destination_parity_cmd_test.go.
func TestBackupDestinationUpdate_SFTPBlockReplacesNotOverlays(t *testing.T) {
	src := readOpsSource(t, "backup_destination_cmd.go")
	start := strings.Index(src, "func newBackupDestinationUpdateCmd(")
	if start < 0 {
		t.Fatal("update command not found")
	}
	body := src[start:]
	if end := strings.Index(body, "\nfunc newBackupDestinationRotatePasswordCmd("); end > 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "buildReplacedSFTPBlock(sftpHost, sftpUser, sftpPort, sftpPath, sftpAuth, sftpKeyPath)") {
		t.Error("structural SFTP edit must build a full-replace block via buildReplacedSFTPBlock (fresh from flags)")
	}
	// The structural branch must not seed opts from the stored block (the overlay).
	// The password branch legitimately reads d.ExtraOptionsTyped().SFTP for its
	// effective-auth gate (`s := d.ExtraOptionsTyped().SFTP`), so pin the exact
	// overlay-seed spelling, not the accessor in general.
	if strings.Contains(body, "opts := d.ExtraOptionsTyped().SFTP") {
		t.Error("structural SFTP edit must not seed opts from the stored block (overlay); build fresh from flags")
	}
	// The structural touch list must exclude sftp-password.
	tlStart := strings.Index(body, "sftpStructural := false")
	if tlStart < 0 {
		t.Fatal("structural touch list not found")
	}
	touchList := body[tlStart:]
	touchList = touchList[:strings.Index(touchList, "}")]
	if strings.Contains(touchList, "sftp-password") {
		t.Error("--sftp-password must not be in the structural touch list; it is an independent credential write")
	}
}

// TestDeleteBackupDestinationDirect_SurfacesSwallowedCleanupFailure is the
// load-bearing guard for this slice: a failed creds_delete after the row is gone
// must NOT vanish. The delete still succeeds (returns nil — the row is deleted),
// but the leftover root:root 0600 credential file is reported on errOut with the
// dest id and the underlying error, matching the CLI create/update cores and the
// REST twin #1665. Before this slice the RunE did `_, _ = sharedAgent.Call(...)`.
func TestDeleteBackupDestinationDirect_SurfacesSwallowedCleanupFailure(t *testing.T) {
	agent := &recordingAgent{failCmd: "backup.dest.creds_delete", failErr: errors.New("agent down")}
	repo := &fakeBackupDestRepo{}
	d := newDest()
	var errOut bytes.Buffer

	if err := deleteBackupDestinationDirect(context.Background(), agent.call, repo, d, &errOut); err != nil {
		t.Fatalf("a cleanup failure must stay non-fatal to the delete, got %v", err)
	}
	if repo.deleteCalled != 1 {
		t.Fatalf("row must be deleted exactly once, Delete called %d", repo.deleteCalled)
	}
	warn := errOut.String()
	if !strings.Contains(warn, "cleanup failed") || !strings.Contains(warn, "dst-1") || !strings.Contains(warn, "agent down") {
		t.Fatalf("failed creds_delete must be surfaced with dest id + error, got: %q", warn)
	}
}

// TestDeleteBackupDestinationDirect_SuccessNoWarning: a creds_delete that
// succeeds writes nothing to errOut — we only warn when a secrets file is
// actually left behind.
func TestDeleteBackupDestinationDirect_SuccessNoWarning(t *testing.T) {
	agent := &recordingAgent{}
	repo := &fakeBackupDestRepo{}
	d := newDest()
	ref := "/etc/jabali-panel/restic-remotes/dst-1.env"
	d.CredentialsRef = &ref
	var errOut bytes.Buffer

	if err := deleteBackupDestinationDirect(context.Background(), agent.call, repo, d, &errOut); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.deleteCalled != 1 {
		t.Fatalf("row must be deleted exactly once, Delete called %d", repo.deleteCalled)
	}
	if !agent.fired("backup.dest.creds_delete") {
		t.Fatal("creds_delete must fire on a successful delete")
	}
	if errOut.Len() != 0 {
		t.Fatalf("no warning expected on a successful cleanup, got: %q", errOut.String())
	}
}

// TestDeleteBackupDestinationDirect_DeleteFailsNoCredsDelete pins the ordering:
// if the row delete fails the row survives, so its credential file must be left
// in place (never delete a file behind a surviving row). The error wraps the
// cause, and creds_delete never fires.
func TestDeleteBackupDestinationDirect_DeleteFailsNoCredsDelete(t *testing.T) {
	agent := &recordingAgent{}
	repo := &fakeBackupDestRepo{deleteErr: errors.New("connection reset")}
	d := newDest()
	var errOut bytes.Buffer

	err := deleteBackupDestinationDirect(context.Background(), agent.call, repo, d, &errOut)
	if err == nil || !strings.Contains(err.Error(), "delete destination") {
		t.Fatalf("want a delete-destination error, got %v", err)
	}
	if repo.deleteCalled != 1 {
		t.Fatalf("Delete must have been attempted exactly once, called %d", repo.deleteCalled)
	}
	if agent.fired("backup.dest.creds_delete") {
		t.Fatal("row delete failed (row survives) — its credential file must NOT be removed")
	}
	if errOut.Len() != 0 {
		t.Fatalf("no cleanup warning expected when the row delete failed, got: %q", errOut.String())
	}
}

// TestDeleteBackupDestinationDirect_CredsDeleteUnconditional pins that the
// creds_delete call is NOT gated on CredentialsRef: even a destination that never
// had a credential file still fires it once (the Agent handler is idempotent), so
// no orphaned file is missed. A future --clear-creds/guard slice would change this
// deliberately; this keeps the current unconditional behavior explicit.
func TestDeleteBackupDestinationDirect_CredsDeleteUnconditional(t *testing.T) {
	agent := &recordingAgent{}
	repo := &fakeBackupDestRepo{}
	d := newDest() // CredentialsRef nil
	var errOut bytes.Buffer

	if err := deleteBackupDestinationDirect(context.Background(), agent.call, repo, d, &errOut); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !agent.fired("backup.dest.creds_delete") {
		t.Fatal("creds_delete must fire unconditionally, even with no CredentialsRef")
	}
	if len(agent.cmds) != 1 {
		t.Fatalf("creds_delete must fire exactly once, got %v", agent.cmds)
	}
	if errOut.Len() != 0 {
		t.Fatalf("no warning expected, got: %q", errOut.String())
	}
}

// TestBackupDestinationDelete_RoutesThroughCore source-pins that the delete RunE
// hands the production agent caller to the core rather than swallowing the
// creds_delete inline again (positive pin only — the create/update commands in
// the same file legitimately call the same verbs, JAB-339 scar).
func TestBackupDestinationDelete_RoutesThroughCore(t *testing.T) {
	src := readOpsSource(t, "backup_destination_cmd.go")
	start := strings.Index(src, "func newBackupDestinationDeleteCmd(")
	if start < 0 {
		t.Fatal("delete command not found")
	}
	body := src[start:]
	if !strings.Contains(body, "deleteBackupDestinationDirect(ctx, sharedAgent.Call,") {
		t.Error("delete RunE must route through deleteBackupDestinationDirect with the production agent caller")
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
