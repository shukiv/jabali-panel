package backupscheduler

// GH #2300: under a plan's default "reject" policy, a scheduled run at the
// plan's backup limit was skipped. A schedule whose keep rule reached the
// limit (an account's "Days to keep" may be set up to it) then never ran
// again: the nightly sweep keeps exactly what the rule asks for, so the
// account stayed at the limit and every later run was refused. Daily
// schedules too. A schedule with a keep rule now removes its own oldest
// backup to make room, since its sweep would remove that one anyway; it never
// removes its last, and without a keep rule the run is still refused.

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// limitFakeJobs holds the account's retained backups, oldest first, each
// tagged with the schedule that made it ("" for a manual backup).
type limitFakeJobs struct {
	repository.BackupJobRepository
	retained []models.BackupJob
	deleted  []string
	created  int
}

func (f *limitFakeJobs) CountRetainedForUser(context.Context, string) (int64, error) {
	return int64(len(f.retained)), nil
}

func (f *limitFakeJobs) ofSchedule(scheduleID string) []models.BackupJob {
	var out []models.BackupJob
	for _, j := range f.retained {
		if j.ScheduleID != nil && *j.ScheduleID == scheduleID {
			out = append(out, j)
		}
	}
	return out
}

func (f *limitFakeJobs) CountRetainedForSchedule(_ context.Context, _ string, scheduleID string) (int64, error) {
	return int64(len(f.ofSchedule(scheduleID))), nil
}

func (f *limitFakeJobs) OldestRetainedForSchedule(_ context.Context, _ string, scheduleID string) (*models.BackupJob, error) {
	own := f.ofSchedule(scheduleID)
	if len(own) == 0 {
		return nil, repository.ErrNotFound
	}
	return &own[0], nil
}

func (f *limitFakeJobs) Delete(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	for i, j := range f.retained {
		if j.ID == id {
			f.retained = append(f.retained[:i], f.retained[i+1:]...)
			break
		}
	}
	return nil
}

func (f *limitFakeJobs) HasPendingForTarget(context.Context, string, string, string) (bool, error) {
	return false, nil
}

func (f *limitFakeJobs) Create(context.Context, *models.BackupJob) error { f.created++; return nil }

// limitFakePackages only has to be non-nil: the caller hands the plan in.
type limitFakePackages struct{ repository.PackageRepository }

type limitAgent struct{ forgets []map[string]any }

func (a *limitAgent) Call(_ context.Context, command string, params any) (json.RawMessage, error) {
	if command == "backup.forget" {
		a.forgets = append(a.forgets, params.(map[string]any))
	}
	return json.RawMessage(`{}`), nil
}

func scheduled(id, scheduleID string) models.BackupJob {
	d := "local1"
	j := models.BackupJob{ID: id, UserID: "u1", Kind: models.BackupJobKindAccountBackup,
		Status: models.BackupJobStatusSucceeded, DestinationID: &d}
	if scheduleID != "" {
		sid := scheduleID
		j.ScheduleID = &sid
	}
	return j
}

func intp(n int) *int { return &n }

// rejectPlan allows three backups and refuses new ones at the limit.
var rejectPlan = &models.HostingPackage{MaxBackups: 3, BackupRetentionPolicy: "reject"}

var limitDest = &models.BackupDestination{ID: "local1", Kind: models.BackupDestinationKindLocal, Enabled: true, URL: "/var/backups/jabali/local1"}

func limitScheduler(jobs *limitFakeJobs, ag *limitAgent) *Scheduler {
	return &Scheduler{deps: Deps{
		Jobs: jobs, Packages: limitFakePackages{}, Agent: ag, Log: slog.Default(),
		Destinations: &pruneFakeDests{dests: map[string]*models.BackupDestination{"local1": limitDest}},
	}}
}

