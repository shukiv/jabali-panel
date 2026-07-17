package api

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeDestRepo implements just Get; the rest of the interface panics if used.
type fakeDestRepo struct {
	repository.BackupDestinationRepository
	dest *models.BackupDestination
	err  error
}

func (f fakeDestRepo) Get(_ context.Context, _ string) (*models.BackupDestination, error) {
	return f.dest, f.err
}

// TestMaterializeDestParams — the download must forward the job's DESTINATION
// repo so the agent opens the right restic repo, not the local default (which
// 404s "no snapshots for job" — GH #462).
func TestMaterializeDestParams(t *testing.T) {
	dest := &models.BackupDestination{ID: "01D", Kind: "sftp", URL: "sftp:user@host:/backups", CredentialsRef: strptr("cred01")}

	t.Run("job with destination resolves repo_url", func(t *testing.T) {
		repo := fakeDestRepo{dest: dest}
		job := &models.BackupJob{ID: "01J", DestinationID: strptr("01D")}
		p := materializeDestParams(context.Background(), repo, job)
		if p == nil {
			t.Fatal("expected params, got nil")
		}
		if p["repo_url"] != "sftp:user@host:/backups" {
			t.Errorf("repo_url = %v, want the destination URL", p["repo_url"])
		}
		if p["credentials_ref"] != "cred01" {
			t.Errorf("credentials_ref = %v, want cred01", p["credentials_ref"])
		}
	})

	t.Run("job without destination = nil (local default)", func(t *testing.T) {
		repo := fakeDestRepo{dest: dest}
		job := &models.BackupJob{ID: "01J"} // DestinationID nil
		if p := materializeDestParams(context.Background(), repo, job); p != nil {
			t.Errorf("expected nil for local backup, got %v", p)
		}
	})

	t.Run("nil repo = nil", func(t *testing.T) {
		job := &models.BackupJob{ID: "01J", DestinationID: strptr("01D")}
		if p := materializeDestParams(context.Background(), nil, job); p != nil {
			t.Errorf("expected nil when repo unavailable, got %v", p)
		}
	})
}
