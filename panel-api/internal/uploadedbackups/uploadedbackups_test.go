package uploadedbackups

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeRepo keeps rows in memory; busy ids refuse DeleteIfIdle.
type fakeRepo struct {
	repository.UploadedBackupRepository
	rows    map[string]*models.UploadedBackup
	busy    map[string]bool
	deleted []string
}

func (f *fakeRepo) ListExpired(_ context.Context, now time.Time) ([]models.UploadedBackup, error) {
	var out []models.UploadedBackup
	for _, b := range f.rows {
		if b.ExpiresAt != nil && b.ExpiresAt.Before(now) {
			out = append(out, *b)
		}
	}
	return out, nil
}

func (f *fakeRepo) ListAll(context.Context) ([]models.UploadedBackup, error) {
	var out []models.UploadedBackup
	for _, b := range f.rows {
		out = append(out, *b)
	}
	return out, nil
}

func (f *fakeRepo) DeleteIfIdle(_ context.Context, id string, _ time.Time) error {
	if f.busy[id] {
		return repository.ErrUploadedBackupBusy
	}
	delete(f.rows, id)
	f.deleted = append(f.deleted, id)
	return nil
}

const (
	idExpired = "01K00000000000000000000001"
	idBusy    = "01K00000000000000000000002"
	idKept    = "01K00000000000000000000003"
	idOrphan  = "01K00000000000000000000004"
	idYoung   = "01K00000000000000000000005"
)

func archive(t *testing.T, id string, age time.Duration, now time.Time) string {
	t.Helper()
	p := Path(id)
	if err := os.WriteFile(p, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
		t.Fatal(err)
	}
	return p
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// GH #1993: a "keep 7 days" archive goes once it expires, unless a restore is
// using it; an archive file no row owns goes once it is an hour old; the
// archives of live rows stay.
func TestSweep(t *testing.T) {
	Dir = t.TempDir()
	t.Cleanup(func() { Dir = DefaultDir })
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Minute), now.Add(time.Hour)
	repo := &fakeRepo{
		rows: map[string]*models.UploadedBackup{
			idExpired: {ID: idExpired, ExpiresAt: &past},
			idBusy:    {ID: idBusy, ExpiresAt: &past},
			idKept:    {ID: idKept, ExpiresAt: &future},
		},
		busy: map[string]bool{idBusy: true},
	}
	files := map[string]string{
		idExpired: archive(t, idExpired, 8*24*time.Hour, now),
		idBusy:    archive(t, idBusy, 8*24*time.Hour, now),
		idKept:    archive(t, idKept, 30*24*time.Hour, now),
		idOrphan:  archive(t, idOrphan, 2*time.Hour, now),
		idYoung:   archive(t, idYoung, 10*time.Minute, now),
	}

	Sweep(context.Background(), repo, now, slog.New(slog.NewTextHandler(io.Discard, nil)))

	for id, want := range map[string]bool{idExpired: false, idBusy: true, idKept: true, idOrphan: false, idYoung: true} {
		if got := exists(files[id]); got != want {
			t.Errorf("%s: archive present=%v, want %v", id, got, want)
		}
	}
	if strings.Join(repo.deleted, ",") != idExpired {
		t.Errorf("deleted rows %v, want only %s", repo.deleted, idExpired)
	}
}

func TestPathTakesOnlyAULID(t *testing.T) {
	for _, bad := range []string{"", "../etc/passwd", "01K0000000000000000000000/", "01k00000000000000000000001", "01K0000000000000000000000U"} {
		if ValidID(bad) {
			t.Errorf("ValidID(%q) = true", bad)
		}
	}
	if !ValidID(idKept) {
		t.Errorf("ValidID(%q) = false", idKept)
	}
}

func TestExpiresAt(t *testing.T) {
	created := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	if e := ExpiresAt(models.UploadedBackupKeep7Days, created); e == nil || !e.Equal(created.Add(7*24*time.Hour)) {
		t.Errorf("keep_7_days expires at %v, want 7 days later", e)
	}
	for _, r := range []string{models.UploadedBackupKeep, models.UploadedBackupDeleteAfterRestore} {
		if e := ExpiresAt(r, created); e != nil {
			t.Errorf("%s expires at %v, want never", r, e)
		}
	}
}

// The installer's tmpfiles reaper removes everything in /var/lib/jabali-uploads
// that is 12 hours old. Kept archives must be excluded, on a fresh install and
// on every `jabali update`, or a kept backup silently disappears overnight.
func TestInstallerReaperExcludesKeptArchives(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	sh := string(raw)
	body := regexp.MustCompile(`(?s)\ninstall_uploads_reaper\(\) \{\n(.*?)\n\}\n`).FindStringSubmatch(sh)
	if body == nil {
		t.Fatal("install.sh has no install_uploads_reaper() function")
	}
	for _, line := range []string{"e /var/lib/jabali-uploads - - - 12h", "x " + DefaultDir} {
		if !strings.Contains(body[1], line) {
			t.Errorf("install_uploads_reaper does not write %q", line)
		}
	}
	for _, caller := range []string{"write_systemd_unit", "provision_new_software"} {
		fn := regexp.MustCompile(`(?s)\n` + caller + `\(\) \{\n(.*?)\n\}\n`).FindStringSubmatch(sh)
		if fn == nil || !regexp.MustCompile(`(?m)^\s+install_uploads_reaper\s*$`).MatchString(fn[1]) {
			t.Errorf("%s does not call install_uploads_reaper", caller)
		}
	}
}
