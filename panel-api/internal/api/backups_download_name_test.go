package api

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #2089: a downloaded backup is named for its account, content and time,
// not only its job ID, so a folder of downloads can be told apart. The
// extension is added by streamBackupArtifact from the real archive format.
func TestBackupDownloadBase(t *testing.T) {
	created := time.Date(2026, 10, 11, 4, 30, 12, 0, time.UTC)
	cases := []struct {
		name     string
		job      models.BackupJob
		username string
		want     string
	}{
		{"full account backup", models.BackupJob{ID: "01JOB", Kind: models.BackupJobKindAccountBackup, Content: "full", CreatedAt: created}, "alice", "backup_alice_2026-10-11_0430"},
		{"empty content is full", models.BackupJob{ID: "01JOB", Kind: models.BackupJobKindAccountBackup, CreatedAt: created}, "alice", "backup_alice_2026-10-11_0430"},
		{"files only", models.BackupJob{ID: "01JOB", Kind: models.BackupJobKindAccountBackup, Content: "files", CreatedAt: created}, "alice", "backup_alice_files_2026-10-11_0430"},
		{"database only", models.BackupJob{ID: "01JOB", Kind: models.BackupJobKindAccountBackup, Content: "database", CreatedAt: created}, "alice", "backup_alice_database_2026-10-11_0430"},
		{"system backup", models.BackupJob{ID: "01JOB", Kind: models.BackupJobKindSystemBackup, Content: "full", CreatedAt: created}, "system", "backup_system_2026-10-11_0430"},
		{"account gone falls back to the job ID", models.BackupJob{ID: "01JOB", Kind: models.BackupJobKindAccountBackup, Content: "full", CreatedAt: created}, "", "backup_01JOB_2026-10-11_0430"},
		{"time is UTC", models.BackupJob{ID: "01JOB", Kind: models.BackupJobKindAccountBackup, Content: "full", CreatedAt: created.In(time.FixedZone("NZDT", 13*3600))}, "alice", "backup_alice_2026-10-11_0430"},
		{"unsafe characters are dropped", models.BackupJob{ID: "01JOB", Kind: models.BackupJobKindAccountBackup, Content: "full", CreatedAt: created}, "../a b\"c;d", "backup_abcd_2026-10-11_0430"},
		{"unsafe content is dropped", models.BackupJob{ID: "01JOB", Kind: models.BackupJobKindAccountBackup, Content: "x/../y", CreatedAt: created}, "alice", "backup_alice_xy_2026-10-11_0430"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := backupDownloadBase(&tc.job, tc.username); got != tc.want {
				t.Fatalf("backupDownloadBase = %q, want %q", got, tc.want)
			}
		})
	}
}

type ownerNameUsers struct {
	repository.UserRepository
	users map[string]*models.User
}

func (o *ownerNameUsers) FindByID(_ context.Context, id string) (*models.User, error) {
	if u, ok := o.users[id]; ok {
		return u, nil
	}
	return nil, repository.ErrNotFound
}

func TestBackupJobOwnerName(t *testing.T) {
	alice := "alice"
	users := &ownerNameUsers{users: map[string]*models.User{
		"u1": {ID: "u1", Username: &alice},
		"u2": {ID: "u2"}, // no username
	}}
	ctx := context.Background()
	cases := []struct {
		name  string
		users repository.UserRepository
		job   models.BackupJob
		want  string
	}{
		{"account owner", users, models.BackupJob{UserID: "u1", Kind: models.BackupJobKindAccountBackup}, "alice"},
		{"system backup", users, models.BackupJob{UserID: "system", Kind: models.BackupJobKindSystemBackup}, "system"},
		{"deleted account", users, models.BackupJob{UserID: "gone", Kind: models.BackupJobKindAccountBackup}, ""},
		{"account without a username", users, models.BackupJob{UserID: "u2", Kind: models.BackupJobKindAccountBackup}, ""},
		{"no user repository", nil, models.BackupJob{UserID: "u1", Kind: models.BackupJobKindAccountBackup}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := backupJobOwnerName(ctx, tc.users, &tc.job); got != tc.want {
				t.Fatalf("backupJobOwnerName = %q, want %q", got, tc.want)
			}
		})
	}
}

// materializeAgent answers backup.materialize with a directory the test made,
// as the agent does with the restored snapshot, and accepts the cleanup call.
type materializeAgent struct{ path string }

func (m *materializeAgent) Call(_ context.Context, command string, _ any) (json.RawMessage, error) {
	if command == "backup.materialize" {
		return json.Marshal(map[string]string{"path": m.path})
	}
	return json.RawMessage(`{}`), nil
}

// The name reaches the browser: streamBackupArtifact puts the base name in
// Content-Disposition, with the extension of the archive it actually builds,
// and falls back to the job ID when it has no name.
func TestStreamBackupArtifact_DownloadName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "01JOBNAME")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ base, want string }{
		{"backup_alice_2026-10-11_0430", "backup_alice_2026-10-11_0430.tar"},
		{"", "01JOBNAME.tar"},
	} {
		c, w := newGateCtx()
		job := &models.BackupJob{ID: "01JOBNAME", Status: models.BackupJobStatusSucceeded, SnapshotID: "snap1"}
		streamBackupArtifact(c, &materializeAgent{path: dir}, job, tc.base, nil, noopLogErr)
		cd := w.Header().Get("Content-Disposition")
		if w.Code != 200 || !strings.Contains(cd, `filename="`+tc.want) {
			t.Fatalf("base %q: code %d, Content-Disposition %q, want a filename starting %q", tc.base, w.Code, cd, tc.want)
		}
	}
}

// Without zstd the archive is a plain tar, and keeps its name: the PATH holds
// only tar, so this covers the branch a host with zstd never takes.
func TestStreamBackupArtifact_DownloadNameWithoutZstd(t *testing.T) {
	tarPath, err := exec.LookPath("tar")
	if err != nil {
		t.Skip("tar not installed")
	}
	bin := t.TempDir()
	if err := os.Symlink(tarPath, filepath.Join(bin, "tar")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	dir := filepath.Join(t.TempDir(), "01JOBNAME")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	c, w := newGateCtx()
	job := &models.BackupJob{ID: "01JOBNAME", Status: models.BackupJobStatusSucceeded, SnapshotID: "snap1"}
	streamBackupArtifact(c, &materializeAgent{path: dir}, job, "backup_alice_2026-10-11_0430", nil, noopLogErr)
	cd := w.Header().Get("Content-Disposition")
	if w.Code != 200 || !strings.Contains(cd, `filename="backup_alice_2026-10-11_0430.tar"`) {
		t.Fatalf("code %d, Content-Disposition %q, want backup_alice_2026-10-11_0430.tar", w.Code, cd)
	}
}
