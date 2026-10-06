// Package uploadedbackups keeps the account backups admins upload from another
// server (GH #1993): where each archive lives, how long it stays, and the
// sweeper that removes expired archives and files no row owns.
//
// The archives live in DefaultDir, under /var/lib/jabali-uploads so the agent
// may read them (backup.restore_from_tar only takes a path there). The
// installer's tmpfiles reaper removes anything in /var/lib/jabali-uploads that
// is 12 hours old, except this directory (install.sh install_uploads_reaper).
package uploadedbackups

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// DefaultDir is where kept archives live.
const DefaultDir = "/var/lib/jabali-uploads/kept"

// Dir is DefaultDir; tests point it elsewhere.
var Dir = DefaultDir

// RestoreStaleAfter is how long a restore may hold an archive. The restore's
// own deadline is 60 minutes; a claim older than this belongs to a restore the
// panel lost (a restart) and can be taken over.
const RestoreStaleAfter = 65 * time.Minute

// orphanGrace is how old a file with no row must be before the sweeper removes
// it, so it never races a registration between the rename and the row insert.
const orphanGrace = time.Hour

// sweepInterval is how often the sweeper runs.
const sweepInterval = time.Hour

var idRE = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// ValidID reports whether id is a ULID, the only thing Path joins into a path.
func ValidID(id string) bool { return idRE.MatchString(id) }

// Path is the archive file of the uploaded backup id. id must be ValidID.
func Path(id string) string { return filepath.Join(Dir, id+".tar.zst") }

// ValidRetention reports whether r is a retention choice.
func ValidRetention(r string) bool {
	switch r {
	case models.UploadedBackupKeep, models.UploadedBackupKeep7Days, models.UploadedBackupDeleteAfterRestore:
		return true
	}
	return false
}

// ExpiresAt is when an archive uploaded at created with retention r is
// removed, or nil when it stays until deleted (or, for delete_after_restore,
// until a restore succeeds).
func ExpiresAt(r string, created time.Time) *time.Time {
	if r != models.UploadedBackupKeep7Days {
		return nil
	}
	t := created.Add(7 * 24 * time.Hour)
	return &t
}

// StaleBefore is the restore start time before which a claim is stale.
func StaleBefore(now time.Time) time.Time { return now.Add(-RestoreStaleAfter) }

// Remove deletes the archive of id; a missing file is fine.
func Remove(id string) error {
	if err := os.Remove(Path(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Sweep removes the expired archives no restore holds, and the archive files
// no row owns that are older than an hour.
func Sweep(ctx context.Context, repo repository.UploadedBackupRepository, now time.Time, log *slog.Logger) {
	expired, err := repo.ListExpired(ctx, now)
	if err != nil {
		log.Warn("uploaded backups: list expired", "error", err)
		return
	}
	for _, b := range expired {
		if !ValidID(b.ID) {
			continue
		}
		if err := repo.DeleteIfIdle(ctx, b.ID, StaleBefore(now)); err != nil {
			if !errors.Is(err, repository.ErrUploadedBackupBusy) && !errors.Is(err, repository.ErrNotFound) {
				log.Warn("uploaded backups: delete expired", "id", b.ID, "error", err)
			}
			continue
		}
		if err := Remove(b.ID); err != nil {
			log.Warn("uploaded backups: remove expired archive", "id", b.ID, "error", err)
			continue
		}
		log.Info("uploaded backups: removed expired archive", "id", b.ID, "account", b.AccountUsername)
	}

	rows, err := repo.ListAll(ctx)
	if err != nil {
		log.Warn("uploaded backups: list", "error", err)
		return
	}
	owned := make(map[string]bool, len(rows))
	for _, b := range rows {
		owned[b.ID] = true
	}
	files, _ := filepath.Glob(filepath.Join(Dir, "*.tar.zst"))
	for _, f := range files {
		id := strings.TrimSuffix(filepath.Base(f), ".tar.zst")
		if owned[id] {
			continue
		}
		fi, err := os.Lstat(f)
		if err != nil || now.Sub(fi.ModTime()) < orphanGrace {
			continue
		}
		if err := os.Remove(f); err != nil {
			log.Warn("uploaded backups: remove orphan archive", "file", f, "error", err)
			continue
		}
		log.Info("uploaded backups: removed an archive no backup row owns", "file", f)
	}
}

// StartSweeper runs Sweep hourly until ctx is cancelled. A nil repo disables it.
func StartSweeper(ctx context.Context, repo repository.UploadedBackupRepository, log *slog.Logger) {
	if repo == nil {
		return
	}
	t := time.NewTicker(sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			Sweep(ctx, repo, time.Now().UTC(), log)
		}
	}
}
