package commands

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// backup_restore_progress.go — GH #1993. What a running account restore from
// an uploaded file is doing, so the panel can show progress by stage instead
// of a minutes-long spinner: unpacking (bytes of the archive read so far),
// then each stage it applies (home, each database, mail, …).
//
// backup.restore_from_tar records it under its job_id (a ULID the panel
// generates) and backup.restore_progress returns it while the restore runs and
// for a while after. State is in memory: an agent restart loses it, and the
// restore with it.

const (
	restorePhaseUnpacking = "unpacking"
	restorePhaseApplying  = "applying"
	restorePhaseDone      = "done"

	// restoreProgressTTL is how long a finished restore's progress stays.
	restoreProgressTTL = 10 * time.Minute
)

type restoreProgress struct {
	mu         sync.Mutex
	phase      string
	stage      string
	item       string
	index      int // 1-based position of stage among the stages applied
	count      int
	bytesDone  int64
	bytesTotal int64
	finishedAt time.Time
}

// restoreProgressSnapshot is backup.restore_progress's result.
type restoreProgressSnapshot struct {
	JobID      string `json:"job_id"`
	Phase      string `json:"phase"`
	Stage      string `json:"stage,omitempty"`
	Item       string `json:"item,omitempty"`
	Index      int    `json:"index,omitempty"`
	Count      int    `json:"count,omitempty"`
	BytesDone  int64  `json:"bytes_done,omitempty"`
	BytesTotal int64  `json:"bytes_total,omitempty"`
}

var restoreProgressJobs = struct {
	sync.Mutex
	m map[string]*restoreProgress
}{m: map[string]*restoreProgress{}}

// startRestoreProgress records a restore of jobID that has bytesTotal archive
// bytes to unpack, replacing any earlier record of jobID.
func startRestoreProgress(jobID string, bytesTotal int64) *restoreProgress {
	p := &restoreProgress{phase: restorePhaseUnpacking, bytesTotal: bytesTotal}
	now := time.Now()
	restoreProgressJobs.Lock()
	defer restoreProgressJobs.Unlock()
	for id, old := range restoreProgressJobs.m {
		old.mu.Lock()
		stale := !old.finishedAt.IsZero() && now.Sub(old.finishedAt) > restoreProgressTTL
		old.mu.Unlock()
		if stale {
			delete(restoreProgressJobs.m, id)
		}
	}
	restoreProgressJobs.m[jobID] = p
	return p
}

func lookupRestoreProgress(jobID string) *restoreProgress {
	restoreProgressJobs.Lock()
	defer restoreProgressJobs.Unlock()
	return restoreProgressJobs.m[jobID]
}

// The methods are no-ops on a nil *restoreProgress (a restore nothing tracks).

func (p *restoreProgress) addUnpacked(n int64) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.bytesDone += n
	p.mu.Unlock()
}

// applying records that the restore is applying stage (item names the
// database, app or zone when the stage has one), the index-th of count.
func (p *restoreProgress) applying(stage, item string, index, count int) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.phase, p.stage, p.item, p.index, p.count = restorePhaseApplying, stage, item, index, count
	p.mu.Unlock()
}

func (p *restoreProgress) finish() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.phase, p.finishedAt = restorePhaseDone, time.Now()
	p.mu.Unlock()
}

func (p *restoreProgress) snapshot(jobID string) restoreProgressSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return restoreProgressSnapshot{
		JobID: jobID, Phase: p.phase, Stage: p.stage, Item: p.item,
		Index: p.index, Count: p.count, BytesDone: p.bytesDone, BytesTotal: p.bytesTotal,
	}
}

type restoreProgressKey struct{}

func withRestoreProgress(ctx context.Context, p *restoreProgress) context.Context {
	return context.WithValue(ctx, restoreProgressKey{}, p)
}

// restoreProgressFrom is the progress ctx carries, or nil.
func restoreProgressFrom(ctx context.Context) *restoreProgress {
	p, _ := ctx.Value(restoreProgressKey{}).(*restoreProgress)
	return p
}

// countingReader reports every read to onRead.
type countingReader struct {
	r      io.Reader
	onRead func(int64)
}

func (c *countingReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	if n > 0 {
		c.onRead(int64(n))
	}
	return n, err
}

type backupRestoreProgressParams struct {
	JobID string `json:"job_id"`
}

func backupRestoreProgressHandler(_ context.Context, raw json.RawMessage) (any, error) {
	var p backupRestoreProgressParams
	if err := json.Unmarshal(raw, &p); err != nil || !jobIDRE.MatchString(p.JobID) {
		return nil, bkInvalidArg("job_id must be a 26-char ULID")
	}
	rp := lookupRestoreProgress(p.JobID)
	if rp == nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: "no restore with this job_id"}
	}
	return rp.snapshot(p.JobID), nil
}

func init() {
	Default.Register("backup.restore_progress", backupRestoreProgressHandler)
}
