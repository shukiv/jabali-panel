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
	"slices"
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

// A job without a manifest that still holds data counts toward the keep
// rules like a complete one; within one day the complete one is preferred. A
// job without a manifest and without data goes once a newer complete backup
// exists in its series.
func TestPlanJobRetention_IncompleteJobs(t *testing.T) {
	snaps := jobSet("OK", "u1", "2026-01-02T01:00:00Z", "2026-01-02T01:01:00Z", "2026-01-02T01:02:00Z")
	snaps = append(snaps,
		snap("old-home", "OLD", "u1", "home", "2026-01-01T01:00:00Z"),    // failed on day 1: day 1's only backup
		snap("late-home", "LATE", "u1", "home", "2026-01-02T09:00:00Z"),  // failed later on OK's day
		snap("new-home", "NEW", "u1", "home", "2026-01-03T01:00:00Z"),    // newest: maybe running
		snap("old-dns", "NODATA", "u1", "dns", "2026-01-01T02:00:00Z"),   // no data, older than OK
		snap("new-dns", "NODATA2", "u1", "dns", "2026-01-03T02:00:00Z"),  // no data, newer than OK
		snap("lone-home", "LONE", "u2", "home", "2026-01-01T01:00:00Z"),  // u2 has no complete backup
		snap("lone-dns", "LONEDNS", "u2", "dns", "2026-01-01T02:00:00Z"), // nor a newer one to judge this by
	)
	plan := planJobRetention(snaps, retentionPolicy{Daily: 7})
	if got := jobIDs(plan.Keep); !eq(got, []string{"LONE", "LONEDNS", "NEW", "NODATA2", "OK", "OLD"}) {
		t.Fatalf("keep = %v", got)
	}
	if got := jobIDs(plan.Drop); !eq(got, []string{"LATE", "NODATA"}) {
		t.Fatalf("drop = %v, want LATE (OK is the complete backup of its day) and NODATA", got)
	}
}

// Within one day, week or month a rule keeps a complete backup over a newer
// incomplete one, and of incomplete ones the one holding the most kinds of
// data over a newer one holding less.
func TestPlanJobRetention_PrefersCompleteThenMostData(t *testing.T) {
	snaps := []resticSnapshot{
		// day 1: home+db+mail, then a newer mail-only job
		snap("a-home", "A", "u1", "home", "2026-01-01T01:00:00Z"),
		snap("a-db", "A", "u1", "db", "2026-01-01T01:01:00Z"),
		snap("a-mail", "A", "u1", "mail", "2026-01-01T01:02:00Z"),
		snap("b-mail", "B", "u1", "mail", "2026-01-01T05:00:00Z"),
	}
	// day 2: complete, then a newer home+db+mail job without its manifest
	snaps = append(snaps, jobSet("C", "u1", "2026-01-02T01:00:00Z", "2026-01-02T01:01:00Z", "2026-01-02T01:02:00Z")...)
	snaps = append(snaps,
		snap("d-home", "D", "u1", "home", "2026-01-02T05:00:00Z"),
		snap("d-db", "D", "u1", "db", "2026-01-02T05:01:00Z"),
		snap("d-mail", "D", "u1", "mail", "2026-01-02T05:02:00Z"),
	)
	plan := planJobRetention(snaps, retentionPolicy{Daily: 2})
	if got := jobIDs(plan.Keep); !eq(got, []string{"A", "C"}) {
		t.Fatalf("keep = %v, want [A C]", got)
	}
	if got := jobIDs(plan.Drop); !eq(got, []string{"B", "D"}) {
		t.Fatalf("drop = %v, want [B D]", got)
	}
}

// oldSweep forgets what the sweep before whole-job retention forgot: restic's
// keep rules applied per (host, path) group. The manifest's path is the same
// for every account, so manifests are kept host-wide; home and db paths are
// one account's, so each account keeps its full history of those; the mail
// stage's path is one job's, so mail is never forgotten.
func oldSweep(snaps []resticSnapshot, p retentionPolicy) []resticSnapshot {
	groups := map[string][]int{}
	for i, s := range snaps {
		var path string
		switch stage := tagValue(s.Tags, "stage"); stage {
		case "manifest":
			path = "/manifest.json"
		case "mail":
			path = "/mail/" + tagValue(s.Tags, "job-id")
		default:
			path = "/" + stage + "/" + tagValue(s.Tags, "user-id")
		}
		groups[s.Hostname+"|"+path] = append(groups[s.Hostname+"|"+path], i)
	}
	kept := map[int]bool{}
	for _, idx := range groups {
		sort.SliceStable(idx, func(a, b int) bool { return snaps[idx[a]].Time.After(snaps[idx[b]].Time) })
		times := make([]time.Time, len(idx))
		for i, j := range idx {
			times[i] = snaps[j].Time
		}
		for i, k := range keepByPolicy(times, p) {
			if k {
				kept[idx[i]] = true
			}
		}
	}
	var out []resticSnapshot
	for i, s := range snaps {
		if kept[i] {
			out = append(out, s)
		}
	}
	return out
}

