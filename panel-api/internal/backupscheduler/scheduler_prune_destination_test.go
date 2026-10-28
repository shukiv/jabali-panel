package backupscheduler

// A plan's "prune" policy makes a scheduled backup at the plan's limit forget
// the account's oldest backup first. The forget used to name only the job, so
// the agent looked in its default local repository: a backup on an SFTP, S3
// or other destination was not found there, the call "succeeded", the row was
// deleted and the backup's data stayed in its repository, uncounted and never
// removed. The forget now goes to the job's own destination.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// pruneFakeJobs holds the account's retained backups, oldest first.
type pruneFakeJobs struct {
	repository.BackupJobRepository
	retained []models.BackupJob
	deleted  []string
}

func (f *pruneFakeJobs) CountRetainedForUser(context.Context, string) (int64, error) {
	return int64(len(f.retained)), nil
}

func (f *pruneFakeJobs) OldestAccountBackupForUser(context.Context, string) (*models.BackupJob, error) {
	if len(f.retained) == 0 {
		return nil, repository.ErrNotFound
	}
	j := f.retained[0]
	return &j, nil
}

func (f *pruneFakeJobs) Delete(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	for i, j := range f.retained {
		if j.ID == id {
			f.retained = append(f.retained[:i], f.retained[i+1:]...)
			break
		}
	}
	return nil
}

type pruneFakeDests struct {
	repository.BackupDestinationRepository
	dests map[string]*models.BackupDestination
	err   error
}

func (f *pruneFakeDests) Get(_ context.Context, id string) (*models.BackupDestination, error) {
	if f.err != nil {
		return nil, f.err
	}
	d, ok := f.dests[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return d, nil
}

// pruneAgent records each backup.forget and answers the password tempfile
// write the way the agent does.
type pruneAgent struct {
	forgets []map[string]any
}

const prunePasswordFile = "/run/jabali/restic-pw/test"

func (a *pruneAgent) Call(_ context.Context, command string, params any) (json.RawMessage, error) {
	switch command {
	case "backup.repo.password.write_temp":
		return json.RawMessage(`{"path":"` + prunePasswordFile + `"}`), nil
	case "backup.forget":
		a.forgets = append(a.forgets, params.(map[string]any))
	}
	return json.RawMessage(`{}`), nil
}

func strPtr(s string) *string { return &s }

func pruneScheduler(jobs *pruneFakeJobs, dests *pruneFakeDests, ag *pruneAgent, key *ssokey.Key) *Scheduler {
	return &Scheduler{deps: Deps{Jobs: jobs, Destinations: dests, Agent: ag, SSOKey: key, Log: slog.Default()}}
}

func TestPruneOldestForUser_ForgetsFromTheJobsDestination(t *testing.T) {
	key := ssokey.Key{7}
	sealed, err := key.Seal([]byte("destination-password"))
	if err != nil {
		t.Fatal(err)
	}
	dest := &models.BackupDestination{
		ID: "sftp1", Kind: models.BackupDestinationKindSFTP, Enabled: true,
		URL: "sftp:backup@storage.example:/srv/restic", PasswordEnc: sealed,
	}
	jobs := &pruneFakeJobs{retained: []models.BackupJob{
		{ID: "old", UserID: "u1", Kind: models.BackupJobKindAccountBackup, DestinationID: strPtr("sftp1")},
		{ID: "new", UserID: "u1", Kind: models.BackupJobKindAccountBackup, DestinationID: strPtr("sftp1")},
	}}
	ag := &pruneAgent{}
	s := pruneScheduler(jobs, &pruneFakeDests{dests: map[string]*models.BackupDestination{"sftp1": dest}}, ag, &key)

	if !s.pruneOldestForUser(context.Background(), &models.User{ID: "u1"}, 2) {
		t.Fatal("pruneOldestForUser must report room once the oldest backup is gone")
	}
	if len(ag.forgets) != 1 {
		t.Fatalf("want one backup.forget, got %d", len(ag.forgets))
	}
	p := ag.forgets[0]
	if p["job_id"] != "old" {
		t.Errorf("forgot job %v, want the oldest (old)", p["job_id"])
	}
	if p["repo_url"] != dest.URL {
		t.Errorf("forget went to repo %v, want the job's destination %q", p["repo_url"], dest.URL)
	}
	if p["password_file"] != prunePasswordFile {
		t.Errorf("forget used password file %v, want the destination's own (%q)", p["password_file"], prunePasswordFile)
	}
	if len(jobs.deleted) != 1 || jobs.deleted[0] != "old" {
		t.Errorf("deleted rows %v, want [old]", jobs.deleted)
	}
}

func TestPruneOldestForUser_DestinationLookupFails_KeepsTheBackup(t *testing.T) {
	jobs := &pruneFakeJobs{retained: []models.BackupJob{
		{ID: "old", UserID: "u1", Kind: models.BackupJobKindAccountBackup, DestinationID: strPtr("sftp1")},
	}}
	ag := &pruneAgent{}
	s := pruneScheduler(jobs, &pruneFakeDests{err: errors.New("db down")}, ag, nil)

	if s.pruneOldestForUser(context.Background(), &models.User{ID: "u1"}, 1) {
		t.Fatal("pruneOldestForUser must not report room when the backup could not be forgotten")
	}
	if len(ag.forgets) != 0 {
		t.Errorf("no forget may run against a guessed repository; got %v", ag.forgets)
	}
	if len(jobs.deleted) != 0 {
		t.Errorf("the row must stay so a later run retries; deleted %v", jobs.deleted)
	}
}

// A job whose destination row was deleted, or a pre-destination job, is
// forgotten from the agent's default repository, as the admin Delete does.
func TestPruneOldestForUser_NoDestination_UsesDefaultRepository(t *testing.T) {
	jobs := &pruneFakeJobs{retained: []models.BackupJob{
		{ID: "gone", UserID: "u1", Kind: models.BackupJobKindAccountBackup, DestinationID: strPtr("deleted-dest")},
		{ID: "legacy", UserID: "u1", Kind: models.BackupJobKindAccountBackup},
	}}
	ag := &pruneAgent{}
	s := pruneScheduler(jobs, &pruneFakeDests{dests: map[string]*models.BackupDestination{}}, ag, nil)

	if !s.pruneOldestForUser(context.Background(), &models.User{ID: "u1"}, 1) {
		t.Fatal("pruneOldestForUser must make room")
	}
	if len(ag.forgets) != 2 {
		t.Fatalf("want two forgets, got %d", len(ag.forgets))
	}
	for _, p := range ag.forgets {
		if _, ok := p["repo_url"]; ok {
			t.Errorf("job %v: forget named repo %v, want the agent's default", p["job_id"], p["repo_url"])
		}
	}
}
