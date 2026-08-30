package commands

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// file_jobs.go — GH #1392. In-memory registry for long-running File Manager
// operations (currently async extract) so the UI can show a progress bar
// instead of a blank wait, and a large extract can't block the HTTP request
// past a proxy timeout.
//
// Ownership: a job is keyed by the requesting username; files.job.status returns
// a job ONLY to a caller passing that same username (the panel sends the caller's
// verified username). Job ids are crypto-random as a second layer. State is
// in-memory: it is lost on an agent restart (a poll then returns job_not_found;
// the UI tells the user to refresh the folder) — acceptable for a progress hint.
//
// Concurrency: the agent serves each connection in its own goroutine
// (server.go), so the job goroutine outlives its start request and status polls
// run concurrently. All job state is guarded by the job mutex.

const (
	fileJobRunning = "running"
	fileJobDone    = "done"
	fileJobError   = "error"

	fileJobTTL     = 10 * time.Minute // reap finished jobs after this
	maxJobsPerUser = 3                // concurrent running jobs per user
)

type fileJob struct {
	id        string
	username  string
	op        string
	startedAt time.Time

	mu     sync.Mutex
	status string
	done   int64
	total  int64 // 0 = unknown (streamed tar) → indeterminate
	result filesExtractResult
	errMsg string
}

func (j *fileJob) setTotal(n int64) { j.mu.Lock(); j.total = n; j.mu.Unlock() }
func (j *fileJob) tick(done int64)  { j.mu.Lock(); j.done = done; j.mu.Unlock() }

func (j *fileJob) finish(res filesExtractResult) {
	j.mu.Lock()
	j.status = fileJobDone
	j.result = res
	j.done = int64(res.Extracted + res.Skipped)
	j.mu.Unlock()
}

func (j *fileJob) fail(msg string) {
	j.mu.Lock()
	j.status = fileJobError
	j.errMsg = msg
	j.mu.Unlock()
}

// fileJobSnapshot is the wire shape returned by files.job.status.
type fileJobSnapshot struct {
	JobID     string             `json:"job_id"`
	Status    string             `json:"status"`
	Done      int64              `json:"done"`
	Total     int64              `json:"total"`
	Result    filesExtractResult `json:"result"`
	Error     string             `json:"error,omitempty"`
	StartedAt string             `json:"started_at"`
}

func (j *fileJob) snapshot() fileJobSnapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	return fileJobSnapshot{
		JobID:     j.id,
		Status:    j.status,
		Done:      j.done,
		Total:     j.total,
		Result:    j.result,
		Error:     j.errMsg,
		StartedAt: j.startedAt.UTC().Format(time.RFC3339),
	}
}

var fileJobs = struct {
	mu sync.Mutex
	m  map[string]*fileJob
}{m: map[string]*fileJob{}}

func randomJobID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// newFileJob registers a running job for username, enforcing the per-user cap.
func newFileJob(username, op string) (*fileJob, error) {
	fileJobs.mu.Lock()
	defer fileJobs.mu.Unlock()
	reapLocked()

	running := 0
	for _, j := range fileJobs.m {
		j.mu.Lock()
		r := j.status == fileJobRunning
		j.mu.Unlock()
		if r && j.username == username {
			running++
		}
	}
	if running >= maxJobsPerUser {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeFailedPrecondition,
			Message: "too many file operations already running; wait for one to finish",
		}
	}

	j := &fileJob{
		id:        randomJobID(),
		username:  username,
		op:        op,
		startedAt: time.Now(),
		status:    fileJobRunning,
	}
	fileJobs.m[j.id] = j
	return j, nil
}

// getFileJob returns the job iff it exists AND belongs to username.
func getFileJob(id, username string) *fileJob {
	fileJobs.mu.Lock()
	defer fileJobs.mu.Unlock()
	reapLocked()
	j := fileJobs.m[id]
	if j == nil || j.username != username {
		return nil
	}
	return j
}

// reapLocked drops finished jobs older than the TTL. Caller holds fileJobs.mu.
func reapLocked() {
	cutoff := time.Now().Add(-fileJobTTL)
	for id, j := range fileJobs.m {
		j.mu.Lock()
		expired := j.status != fileJobRunning && j.startedAt.Before(cutoff)
		j.mu.Unlock()
		if expired {
			delete(fileJobs.m, id)
		}
	}
}
