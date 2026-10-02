package main

import (
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
// daily/weekly/monthly rules are applied to each complete job's manifest time,
// and every snapshot of a dropped job is forgotten by ID. A job is kept or
// dropped as one unit, so a restore point is never partial.

// resticSnapshot is one row of `restic snapshots --json`.
type resticSnapshot struct {
	ID       string    `json:"id"`
	Time     time.Time `json:"time"`
	Hostname string    `json:"hostname"`
	Tags     []string  `json:"tags"`
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
//   - Complete jobs are kept by restic's rules (keepByPolicy) on their
//     manifest times, per series.
//   - An incomplete job (no manifest: failed, or still running) is dropped only
//     when a complete job in its series is newer; otherwise it is kept. The
//     caller still checks the job's row and keeps any job that is queued or
//     running.
//   - A series with no complete job is kept whole.
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
		if tagValue(s.Tags, internalbackup.TagKeyStage) == internalbackup.StageManifest {
			a.job.Complete = true
			a.job.Time = s.Time
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
		var complete, incomplete []retentionJob
		for _, j := range bySeries[series] {
			if j.Complete {
				complete = append(complete, j)
			} else {
				incomplete = append(incomplete, j)
			}
		}
		if len(complete) == 0 {
			plan.Keep = append(plan.Keep, incomplete...)
			continue
		}
		sortNewestFirst(complete)
		keep := keepByPolicy(jobTimes(complete), p)
		for i, j := range complete {
			if keep[i] {
				plan.Keep = append(plan.Keep, j)
			} else {
				plan.Drop = append(plan.Drop, j)
			}
		}
		newestComplete := complete[0].Time
		for _, j := range incomplete {
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
	type rule struct {
		count  int
		bucket func(time.Time) int
		last   int
	}
	rules := []*rule{
		{count: p.Daily, bucket: func(t time.Time) int { return t.Year()*10000 + int(t.Month())*100 + t.Day() }, last: -1},
		{count: p.Weekly, bucket: func(t time.Time) int { y, w := t.ISOWeek(); return y*100 + w }, last: -1},
		{count: p.Monthly, bucket: func(t time.Time) int { return t.Year()*100 + int(t.Month()) }, last: -1},
	}
	keep := make([]bool, len(times))
	for i, t := range times {
		for _, r := range rules {
			if r.count <= 0 {
				continue
			}
			v := r.bucket(t)
			if v != r.last || i == len(times)-1 {
				keep[i] = true
				r.last = v
				r.count--
			}
		}
	}
	return keep
}
