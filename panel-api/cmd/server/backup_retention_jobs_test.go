package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// snap builds one stage snapshot of an account backup job.
func snap(id, job, user, stage, at string) resticSnapshot {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		panic(err)
	}
	return resticSnapshot{
		ID: id, Time: t, Hostname: "box",
		Tags: []string{"jabali", "kind=account_backup", "job-id=" + job, "user-id=" + user, "stage=" + stage, "schedule-id=s1"},
	}
}

// jobSet is the three stages of one complete account backup.
func jobSet(job, user, home, db, manifest string) []resticSnapshot {
	return []resticSnapshot{
		snap(job+"-home", job, user, "home", home),
		snap(job+"-db", job, user, "db", db),
		snap(job+"-manifest", job, user, "manifest", manifest),
	}
}

func jobIDs(js []retentionJob) []string {
	out := []string{}
	for _, j := range js {
		out = append(out, j.JobID)
	}
	sort.Strings(out)
	return out
}

func eq(a, b []string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

// The reproduced bug: R2 straddles midnight (home on day 1, db + manifest on
// day 2). Per-stage forget with keep-daily=2 kept R1's db+manifest but not its
// home, and only R2's home. Whole-job retention keeps R3 (day 2) and R1 (day 1)
// complete and drops R2 with all of its snapshots.
func TestPlanJobRetention_MidnightStraddleKeepsWholeJobs(t *testing.T) {
	var snaps []resticSnapshot
	snaps = append(snaps, jobSet("R1", "u1", "2026-01-01T22:00:00Z", "2026-01-01T22:01:00Z", "2026-01-01T22:02:00Z")...)
	snaps = append(snaps, jobSet("R2", "u1", "2026-01-01T23:58:00Z", "2026-01-02T00:01:00Z", "2026-01-02T00:02:00Z")...)
	snaps = append(snaps, jobSet("R3", "u1", "2026-01-02T22:00:00Z", "2026-01-02T22:01:00Z", "2026-01-02T22:02:00Z")...)

	plan := planJobRetention(snaps, retentionPolicy{Daily: 2})
	if got := jobIDs(plan.Keep); !eq(got, []string{"R1", "R3"}) {
		t.Fatalf("keep = %v, want [R1 R3]", got)
	}
	if got := jobIDs(plan.Drop); !eq(got, []string{"R2"}) {
		t.Fatalf("drop = %v, want [R2]", got)
	}
	if n := len(plan.Drop[0].SnapshotIDs); n != 3 {
		t.Fatalf("R2 must be forgotten whole (3 snapshots), got %d", n)
	}
}

// Policy counts run per account: one tenant's frequent backups never use up
// another tenant's keep counts.
func TestPlanJobRetention_SeriesAreIndependent(t *testing.T) {
	var snaps []resticSnapshot
	for d := 1; d <= 3; d++ {
		day := fmt.Sprintf("2026-01-0%d", d)
		snaps = append(snaps, jobSet("A"+day, "alice", day+"T01:00:00Z", day+"T01:01:00Z", day+"T01:02:00Z")...)
		snaps = append(snaps, jobSet("B"+day, "bob", day+"T02:00:00Z", day+"T02:01:00Z", day+"T02:02:00Z")...)
	}
	plan := planJobRetention(snaps, retentionPolicy{Daily: 2})
	want := []string{"A2026-01-02", "A2026-01-03", "B2026-01-02", "B2026-01-03"}
	if got := jobIDs(plan.Keep); !eq(got, want) {
		t.Fatalf("keep = %v, want %v", got, want)
	}
	if got := jobIDs(plan.Drop); !eq(got, []string{"A2026-01-01", "B2026-01-01"}) {
		t.Fatalf("drop = %v", got)
	}
}

// A job without a manifest (failed, or still running) goes only once a newer
// complete backup exists in its series; a series with no complete backup is
// kept whole.
func TestPlanJobRetention_IncompleteJobs(t *testing.T) {
	snaps := jobSet("OK", "u1", "2026-01-02T01:00:00Z", "2026-01-02T01:01:00Z", "2026-01-02T01:02:00Z")
	snaps = append(snaps,
		snap("old-home", "OLD", "u1", "home", "2026-01-01T01:00:00Z"),   // failed before OK
		snap("new-home", "NEW", "u1", "home", "2026-01-03T01:00:00Z"),   // newer than OK: maybe running
		snap("lone-home", "LONE", "u2", "home", "2026-01-01T01:00:00Z"), // u2 has no complete backup
	)
	plan := planJobRetention(snaps, retentionPolicy{Daily: 7})
	if got := jobIDs(plan.Keep); !eq(got, []string{"LONE", "NEW", "OK"}) {
		t.Fatalf("keep = %v", got)
	}
	if got := jobIDs(plan.Drop); !eq(got, []string{"OLD"}) {
		t.Fatalf("drop = %v", got)
	}
}

func TestPlanJobRetention_UntaggedAndAmbiguousLeftAlone(t *testing.T) {
	snaps := jobSet("J1", "u1", "2026-01-01T01:00:00Z", "2026-01-01T01:01:00Z", "2026-01-01T01:02:00Z")
	snaps = append(snaps, jobSet("J2", "u1", "2026-01-02T01:00:00Z", "2026-01-02T01:01:00Z", "2026-01-02T01:02:00Z")...)
	snaps = append(snaps, jobSet("J3", "u1", "2026-01-03T01:00:00Z", "2026-01-03T01:01:00Z", "2026-01-03T01:02:00Z")...)
	// J1 also has a snapshot under another account: never dropped.
	snaps = append(snaps, snap("J1-other", "J1", "u9", "home", "2026-01-01T01:03:00Z"))
	untagged := snap("legacy", "", "u1", "home", "2025-12-01T01:00:00Z")
	untagged.Tags = []string{"jabali", "kind=account_backup", "user-id=u1", "schedule-id=s1"}
	snaps = append(snaps, untagged)

	plan := planJobRetention(snaps, retentionPolicy{Daily: 1})
	if plan.Untagged != 1 {
		t.Fatalf("untagged = %d, want 1", plan.Untagged)
	}
	if !eq(plan.Ambiguous, []string{"J1"}) {
		t.Fatalf("ambiguous = %v, want [J1]", plan.Ambiguous)
	}
	// keep-daily=1 over J2, J3: J3 is the newest; J2 is the oldest left in the
	// series with no count left, so it goes.
	if got := jobIDs(plan.Drop); !eq(got, []string{"J2"}) {
		t.Fatalf("drop = %v, want [J2]", got)
	}
}

func TestPlanJobRetention_EmptyPolicyDropsNothing(t *testing.T) {
	snaps := jobSet("J1", "u1", "2026-01-01T01:00:00Z", "2026-01-01T01:01:00Z", "2026-01-01T01:02:00Z")
	if plan := planJobRetention(snaps, retentionPolicy{}); len(plan.Drop) != 0 {
		t.Fatalf("an empty policy must drop nothing, got %v", jobIDs(plan.Drop))
	}
}

// ---- against real restic (skipped where restic is not installed) ----------

type testRepo struct {
	dir, pw string
}

func newTestRepo(t *testing.T) testRepo {
	t.Helper()
	if testing.Short() {
		t.Skip("drives real restic; skipped with -short")
	}
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic not installed")
	}
	dir := t.TempDir()
	r := testRepo{dir: filepath.Join(dir, "repo"), pw: filepath.Join(dir, "pw")}
	if err := os.WriteFile(r.pw, []byte("retention-test-only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data", "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.run(t, "", "init")
	return r
}

func (r testRepo) run(t *testing.T, stdin string, args ...string) []byte {
	t.Helper()
	c := exec.Command("restic", append([]string{"--repo", r.dir, "--password-file", r.pw, "-q"}, args...)...)
	c.Dir = filepath.Dir(r.dir)
	c.Env = append(os.Environ(), "TZ=UTC")
	if stdin != "" {
		c.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		t.Fatalf("restic %v: %v: %s", args, err, errb.String())
	}
	return out.Bytes()
}

// backupStage writes one stage snapshot at time at ("2006-01-02 15:04:05").
func (r testRepo) backupStage(t *testing.T, job, stage, at string) {
	t.Helper()
	tags := []string{"--tag", "jabali", "--tag", "kind=account_backup", "--tag", "job-id=" + job,
		"--tag", "user-id=u1", "--tag", "stage=" + stage, "--tag", "schedule-id=s1"}
	args := append([]string{"backup", "--host", "box", "--time", at}, tags...)
	switch stage {
	case "home":
		r.run(t, "", append(args, "data")...)
	default:
		r.run(t, stage+" of "+job, append(args, "--stdin", "--stdin-filename", stage+".dat")...)
	}
}

func (r testRepo) snapshots(t *testing.T) []resticSnapshot {
	t.Helper()
	var s []resticSnapshot
	if err := json.Unmarshal(r.run(t, "", "snapshots", "--json"), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// stagesByJob maps job-id to its surviving stages.
func stagesByJob(snaps []resticSnapshot) map[string][]string {
	out := map[string][]string{}
	for _, s := range snaps {
		j := tagValue(s.Tags, "job-id")
		out[j] = append(out[j], tagValue(s.Tags, "stage"))
	}
	for j := range out {
		sort.Strings(out[j])
	}
	return out
}

// keepByPolicy must keep exactly what restic itself keeps for one series
// (same host and paths), including restic's "oldest" keep.
func TestKeepByPolicy_MatchesRestic(t *testing.T) {
	r := newTestRepo(t)
	rng := rand.New(rand.NewSource(1701))
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seen := map[string]bool{}
	for len(seen) < 30 {
		at := base.Add(time.Duration(rng.Int63n(int64(100 * 24 * time.Hour)))).Truncate(time.Second)
		s := at.Format("2006-01-02 15:04:05")
		if seen[s] {
			continue
		}
		seen[s] = true
		r.run(t, "", "backup", "--host", "box", "--time", s, "data")
	}
	snaps := r.snapshots(t)
	sort.SliceStable(snaps, func(a, b int) bool { return snaps[a].Time.After(snaps[b].Time) })
	times := make([]time.Time, len(snaps))
	for i, s := range snaps {
		times[i] = s.Time
	}

	for _, p := range []retentionPolicy{
		{Daily: 7}, {Daily: 3, Weekly: 2}, {Weekly: 4, Monthly: 2},
		{Daily: 2, Weekly: 3, Monthly: 6}, {Monthly: 12}, {Daily: 60},
	} {
		args := []string{"forget", "--dry-run", "--json"}
		if p.Daily > 0 {
			args = append(args, "--keep-daily", fmt.Sprint(p.Daily))
		}
		if p.Weekly > 0 {
			args = append(args, "--keep-weekly", fmt.Sprint(p.Weekly))
		}
		if p.Monthly > 0 {
			args = append(args, "--keep-monthly", fmt.Sprint(p.Monthly))
		}
		var groups []struct {
			Keep []resticSnapshot `json:"keep"`
		}
		if err := json.Unmarshal(r.run(t, "", args...), &groups); err != nil {
			t.Fatal(err)
		}
		want := map[string]bool{}
		for _, g := range groups {
			for _, s := range g.Keep {
				want[s.ID] = true
			}
		}
		keep := keepByPolicy(times, p)
		for i, s := range snaps {
			if keep[i] != want[s.ID] {
				t.Errorf("policy %+v: snapshot %s: keepByPolicy=%v restic=%v", p, s.Time, keep[i], want[s.ID])
			}
		}
	}
}

// fakeJobStore is the backup_jobs rows the sweep reads and deletes.
type fakeJobStore struct {
	rows    map[string]*models.BackupJob
	deleted []string
}

func (f *fakeJobStore) Get(_ context.Context, id string) (*models.BackupJob, error) {
	if r, ok := f.rows[id]; ok {
		return r, nil
	}
	return nil, repository.ErrNotFound
}

func (f *fakeJobStore) Delete(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	delete(f.rows, id)
	return nil
}

func keep(n int) *int { return &n }

// End to end on a real repo: the straddle scenario that the old per-stage
// forget breaks (checked on a copy as the control) leaves only complete
// restore points through forgetForSchedule.
func TestForgetForSchedule_RealRestic_NoPartialRestorePoint(t *testing.T) {
	r := newTestRepo(t)
	for _, j := range []struct{ id, home, db, manifest string }{
		{"R1", "2026-01-01 22:00:00", "2026-01-01 22:01:00", "2026-01-01 22:02:00"},
		{"R2", "2026-01-01 23:58:00", "2026-01-02 00:01:00", "2026-01-02 00:02:00"},
		{"R3", "2026-01-02 22:00:00", "2026-01-02 22:01:00", "2026-01-02 22:02:00"},
	} {
		r.backupStage(t, j.id, "home", j.home)
		r.backupStage(t, j.id, "db", j.db)
		r.backupStage(t, j.id, "manifest", j.manifest)
	}

	// Control: the old per-stage policy forget on a copy breaks restore points.
	ctl := testRepo{dir: filepath.Join(t.TempDir(), "repo"), pw: r.pw}
	if out, err := exec.Command("cp", "-a", r.dir, ctl.dir).CombinedOutput(); err != nil {
		t.Fatalf("copy repo: %v: %s", err, out)
	}
	ctl.run(t, "", "forget", "--tag", "schedule-id=s1", "--keep-daily", "2")
	partial := 0
	for _, st := range stagesByJob(ctl.snapshots(t)) {
		if len(st) != 3 {
			partial++
		}
	}
	if partial == 0 {
		t.Fatal("control: the old per-stage forget no longer leaves partial jobs; the scenario does not exercise the bug")
	}

	dest := resticRepo{&models.BackupDestination{ID: "d1", Name: "local", URL: r.dir}, r.pw}
	jobs := &fakeJobStore{rows: map[string]*models.BackupJob{}}
	dID := "d1"
	for _, id := range []string{"R1", "R2", "R3"} {
		jobs.rows[id] = &models.BackupJob{ID: id, Kind: models.BackupJobKindAccountBackup,
			Status: models.BackupJobStatusSucceeded, DestinationID: &dID}
	}
	sched := models.BackupSchedule{ID: "s1", KeepDaily: keep(2)}
	if err := forgetForSchedule(context.Background(), newRetentionTestCmd(), sched, dest, jobs, false); err != nil {
		t.Fatalf("forgetForSchedule: %v", err)
	}

	got := stagesByJob(r.snapshots(t))
	for job, st := range got {
		if len(st) != 3 {
			t.Errorf("job %s left partial: %v", job, st)
		}
	}
	if _, ok := got["R2"]; ok {
		t.Errorf("R2 should be forgotten whole, still has %v", got["R2"])
	}
	if len(got) != 2 {
		t.Errorf("want R1 and R3 kept, got %v", got)
	}
	if !eq(jobs.deleted, []string{"R2"}) {
		t.Errorf("deleted rows = %v, want [R2]", jobs.deleted)
	}
}

// ---- the sweep with a fake restic -------------------------------------------

// fakeRestic answers `snapshots --json` with snaps and records every other call.
func fakeRestic(t *testing.T, snaps []resticSnapshot) *[][]string {
	t.Helper()
	orig := retentionExec
	t.Cleanup(func() { retentionExec = orig })
	var calls [][]string
	body, _ := json.Marshal(snaps)
	retentionExec = func(_ context.Context, _ []string, stdout, _ io.Writer, _ string, args ...string) error {
		calls = append(calls, args)
		if hasArg(args, "snapshots") {
			_, err := stdout.Write(body)
			return err
		}
		return nil
	}
	return &calls
}

func forgetCalls(calls [][]string) [][]string {
	var out [][]string
	for _, c := range calls {
		if hasArg(c, "forget") {
			out = append(out, c)
		}
	}
	return out
}

func threeDays() []resticSnapshot {
	var snaps []resticSnapshot
	snaps = append(snaps, jobSet("J1", "u1", "2026-01-01T01:00:00Z", "2026-01-01T01:01:00Z", "2026-01-01T01:02:00Z")...)
	snaps = append(snaps, jobSet("J2", "u1", "2026-01-02T01:00:00Z", "2026-01-02T01:01:00Z", "2026-01-02T01:02:00Z")...)
	snaps = append(snaps, jobSet("J3", "u1", "2026-01-03T01:00:00Z", "2026-01-03T01:01:00Z", "2026-01-03T01:02:00Z")...)
	return snaps
}

func TestForgetForSchedule_ForgetsDroppedJobsByIDAndDeletesTheirRows(t *testing.T) {
	calls := fakeRestic(t, threeDays())
	d1 := "d1"
	jobs := &fakeJobStore{rows: map[string]*models.BackupJob{
		"J1": {ID: "J1", Kind: models.BackupJobKindAccountBackup, Status: models.BackupJobStatusSucceeded, DestinationID: &d1},
	}}
	sched := models.BackupSchedule{ID: "s1", KeepDaily: keep(1)}
	cmd := newRetentionTestCmd()
	if err := forgetForSchedule(context.Background(), cmd, sched, testDest(), jobs, false); err != nil {
		t.Fatal(err)
	}
	fc := forgetCalls(*calls)
	if len(fc) != 1 {
		t.Fatalf("want one forget call, got %v", fc)
	}
	for _, a := range fc[0] {
		if strings.HasPrefix(a, "--keep-") || a == "--tag" {
			t.Fatalf("forget must name snapshot IDs, not a tag policy: %v", fc[0])
		}
	}
	for _, id := range []string{"J1-home", "J1-db", "J1-manifest", "J2-home", "J2-db", "J2-manifest"} {
		if !hasArg(fc[0], id) {
			t.Errorf("forget is missing %s: %v", id, fc[0])
		}
	}
	if hasArg(fc[0], "J3-manifest") {
		t.Errorf("the newest job must be kept: %v", fc[0])
	}
	if !eq(jobs.deleted, []string{"J1"}) {
		t.Errorf("deleted rows = %v, want [J1] (J2 has no row)", jobs.deleted)
	}
	out := cmd.OutOrStdout().(*bytes.Buffer).String()
	for _, id := range []string{"J1", "J2"} {
		if !strings.Contains(out, "forgot job "+id+" ") {
			t.Errorf("the sweep should log each job it forgot (%s), got:\n%s", id, out)
		}
	}
	if strings.Contains(out, "forgot job J3") {
		t.Errorf("J3 was kept and must not be logged as forgotten:\n%s", out)
	}
}

func TestForgetForSchedule_SparesRunningJobsAndOtherDestinations(t *testing.T) {
	calls := fakeRestic(t, threeDays())
	other := "d-other"
	d1 := "d1"
	jobs := &fakeJobStore{rows: map[string]*models.BackupJob{
		"J1": {ID: "J1", Kind: models.BackupJobKindAccountBackup, Status: models.BackupJobStatusRunning, DestinationID: &d1},
		"J2": {ID: "J2", Kind: models.BackupJobKindAccountBackup, Status: models.BackupJobStatusSucceeded, DestinationID: &other},
	}}
	sched := models.BackupSchedule{ID: "s1", KeepDaily: keep(1)}
	if err := forgetForSchedule(context.Background(), newRetentionTestCmd(), sched, testDest(), jobs, false); err != nil {
		t.Fatal(err)
	}
	fc := forgetCalls(*calls)
	if len(fc) != 1 || hasArg(fc[0], "J1-home") || !hasArg(fc[0], "J2-home") {
		t.Fatalf("want only J2 forgotten (J1 is running): %v", fc)
	}
	if len(jobs.deleted) != 0 {
		t.Errorf("J2's row belongs to another destination and must stay; deleted %v", jobs.deleted)
	}
}

func TestForgetForSchedule_DryRunForgetsNothing(t *testing.T) {
	calls := fakeRestic(t, threeDays())
	jobs := &fakeJobStore{rows: map[string]*models.BackupJob{}}
	cmd := newRetentionTestCmd()
	sched := models.BackupSchedule{ID: "s1", KeepDaily: keep(1)}
	if err := forgetForSchedule(context.Background(), cmd, sched, testDest(), jobs, true); err != nil {
		t.Fatal(err)
	}
	if fc := forgetCalls(*calls); len(fc) != 0 {
		t.Fatalf("dry-run must not forget: %v", fc)
	}
	if out := cmd.OutOrStdout().(*bytes.Buffer).String(); !strings.Contains(out, "[dry-run] would forget job J1") {
		t.Errorf("dry-run should list the jobs it would forget, got:\n%s", out)
	}
}
