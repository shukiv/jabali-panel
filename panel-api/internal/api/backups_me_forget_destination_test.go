package api

// An account's own Delete, and the "prune" policy at the plan's backup limit,
// told the agent to forget a backup by job alone. The agent then looked in its
// default local repository: a backup on an SFTP, S3 or other destination was
// not found there, the row was deleted, and the backup's data stayed in its
// repository. The forget now goes to the job's destination, as the admin
// Delete's does.
//
// The prune loop also counted every job row, failed and cancelled ones
// included, while the limit counts only retained backups (GH #1045). With
// enough failed rows it deleted every backup the account had.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// forgetFakeJobs holds the account's retained backups, oldest first, plus a
// number of failed rows that count as rows but are not retained.
type forgetFakeJobs struct {
	repository.BackupJobRepository
	retained []models.BackupJob
	failed   int
	deleted  []string
}

func (f *forgetFakeJobs) Get(_ context.Context, id string) (*models.BackupJob, error) {
	for _, j := range f.retained {
		if j.ID == id {
			j := j
			return &j, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (f *forgetFakeJobs) ListForUser(context.Context, string, int, int) ([]models.BackupJob, int64, error) {
	return nil, int64(len(f.retained) + f.failed), nil
}

func (f *forgetFakeJobs) CountRetainedForUser(context.Context, string) (int64, error) {
	return int64(len(f.retained)), nil
}

func (f *forgetFakeJobs) OldestAccountBackupForUser(context.Context, string) (*models.BackupJob, error) {
	if len(f.retained) == 0 {
		return nil, repository.ErrNotFound
	}
	j := f.retained[0]
	return &j, nil
}

func (f *forgetFakeJobs) Delete(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	for i, j := range f.retained {
		if j.ID == id {
			f.retained = append(f.retained[:i], f.retained[i+1:]...)
			break
		}
	}
	return nil
}

type forgetFakeDests struct {
	repository.BackupDestinationRepository
	dests map[string]*models.BackupDestination
	err   error
}

func (f *forgetFakeDests) Get(_ context.Context, id string) (*models.BackupDestination, error) {
	if f.err != nil {
		return nil, f.err
	}
	if d, ok := f.dests[id]; ok {
		return d, nil
	}
	return nil, repository.ErrNotFound
}

// forgetAgent records each backup.forget and answers the password tempfile
// write the way the agent does.
type forgetAgent struct {
	forgets   []map[string]any
	forgetErr error
}

const forgetPasswordFile = "/run/jabali/restic-pw/test"

func (a *forgetAgent) Call(_ context.Context, command string, params any) (json.RawMessage, error) {
	switch command {
	case "backup.repo.password.write_temp":
		return json.RawMessage(`{"path":"` + forgetPasswordFile + `"}`), nil
	case "backup.forget":
		a.forgets = append(a.forgets, params.(map[string]any))
		if a.forgetErr != nil {
			return nil, a.forgetErr
		}
	}
	return json.RawMessage(`{}`), nil
}

// sftpDestWithPassword is a destination with its own sealed restic password.
func sftpDestWithPassword(t *testing.T, key ssokey.Key) *models.BackupDestination {
	t.Helper()
	sealed, err := key.Seal([]byte("destination-password"))
	if err != nil {
		t.Fatal(err)
	}
	return &models.BackupDestination{
		ID: "sftp1", Kind: models.BackupDestinationKindSFTP, Enabled: true,
		URL: "sftp:backup@storage.example:/srv/restic", PasswordEnc: sealed,
	}
}

func onSFTP(id string) models.BackupJob {
	d := "sftp1"
	return models.BackupJob{ID: id, UserID: "u1", Kind: models.BackupJobKindAccountBackup,
		Status: models.BackupJobStatusSucceeded, DestinationID: &d}
}

func assertForgotFromDest(t *testing.T, p map[string]any, jobID string, dest *models.BackupDestination) {
	t.Helper()
	if p["job_id"] != jobID {
		t.Errorf("forgot job %v, want %s", p["job_id"], jobID)
	}
	if p["repo_url"] != dest.URL {
		t.Errorf("forget went to repo %v, want the job's destination %q", p["repo_url"], dest.URL)
	}
	if p["password_file"] != forgetPasswordFile {
		t.Errorf("forget used password file %v, want the destination's own (%q)", p["password_file"], forgetPasswordFile)
	}
}

func TestMeDeleteBackup_ForgetsFromTheJobsDestination(t *testing.T) {
	key := ssokey.Key{9}
	dest := sftpDestWithPassword(t, key)
	jobs := &forgetFakeJobs{retained: []models.BackupJob{onSFTP("j1")}}
	ag := &forgetAgent{}
	h := &meBackupHandler{cfg: MeBackupsHandlerConfig{
		Jobs: jobs, Agent: ag, SSOKey: &key,
		Destinations: &forgetFakeDests{dests: map[string]*models.BackupDestination{"sftp1": dest}},
	}}

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/api/v1/me/backups/j1", nil)
	c.Params = gin.Params{{Key: "id", Value: "j1"}}
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
	h.delete(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("delete: status %d, body %s", rec.Code, rec.Body.String())
	}
	if len(ag.forgets) != 1 {
		t.Fatalf("want one backup.forget, got %d", len(ag.forgets))
	}
	assertForgotFromDest(t, ag.forgets[0], "j1", dest)
}

func TestMeDeleteBackup_DestinationLookupFails_KeepsTheRow(t *testing.T) {
	jobs := &forgetFakeJobs{retained: []models.BackupJob{onSFTP("j1")}}
	ag := &forgetAgent{}
	h := &meBackupHandler{cfg: MeBackupsHandlerConfig{
		Jobs: jobs, Agent: ag, Destinations: &forgetFakeDests{err: errors.New("db down")},
	}}

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/api/v1/me/backups/j1", nil)
	c.Params = gin.Params{{Key: "id", Value: "j1"}}
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
	h.delete(c)

	if rec.Code == http.StatusOK {
		t.Fatal("delete must fail when the backup's destination cannot be read")
	}
	if len(ag.forgets) != 0 || len(jobs.deleted) != 0 {
		t.Errorf("nothing may be forgotten or deleted; forgets %v, deleted %v", ag.forgets, jobs.deleted)
	}
}

func TestPruneOldestOwned_ForgetsFromDestination_CountsRetainedOnly(t *testing.T) {
	key := ssokey.Key{9}
	dest := sftpDestWithPassword(t, key)
	// Three retained backups and ten failed attempts, at a limit of three.
	jobs := &forgetFakeJobs{retained: []models.BackupJob{onSFTP("a"), onSFTP("b"), onSFTP("c")}, failed: 10}
	ag := &forgetAgent{}
	h := &meBackupHandler{cfg: MeBackupsHandlerConfig{
		Jobs: jobs, Agent: ag, SSOKey: &key,
		Destinations: &forgetFakeDests{dests: map[string]*models.BackupDestination{"sftp1": dest}},
	}}

	if !h.pruneOldestOwned(context.Background(), &models.User{ID: "u1"}, 3) {
		t.Fatal("pruneOldestOwned must make room for one backup")
	}
	if len(jobs.deleted) != 1 || jobs.deleted[0] != "a" {
		t.Fatalf("deleted %v, want only the oldest [a]: failed attempts don't count toward the limit", jobs.deleted)
	}
	assertForgotFromDest(t, ag.forgets[0], "a", dest)
}

func TestPruneOldestOwned_ForgetFails_KeepsTheBackup(t *testing.T) {
	jobs := &forgetFakeJobs{retained: []models.BackupJob{onSFTP("a")}}
	ag := &forgetAgent{forgetErr: errors.New("repository is already locked")}
	h := &meBackupHandler{cfg: MeBackupsHandlerConfig{
		Jobs: jobs, Agent: ag,
		Destinations: &forgetFakeDests{dests: map[string]*models.BackupDestination{
			"sftp1": {ID: "sftp1", Kind: models.BackupDestinationKindSFTP, Enabled: true, URL: "sftp:backup@storage.example:/srv/restic"},
		}},
	}}

	if h.pruneOldestOwned(context.Background(), &models.User{ID: "u1"}, 1) {
		t.Fatal("pruneOldestOwned must not report room when the backup could not be forgotten")
	}
	if len(jobs.deleted) != 0 {
		t.Errorf("the row of a backup still in its repository must stay; deleted %v", jobs.deleted)
	}
}
