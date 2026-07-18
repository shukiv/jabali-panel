// joblog.go — append-only per-job log file.
//
// The backup orchestrator runs in-process inside panel-agent (no
// transient systemd unit, no journal). Without a log target the
// admin "Log" modal would always be empty. JobLogger writes timestamped
// lines to /var/lib/jabali-backups/logs/<job_id>.log so the
// backup.logs agent handler can stream the file back. Mode 0640
// root:jabali because /var/lib/jabali-backups is jabali-traversable
// already and the panel-api process needs to be able to read these.
package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// JobLogDir is the on-disk directory holding per-job log files.
const JobLogDir = "/var/lib/jabali-backups/logs"

// JobLogger is a tiny append-only logger keyed by job ID. Safe for
// concurrent use from multiple stage goroutines.
type JobLogger struct {
	mu sync.Mutex
	f  *os.File
}

// NewJobLogger opens /var/lib/jabali-backups/logs/<jobID>.log in
// append mode, creating the directory if needed. A nil logger is
// returned on filesystem errors so callers can call Printf without a
// nil check (Printf is a no-op on a nil receiver).
func NewJobLogger(jobID string) *JobLogger {
	if err := os.MkdirAll(JobLogDir, 0o755); err != nil {
		return nil
	}
	path := filepath.Join(JobLogDir, jobID+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return nil
	}
	return &JobLogger{f: f}
}

// Printf writes one timestamped line. Errors are swallowed — a
// failing log writer must not abort a backup.
func (l *JobLogger) Printf(format string, args ...any) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	line := fmt.Sprintf("%s "+format+"\n",
		append([]any{time.Now().UTC().Format(time.RFC3339)}, args...)...)
	_, _ = l.f.WriteString(line)
}

// Close releases the file handle. Safe to call on a nil logger.
func (l *JobLogger) Close() {
	if l == nil || l.f == nil {
		return
	}
	_ = l.f.Close()
}

// JobLogPath returns the on-disk path the agent's backup.logs handler
// reads. Exposed so the handler can resolve the same location without
// duplicating the constant.
func JobLogPath(jobID string) string {
	return filepath.Join(JobLogDir, jobID+".log")
}

// DefaultJobLogRetention is how long a per-job backup log file is kept after
// its last write. A running job's log has a fresh mtime, so an mtime-based
// prune at this age never removes an active job's log. 90 days matches the
// fixed-window policy in JAB-98.
const DefaultJobLogRetention = 90 * 24 * time.Hour

// PruneJobLogs removes per-job log files under JobLogDir whose last
// modification is older than maxAge, returning the count removed. A missing
// dir is not an error (no backups have run yet). Age-based, so an in-flight
// job — still appending to its log — is never pruned. (JAB-98)
func PruneJobLogs(maxAge time.Duration) (int, error) {
	return pruneJobLogsIn(JobLogDir, maxAge)
}

func pruneJobLogsIn(dir string, maxAge time.Duration) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".log" {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil || info.ModTime().After(cutoff) {
			continue
		}
		if os.Remove(filepath.Join(dir, e.Name())) == nil {
			removed++
		}
	}
	return removed, nil
}
