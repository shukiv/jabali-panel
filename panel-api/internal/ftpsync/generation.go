// Package ftpsync holds coordination state shared between the FTP-account API
// handlers and the FTP reconciler — the two places that dispatch the full
// ftpaccount.sshd_sync snapshot to the agent.
package ftpsync

import (
	"sync/atomic"
	"time"
)

// lastGen is the process-wide high-water mark for sshd_sync generations.
var lastGen int64

// NextGeneration returns a strictly increasing generation stamp for an
// ftpaccount.sshd_sync dispatch (JAB-267). The agent applies syncs in
// generation order and drops any older than the last it applied, so a delayed
// stale snapshot cannot resurrect a revoked Match block.
//
// It is seeded from the wall clock (so a fresh process starts above any value a
// previous one plausibly emitted) but is NOT a bare time.Now(): UnixNano drops
// the monotonic component, so an NTP step backwards would make every subsequent
// stamp — including revocations — fall below the agent's high-water and get
// dropped, silently reopening the race for the skew window. The max(prev+1, now)
// CAS guarantees the sequence only ever increases regardless of clock motion.
//
// BOTH dispatch sites (the API's syncHostAccess and the reconciler) MUST call
// this same function; two independent counters would re-break cross-site
// ordering during a skew window.
func NextGeneration() int64 {
	for {
		prev := atomic.LoadInt64(&lastGen)
		next := time.Now().UnixNano()
		if next <= prev {
			next = prev + 1
		}
		if atomic.CompareAndSwapInt64(&lastGen, prev, next) {
			return next
		}
	}
}
