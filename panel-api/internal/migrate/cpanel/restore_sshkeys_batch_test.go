package cpanel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Three real, distinct ed25519 keys so ParseAndFingerprint succeeds and the
// within-file fingerprint dedup keeps all three (distinct fp -> distinct rows,
// each reaching the repository).
const (
	batchKey1 = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIP3RoqVsYV0MGQ0fy/pveTZyDL/MQbeicZUoHVwe8iO4 k1@test"
	batchKey2 = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIE6F4ploenKuyOSGo+yh/b0i+2Y/ZPx+tf1xerS0Hu7X k2@test"
	batchKey3 = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDP0pPcj4UCNFN1VkK4vFGNiisMmabnfoknVwPNhNswR k3@test"
)

// injectSSHRepo is a SSHKeyRepository that fails a chosen Create call. errOn maps
// a 1-based Create call index to the error that call returns; a call not in the
// map succeeds and its row is recorded. It embeds the interface so only Create
// needs an implementation.
type injectSSHRepo struct {
	repository.SSHKeyRepository
	errOn   map[int]error
	created []*models.SSHKey
	n       int
}

func (f *injectSSHRepo) Create(_ context.Context, k *models.SSHKey) error {
	f.n++
	if err := f.errOn[f.n]; err != nil {
		return err
	}
	f.created = append(f.created, k)
	return nil
}

func writeAuthKeys(t *testing.T, lines ...string) *ParsedTarball {
	t.Helper()
	dir := t.TempDir()
	ak := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(ak, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &ParsedTarball{SSHAuthorized: []string{ak}}
}

// The load-bearing guard for this slice. A key already present in the ssh_keys
// table (repository.ErrConflict) must be recorded as already_present and the
// import must KEEP GOING — it is not a hard error. This is the pre-existing bug
// the old isConflict() carried: it string-matched the raw driver message, but
// the repository already translates 1062 to the bare repository.ErrConflict
// sentinel, so the conflict went unrecognized and aborted the whole import.
//
// Falsify: replace the errors.Is(err, sshkeyops.ErrDuplicate) check in
// ImportSSHKeys with the old inline string match
// (strings.Contains(err.Error(),"Duplicate entry")||strings.Contains(err.Error(),"1062")).
// ErrDuplicate's message matches neither, so the conflict aborts and this test
// goes red (err != nil, Created == 1).
func TestImportSSHKeys_ConflictSkipsAndContinues(t *testing.T) {
	repo := &injectSSHRepo{errOn: map[int]error{2: repository.ErrConflict}}
	parsed := writeAuthKeys(t, batchKey1, batchKey2, batchKey3)

	res, err := ImportSSHKeys(context.Background(), repo, parsed, "01USERULID0000000000000000", true)
	if err != nil {
		t.Fatalf("ImportSSHKeys returned error, want nil: %v", err)
	}
	if res.Created != 2 {
		t.Fatalf("Created = %d, want 2 (conflict skipped, others imported)", res.Created)
	}
	if len(repo.created) != 2 {
		t.Fatalf("persisted rows = %d, want 2", len(repo.created))
	}
	var present int
	for _, s := range res.Skipped {
		if strings.Contains(s, "already_present") {
			present++
		}
	}
	if present != 1 {
		t.Fatalf("already_present skips = %d, want 1 (skipped=%v)", present, res.Skipped)
	}
}

// A non-conflict persistence error still aborts the import mid-run and returns a
// wrapped error with the partial result — cPanel's chosen semantic, distinct
// from account restore's continue-on-error. Pin it so routing through the batch
// did not silently soften it.
//
// Falsify: change the abort `return res, fmt.Errorf(...)` in ImportSSHKeys to
// `continue` and this goes red (err == nil, Created == 2).
func TestImportSSHKeys_NonConflictErrorAborts(t *testing.T) {
	boom := errors.New("db exploded")
	repo := &injectSSHRepo{errOn: map[int]error{2: boom}}
	parsed := writeAuthKeys(t, batchKey1, batchKey2, batchKey3)

	res, err := ImportSSHKeys(context.Background(), repo, parsed, "01USERULID0000000000000000", true)
	if err == nil {
		t.Fatal("ImportSSHKeys returned nil error, want abort")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("error does not wrap the underlying failure: %v", err)
	}
	if res.Created != 1 {
		t.Fatalf("Created = %d, want 1 (aborted after first row)", res.Created)
	}
	if len(repo.created) != 1 {
		t.Fatalf("persisted rows = %d, want 1", len(repo.created))
	}
}

// Source-pin: ImportSSHKeys persists through the shared lifecycle leaf and no
// longer carries its own duplicate-detection helper. Green pin. The negative
// assertion is file-scoped (isConflict was unexported with a single call site).
func TestImportSSHKeys_RoutesThroughRestoreBatch(t *testing.T) {
	src, err := os.ReadFile("restore_sshkeys.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	for _, want := range []string{"sshkeyops.NewRestoreBatch(", "sshkeyops.ErrDuplicate"} {
		if !strings.Contains(s, want) {
			t.Errorf("restore_sshkeys.go no longer contains %q — routing regressed", want)
		}
	}
	if strings.Contains(s, "func isConflict") {
		t.Error("restore_sshkeys.go still defines isConflict — the raw-driver string match should be gone")
	}
}
