// Package hostreserve is the shared storage-admission primitive for the
// JAB-240..245 family: tenant-influenced data (migration pulls, archive
// extraction, backup staging, docker app trees) lands on host filesystems
// OUTSIDE the tenant's POSIX quota, so every such write path needs three
// things the quota cannot give it:
//
//   - a host low-water reserve (never fill the root filesystem, whatever
//     the source claims about sizes),
//   - a cumulative per-job byte budget (headers, Content-Length and
//     manifests are attacker-controlled — only counted bytes are true),
//   - bounded concurrency (per-tenant and global slots).
//
// One panel process and one agent process each hold their own in-memory
// state; both import this package (single go module, same convention as
// internal/dnsverify). Everything is a hardcoded const on purpose — no
// server_settings knob, no migration, deployable by binary swap alone.
package hostreserve

import (
	"fmt"
	"io"
	"sync"
	"syscall"
)

const (
	// reserveFloorBytes / reserveFloorFraction: the host reserve is
	// min(10% of the filesystem, 5 GiB) — proportional on small VPSes,
	// capped so a 1 TB host doesn't hold 100 GB hostage.
	reserveFloorBytes    = 5 << 30
	reserveFloorFraction = 10

	// reserveCheckInterval: guarded writers re-check the reserve every
	// 256 MiB written, so a job that started with room still stops when
	// concurrent writers eat the disk underneath it.
	reserveCheckInterval = 256 << 20

	// TenantMigrationBudgetBytes caps ONE tenant migration job's total
	// staged bytes (DB dump + files archive + metadata together). Far
	// above any legitimate WordPress site; a source that streams past it
	// is exhausting the host, not migrating (JAB-240).
	TenantMigrationBudgetBytes = 100 << 30
)

// ReserveFloor returns the byte floor kept free on a filesystem of the
// given total size.
func ReserveFloor(totalBytes uint64) uint64 {
	f := totalBytes / reserveFloorFraction
	if f > reserveFloorBytes {
		f = reserveFloorBytes
	}
	return f
}

// CheckReserve refuses when writing need more bytes at path would drop
// the filesystem below the reserve floor. A statfs failure does NOT
// block (a check that cannot see is not entitled to veto — same stance
// as CheckExtractDiskSpace, JAB-41).
func CheckReserve(path string, need int64) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return nil
	}
	free := st.Bavail * uint64(st.Bsize)
	total := st.Blocks * uint64(st.Bsize)
	floor := ReserveFloor(total)
	if need < 0 {
		need = 0
	}
	if free < uint64(need)+floor {
		return fmt.Errorf(
			"host reserve: writing %d MiB at %s would leave under the %d MiB floor (only %d MiB free)",
			need>>20, path, floor>>20, free>>20)
	}
	return nil
}

// Budget is a cumulative byte allowance shared by every writer of one
// job. Safe for concurrent use.
type Budget struct {
	mu        sync.Mutex
	remaining int64
	total     int64
}

// NewBudget returns a budget of n bytes.
func NewBudget(n int64) *Budget { return &Budget{remaining: n, total: n} }

// Remaining reports the unconsumed allowance (never negative).
func (b *Budget) Remaining() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.remaining < 0 {
		return 0
	}
	return b.remaining
}

// Consume debits n bytes; it errors once the budget is exhausted.
func (b *Budget) Consume(n int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.remaining -= n
	if b.remaining < 0 {
		return fmt.Errorf("job byte budget exhausted (%d MiB total)", b.total>>20)
	}
	return nil
}

// guardedWriter enforces an optional Budget and a periodic host-reserve
// check on every write path it wraps.
type guardedWriter struct {
	w          io.Writer
	budget     *Budget // nil = no per-job budget, reserve check only
	path       string  // "" = no reserve checks
	sinceCheck int64
}

func (g *guardedWriter) Write(p []byte) (int, error) {
	if g.budget != nil {
		if err := g.budget.Consume(int64(len(p))); err != nil {
			return 0, err
		}
	}
	if g.path != "" {
		g.sinceCheck += int64(len(p))
		if g.sinceCheck >= reserveCheckInterval {
			g.sinceCheck = 0
			if err := CheckReserve(g.path, 0); err != nil {
				return 0, err
			}
		}
	}
	return g.w.Write(p)
}

// Writer wraps w so writes draw down the budget and periodically
// re-check the host reserve at path. Either guard may be disabled
// (budget nil / path empty).
func (b *Budget) Writer(w io.Writer, path string) io.Writer {
	return &guardedWriter{w: w, budget: b, path: path}
}

// GuardedWriter wraps w with only the periodic host-reserve check —
// for paths where a per-job budget doesn't apply (operator-driven
// pulls) but the host floor still must hold.
func GuardedWriter(w io.Writer, path string) io.Writer {
	return &guardedWriter{w: w, path: path}
}

// KeyedSemaphore bounds concurrent work per key (tenant) and globally.
// In-memory: correct for a single long-lived process (panel-api, agent).
type KeyedSemaphore struct {
	mu     sync.Mutex
	perKey int
	global int
	counts map[string]int
	total  int
}

// NewKeyedSemaphore builds a semaphore with perKey and global slot
// limits. Zero or negative limits mean unlimited for that dimension.
func NewKeyedSemaphore(perKey, global int) *KeyedSemaphore {
	return &KeyedSemaphore{perKey: perKey, global: global, counts: make(map[string]int)}
}

// TryAcquire takes one slot for key. It never blocks: ok=false means the
// caller should reject with a retryable error. Call release exactly once.
func (s *KeyedSemaphore) TryAcquire(key string) (release func(), ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.global > 0 && s.total >= s.global {
		return nil, false
	}
	if s.perKey > 0 && s.counts[key] >= s.perKey {
		return nil, false
	}
	s.counts[key]++
	s.total++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.counts[key]--
			if s.counts[key] <= 0 {
				delete(s.counts, key)
			}
			s.total--
		})
	}, true
}
