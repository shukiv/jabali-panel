package main

import (
	"slices"
	"sort"
	"strings"
	"time"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// Whole-job retention for `jabali backup retention apply`.
//
// One backup is a SET of restic snapshots: one per stage (home, db, mail, …
// for an account; panel_db, tls, os_users, … for the system) plus a manifest
// snapshot that ties them together, all tagged job-id=<id>. The sweep used to
// run `restic forget --tag schedule-id=<id> --keep-daily/weekly/monthly`,
// which applies the keep policy to each stage's snapshots on their own
// (restic groups by host+paths). A run whose stages fall into different
// buckets (one that straddles midnight, a week or a month boundary), or a
// failed run that wrote only some stages, then leaves restore points with a
// manifest whose stage snapshots are gone: silently unrestorable. Reproduced
// with restic 0.18.
//
// So the keep decision is made per whole job: jobs are grouped into series
// (one per host and account, or per host for the system backup), restic's own
// daily/weekly/monthly rules are applied to each job's time, and every
// snapshot of a dropped job is forgotten by ID. A job is kept or dropped as
// one unit, so a restore point is never partial.
//
// The rules count every job that still holds account data, with or without
// its manifest. Those per-stage sweeps grouped by host and path, and the
// manifest's path is the same for every account, so a host kept about one
// policy's worth of manifests in total while each account's home folder and
// database snapshots kept the account's full history. Found on the fleet:
// over a thousand such backups per host, holding the only copy of that
// history. The panel cannot restore a backup without its manifest, but its
// data can still be read back with restic, so retention keeps that history
// rather than deleting it as leftovers. Within one day, week or month a
// complete backup is preferred.

// resticSnapshot is one row of `restic snapshots --json`.
type resticSnapshot struct {
	ID       string    `json:"id"`
	Time     time.Time `json:"time"`
	Hostname string    `json:"hostname"`
	Tags     []string  `json:"tags"`
	Paths    []string  `json:"paths"`
}

// retentionPolicy is a schedule's keep_daily/weekly/monthly; 0 = rule unset.
type retentionPolicy struct {
	Daily, Weekly, Monthly int
}

func (p retentionPolicy) empty() bool { return p.Daily <= 0 && p.Weekly <= 0 && p.Monthly <= 0 }

// retentionJob is every snapshot of one backup job in one series.
type retentionJob struct {
	JobID       string
	Series      string
	Complete    bool      // has its manifest snapshot
	Time        time.Time // the manifest's time; for an incomplete job, its newest snapshot's
	SnapshotIDs []string
	Data        []string // the data stages it still has (dataStages), sorted
}

// dataStages are the account backup stages that hold the account's own data.
// A job without its manifest that still has one of them is counted by the
// keep rules; one with none of them is a leftover.
var dataStages = map[string]bool{
	internalbackup.StageHome:   true,
	internalbackup.StageDB:     true,
	internalbackup.StageMail:   true,
	internalbackup.StageDocker: true,
}

// rank orders the jobs of one day, week or month for the keep rules: a
// complete backup first, then the incomplete job holding the most kinds of
// data. Equal ranks go to the newest, as in restic. A complete job is taken to
// have every stage its manifest lists; `jabali backup retention verify`
// checks that.
func (j retentionJob) rank() int {
	if j.Complete {
		return len(dataStages) + 1
	}
	return len(j.Data)
}

// summary describes the job for the sweep's output.
func (j retentionJob) summary() string {
	switch {
	case j.Complete:
		return "complete"
	case len(j.Data) == 0:
		return "no manifest, no data"
	default:
		return "no manifest, holds " + strings.Join(j.Data, "+")
	}
}

// jobRetentionPlan is what the sweep does with one (schedule, destination).
type jobRetentionPlan struct {
	Keep []retentionJob
	Drop []retentionJob
	// Untagged counts snapshots with no job-id tag, and Ambiguous lists job ids
	// whose snapshots disagree on their series. Both are left alone.
	Untagged  int
	Ambiguous []string
}

func tagValue(tags []string, key string) string {
	prefix := key + "="
	for _, t := range tags {
		if strings.HasPrefix(t, prefix) {
			return strings.TrimPrefix(t, prefix)
		}
	}
	return ""
}

// seriesOf names the restore-point series a snapshot belongs to: an account's
// backups on one host, or one host's system backups. Policy counts run per
// series, so one account's backups never decide another account's.
func seriesOf(s resticSnapshot) string {
	kind := tagValue(s.Tags, internalbackup.TagKeyKind)
	owner := tagValue(s.Tags, internalbackup.TagKeyUserID)
	if kind == internalbackup.KindSystemBackup {
		owner = tagValue(s.Tags, internalbackup.TagKeySystem)
	}
	return s.Hostname + "|" + kind + "|" + owner
}

// planJobRetention decides, per whole job, what a schedule's keep policy keeps.
//
//   - Complete jobs, and incomplete jobs (no manifest) that still hold data
//     (dataStages), are kept by restic's rules on their times, per series.
//     Within one day, week or month a rule keeps the job of highest rank: a
//     complete one before an incomplete one.
//   - An incomplete job with no data is dropped only when a complete job in
//     its series is newer; otherwise it is kept.
//   - The caller still checks each dropped job's row and keeps any job that is
//     queued or running.
//   - Snapshots with no job-id, and jobs spread over more than one series,
//     are never dropped.
func planJobRetention(snaps []resticSnapshot, p retentionPolicy) jobRetentionPlan {
	var plan jobRetentionPlan
	if p.empty() {
		return plan
	}
	type acc struct {
		job       retentionJob
		ambiguous bool
		newest    time.Time
	}
	jobs := map[string]*acc{}
	var order []string
	for _, s := range snaps {
		jobID := tagValue(s.Tags, internalbackup.TagKeyJobID)
		if jobID == "" {
			plan.Untagged++
			continue
		}
		series := seriesOf(s)
		a, ok := jobs[jobID]
		if !ok {
			a = &acc{job: retentionJob{JobID: jobID, Series: series}}
			jobs[jobID] = a
			order = append(order, jobID)
		} else if a.job.Series != series {
			a.ambiguous = true
		}
		a.job.SnapshotIDs = append(a.job.SnapshotIDs, s.ID)
		if s.Time.After(a.newest) {
			a.newest = s.Time
		}
		switch stage := tagValue(s.Tags, internalbackup.TagKeyStage); {
		case stage == internalbackup.StageManifest:
			a.job.Complete = true
			a.job.Time = s.Time
		case dataStages[stage] && !slices.Contains(a.job.Data, stage):
			a.job.Data = append(a.job.Data, stage)
			slices.Sort(a.job.Data)
		}
	}

	bySeries := map[string][]retentionJob{}
	var seriesOrder []string
	for _, id := range order {
		a := jobs[id]
		if a.ambiguous {
			plan.Ambiguous = append(plan.Ambiguous, id)
			plan.Keep = append(plan.Keep, a.job)
			continue
		}
		if !a.job.Complete {
			a.job.Time = a.newest
		}
		if _, ok := bySeries[a.job.Series]; !ok {
			seriesOrder = append(seriesOrder, a.job.Series)
		}
		bySeries[a.job.Series] = append(bySeries[a.job.Series], a.job)
	}

	for _, series := range seriesOrder {
		var counted, leftovers []retentionJob
		var newestComplete time.Time
		for _, j := range bySeries[series] {
			if j.Complete || len(j.Data) > 0 {
				counted = append(counted, j)
			} else {
				leftovers = append(leftovers, j)
			}
			if j.Complete && j.Time.After(newestComplete) {
				newestComplete = j.Time
			}
		}
		sortNewestFirst(counted)
		ranks := make([]int, len(counted))
		for i, j := range counted {
			ranks[i] = j.rank()
		}
		keep := keepByPolicyRanked(jobTimes(counted), ranks, p)
		for i, j := range counted {
			if keep[i] {
				plan.Keep = append(plan.Keep, j)
			} else {
				plan.Drop = append(plan.Drop, j)
			}
		}
		for _, j := range leftovers {
			if j.Time.Before(newestComplete) {
				plan.Drop = append(plan.Drop, j)
			} else {
				plan.Keep = append(plan.Keep, j)
			}
		}
	}
	return plan
}

func sortNewestFirst(js []retentionJob) {
	sort.SliceStable(js, func(a, b int) bool {
		if js[a].Time.Equal(js[b].Time) {
			return js[a].JobID > js[b].JobID
		}
		return js[a].Time.After(js[b].Time)
	})
}

func jobTimes(js []retentionJob) []time.Time {
	out := make([]time.Time, len(js))
	for i, j := range js {
		out[i] = j.Time
	}
	return out
}

// keepByPolicy applies restic's --keep-daily/weekly/monthly rules to times
// sorted newest first, the way restic's ApplyPolicy does: for each rule with
// counts left, a time in a bucket other than the rule's last kept bucket is
// kept and uses one count. When a rule still has counts left at the oldest
// time, that oldest one is kept too ("oldest daily snapshot" in restic's
// output), which keeps the longest history the policy allows. Buckets are
// read in each time's own zone, as restic does.
func keepByPolicy(times []time.Time, p retentionPolicy) []bool {
	return keepByPolicyRanked(times, make([]int, len(times)), p)
}

// keepByPolicyRanked is keepByPolicy where each rule keeps, of the times in
// one bucket, the one of highest rank (the newest of those) instead of the
// newest. With equal ranks it keeps exactly what restic keeps.
func keepByPolicyRanked(times []time.Time, rank []int, p retentionPolicy) []bool {
	rules := []struct {
		count  int
		bucket func(time.Time) int
	}{
		{p.Daily, func(t time.Time) int { return t.Year()*10000 + int(t.Month())*100 + t.Day() }},
		{p.Weekly, func(t time.Time) int { y, w := t.ISOWeek(); return y*100 + w }},
		{p.Monthly, func(t time.Time) int { return t.Year()*100 + int(t.Month()) }},
	}
	keep := make([]bool, len(times))
	for _, r := range rules {
		count := r.count
		if count <= 0 || len(times) == 0 {
			continue
		}
		// A bucket is a run of consecutive times with the same value. restic
		// keeps the first time of each run, as long as the rule has counts.
		for start := 0; start < len(times) && count > 0; {
			v := r.bucket(times[start])
			best, end := start, start+1
			for ; end < len(times) && r.bucket(times[end]) == v; end++ {
				if rank[end] > rank[best] {
					best = end
				}
			}
			keep[best] = true
			count--
			start = end
		}
		if count > 0 {
			keep[len(times)-1] = true
		}
	}
	return keep
}