func TestEnqueueAccountBackup_AtLimit_ScheduleWithKeepRule_DropsItsOldest(t *testing.T) {
	jobs := &limitFakeJobs{retained: []models.BackupJob{
		scheduled("d1", "s1"), scheduled("d2", "s1"), scheduled("d3", "s1"),
	}}
	ag := &limitAgent{}
	s := limitScheduler(jobs, ag)
	sched := models.BackupSchedule{ID: "s1", Kind: models.BackupScheduleKindAccount, KeepDaily: intp(3)}

	if !s.enqueueAccountBackup(context.Background(), sched, &models.User{ID: "u1"}, rejectPlan, limitDest, "run1") {
		t.Fatal("a schedule with a keep rule must make room at the limit and run")
	}
	if jobs.created != 1 {
		t.Errorf("want the new backup queued, created=%d", jobs.created)
	}
	if len(jobs.deleted) != 1 || jobs.deleted[0] != "d1" {
		t.Errorf("deleted %v, want the schedule's oldest [d1]", jobs.deleted)
	}
	if len(ag.forgets) != 1 || ag.forgets[0]["repo_url"] != limitDest.URL {
		t.Errorf("want one forget from the backup's destination %q, got %v", limitDest.URL, ag.forgets)
	}
}

func TestEnqueueAccountBackup_AtLimit_NoKeepRule_StillRejects(t *testing.T) {
	jobs := &limitFakeJobs{retained: []models.BackupJob{
		scheduled("d1", "s1"), scheduled("d2", "s1"), scheduled("d3", "s1"),
	}}
	ag := &limitAgent{}
	s := limitScheduler(jobs, ag)
	sched := models.BackupSchedule{ID: "s1", Kind: models.BackupScheduleKindAccount}

	if s.enqueueAccountBackup(context.Background(), sched, &models.User{ID: "u1"}, rejectPlan, limitDest, "run1") {
		t.Fatal("without a keep rule nothing removes old backups, so the run is refused")
	}
	if jobs.created != 0 || len(jobs.deleted) != 0 || len(ag.forgets) != 0 {
		t.Errorf("nothing may change; created=%d deleted=%v forgets=%v", jobs.created, jobs.deleted, ag.forgets)
	}
}

// The limit is held by manual backups and one scheduled backup: making room
// would remove the schedule's only backup, so the run is refused.
func TestEnqueueAccountBackup_AtLimit_NeverRemovesTheSchedulesLast(t *testing.T) {
	jobs := &limitFakeJobs{retained: []models.BackupJob{
		scheduled("m1", ""), scheduled("m2", ""), scheduled("d1", "s1"),
	}}
	ag := &limitAgent{}
	s := limitScheduler(jobs, ag)
	sched := models.BackupSchedule{ID: "s1", Kind: models.BackupScheduleKindAccount, KeepDaily: intp(7)}

	if s.enqueueAccountBackup(context.Background(), sched, &models.User{ID: "u1"}, rejectPlan, limitDest, "run1") {
		t.Fatal("the run must be refused rather than remove the schedule's only backup")
	}
	if jobs.created != 0 || len(jobs.deleted) != 0 || len(ag.forgets) != 0 {
		t.Errorf("nothing may change; created=%d deleted=%v forgets=%v", jobs.created, jobs.deleted, ag.forgets)
	}
}

// Another schedule's backups are never touched.
func TestEnqueueAccountBackup_AtLimit_LeavesOtherSchedulesAlone(t *testing.T) {
	jobs := &limitFakeJobs{retained: []models.BackupJob{
		scheduled("o1", "other"), scheduled("d1", "s1"), scheduled("d2", "s1"),
	}}
	ag := &limitAgent{}
	s := limitScheduler(jobs, ag)
	sched := models.BackupSchedule{ID: "s1", Kind: models.BackupScheduleKindAccount, KeepWeekly: intp(4)}

	if !s.enqueueAccountBackup(context.Background(), sched, &models.User{ID: "u1"}, rejectPlan, limitDest, "run1") {
		t.Fatal("the schedule has two backups, so it can make room")
	}
	if len(jobs.deleted) != 1 || jobs.deleted[0] != "d1" {
		t.Errorf("deleted %v, want this schedule's oldest [d1], not another schedule's", jobs.deleted)
	}
}