// What the fleet's repositories look like after months of the old sweep: a
// few manifests per host, each account's policy history of home and db
// snapshots without them, and the mail of every job. Retention must keep that
// history and forget only the mail-only leftovers beyond the policy.
func TestPlanJobRetention_KeepsHistoryTheOldSweepLeftWithoutManifests(t *testing.T) {
	p := retentionPolicy{Daily: 7, Weekly: 4, Monthly: 12}
	var snaps []resticSnapshot
	start := time.Date(2025, 11, 1, 0, 0, 0, 0, time.UTC)
	for d := 0; d < 150; d++ {
		day := start.AddDate(0, 0, d)
		for i, user := range []string{"alice", "bob", "carol"} {
			at := day.Add(time.Duration(1+i) * time.Hour)
			job := fmt.Sprintf("%s-%03d", user, d)
			for k, stage := range []string{"home", "db", "mail", "manifest"} {
				s := snap(job+"-"+stage, job, user, stage, at.Add(time.Duration(k)*time.Minute).Format(time.RFC3339))
				snaps = append(snaps, s)
			}
		}
	}
	left := oldSweep(snaps, p)

	withHome, manifests, mailOnly := map[string]bool{}, 0, map[string]bool{}
	for j, stages := range stagesByJob(left) {
		switch {
		case slices.Contains(stages, "manifest"):
			manifests++
			withHome[j] = true
		case slices.Contains(stages, "home"):
			withHome[j] = true
		case eq(stages, []string{"mail"}):
			mailOnly[j] = true
		}
	}
	if manifests > 20 || len(withHome) < 3*12 || len(mailOnly) < 3*130 {
		t.Fatalf("the scenario does not look like the fleet: %d manifests, %d jobs with home", manifests, len(withHome))
	}

	plan := planJobRetention(left, p)
	// alice's first backup kept its manifest: it is the host's oldest one,
	// which the old sweep kept. That complete backup is now alice's November,
	// preferred over her newer job of 30 November that holds home and db
	// without a manifest. Every other job with a home folder is kept.
	preferred := map[string]bool{"alice-029": true}
	kept := map[string]bool{}
	for _, j := range plan.Keep {
		kept[j.JobID] = true
	}
	if !kept["alice-000"] {
		t.Error("alice-000 is complete and the oldest backup: it must be kept")
	}
	for j := range withHome {
		if !kept[j] && !preferred[j] {
			t.Errorf("job %s still has its home folder and database: it is policy history and must be kept", j)
		}
	}
	for _, j := range plan.Drop {
		if !mailOnly[j.JobID] && !preferred[j.JobID] {
			t.Errorf("dropped %s, which is not a mail-only leftover", j.JobID)
		}
	}
	// Each account's history is its jobs with a home folder, so the policy has
	// no room left for a mail-only job: every one of them goes.
	if len(plan.Drop) != len(mailOnly)+len(preferred) || len(plan.Keep) != len(withHome)-len(preferred) {
		t.Errorf("dropped %d of %d mail-only jobs, kept %d of %d jobs with a home folder",
			len(plan.Drop), len(mailOnly), len(plan.Keep), len(withHome))
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
	// listedBefore records each finished-rows query's cutoff, by its first
	// status.
	listedBefore map[string]time.Time
}

// ListFinishedBackupsForDestination mirrors the repository query: account/
// system backups on destID that ended with one of statuses before `before`.
func (f *fakeJobStore) ListFinishedBackupsForDestination(_ context.Context, destID string, statuses []string, before time.Time) ([]models.BackupJob, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	if f.listedBefore == nil {
		f.listedBefore = map[string]time.Time{}
	}
	f.listedBefore[statuses[0]] = before
	var out []models.BackupJob
	for _, r := range f.rows {
		if r.DestinationID == nil || *r.DestinationID != destID {
			continue
		}
		if r.Kind != models.BackupJobKindAccountBackup && r.Kind != models.BackupJobKindSystemBackup {
			continue
		}
		if !slices.Contains(statuses, r.Status) {
			continue
		}
		t := r.CreatedAt
		if r.FinishedAt != nil {
			t = *r.FinishedAt
		}
		if !t.Before(before) {
			continue
		}
		out = append(out, *r)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out, nil
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
