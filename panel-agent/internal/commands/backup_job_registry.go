package commands

// backupJobRegistry — GH #2245. backup.create and system.backup run the whole
// backup in a goroutine and return at once, so until now nothing outside that
// goroutine knew whether the job was still working, had failed, or should
// stop:
//   - backup.cancel was a no-op, so Cancel in the panel stopped nothing;
//   - a failure (a destination that is down fails in seconds) only reached the
//     agent's stdout. The panel learns a job ended by finding its manifest
//     snapshot in the repo, which it could not reach either, so the row stayed
//     "running" until the 4h stall sweep.
//
// The registry keeps each job's cancel func and, once the job ends, its
// outcome, so backup.cancel can stop it and backup.status can report a failure
// or a cancel without touching the repo.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	backupJobRunning   = "running"
	backupJobSucceeded = "succeeded"
	backupJobFailed    = "failed"
	backupJobCancelled = "cancelled"
)

// The goroutine limits for each kind. A job still running past its limit is
// stopped, and the panel's stall sweep relies on these bounds.
const (
	accountBackupTimeout = 90 * time.Minute
	systemBackupTimeout  = 6 * time.Hour
)

// backupJobKeep is how long a finished job's outcome stays readable. The
// panel's finalizer polls every 30s, so a day is far more than it needs; the
// bound only stops the map from growing for the life of the agent.
const backupJobKeep = 24 * time.Hour

// agentStartedAt is reported by backup.status. A job the panel marked started
// before this time cannot still be running here: its goroutine died with the
// previous agent process.
var agentStartedAt = time.Now().UTC()

// backupDestinationError marks a failure to reach or open the destination's
// repo, as opposed to a failure inside a stage. The panel counts these toward
// the destination's back-off breaker (JAB-362).
type backupDestinationError struct{ err error }

func (e *backupDestinationError) Error() string { return "ensure repo: " + e.err.Error() }
func (e *backupDestinationError) Unwrap() error { return e.err }

type backupJob struct {
	ctx             context.Context
	cancel          context.CancelFunc
	timeout         time.Duration
	state           string
	err             string
	destinationErr  bool
	cancelRequested bool
	endedAt         time.Time
	// done is closed by finish, once the job's goroutine has let go of
	// everything it held (backup.cancel waits on it).
	done chan struct{}
}

// backupJobOutcome is what lookup returns: a copy, safe to read unlocked.
type backupJobOutcome struct {
	State          string
	Err            string
	DestinationErr bool
}

type backupJobRegistry struct {
	mu   sync.Mutex
	jobs map[string]*backupJob
	now  func() time.Time
}

func newBackupJobRegistry() *backupJobRegistry {
	return &backupJobRegistry{jobs: map[string]*backupJob{}, now: time.Now}
}

var defaultBackupJobs = newBackupJobRegistry()

// start records a running job and returns the context its goroutine must run
// under. Call it before spawning the goroutine, so a cancel that arrives right
// after backup.create returns finds the job.
func (r *backupJobRegistry) start(jobID string, timeout time.Duration) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune()
	r.jobs[jobID] = &backupJob{ctx: ctx, cancel: cancel, timeout: timeout, state: backupJobRunning, done: make(chan struct{})}
	return ctx
}

// finish records how the job's goroutine ended. Call it last, after the
// goroutine has released its job slot: it closes done, and backup.cancel
// answers as soon as done is closed.
func (r *backupJobRegistry) finish(jobID string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[jobID]
	if !ok || j.state != backupJobRunning {
		return
	}
	defer close(j.done)
	j.cancel()
	j.endedAt = r.now()
	switch {
	case err == nil:
		j.state = backupJobSucceeded
	case j.cancelRequested:
		j.state = backupJobCancelled
	default:
		j.state = backupJobFailed
		j.err = err.Error()
		if errors.Is(j.ctx.Err(), context.DeadlineExceeded) {
			j.err = fmt.Sprintf("stopped after the %s limit: %s", j.timeout, j.err)
		}
		var de *backupDestinationError
		j.destinationErr = errors.As(err, &de)
	}
}

// cancel stops a running job and reports whether there was one. The job's
// goroutine still has to unwind (restic gets SIGINT, then a kill after
// CancelGrace); finish records the cancel when it does and closes the
// returned channel.
func (r *backupJobRegistry) cancel(jobID string) (<-chan struct{}, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[jobID]
	if !ok || j.state != backupJobRunning {
		return nil, false
	}
	j.cancelRequested = true
	j.cancel()
	return j.done, true
}

// lookup returns what this agent knows about a job; false when it has no
// record (finished more than backupJobKeep ago, or started by a previous
// agent process).
func (r *backupJobRegistry) lookup(jobID string) (backupJobOutcome, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[jobID]
	if !ok {
		return backupJobOutcome{}, false
	}
	return backupJobOutcome{State: j.state, Err: j.err, DestinationErr: j.destinationErr}, true
}

// prune drops finished jobs older than backupJobKeep. Caller holds r.mu.
func (r *backupJobRegistry) prune() {
	cutoff := r.now().Add(-backupJobKeep)
	for id, j := range r.jobs {
		if j.state != backupJobRunning && j.endedAt.Before(cutoff) {
			delete(r.jobs, id)
		}
	}
}
