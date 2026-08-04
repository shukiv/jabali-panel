package migrate

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// memcheck.go — JAB-216.
//
// Serial restores on a 7.7 GB host drove it into repeated global OOM kills over
// four hours and finally into a full userspace wedge: sshd stopped answering,
// nginx stopped serving, and the box needed a provider-console reboot. The
// kernel's last kill was Stalwart (~888 MB anon-rss) ingesting migrated mail.
//
// Nothing in the pipeline looked at memory. It checks disk before extracting —
// which is why a low-disk host fails clean — and then starts a job whose peak
// working set is dominated by mail ingest with no idea whether the host has room
// for it.
//
// This is the memory equivalent of that disk check, and it is deliberately a
// FLOOR rather than an estimate. Predicting a restore's peak is not honest work:
// it depends on mailbox count, message sizes, Stalwart's ingest batching and
// what else the box is serving. What can be said with confidence is that
// starting a heavy, hours-long job on a host that is ALREADY nearly out of
// memory is how the incident began.

const (
	// memRefuseBelowMB is the hard floor. Below this a restore is refused: the
	// host has no room for the job, and starting anyway is how a destination box
	// takes its cut-over tenants down with it.
	memRefuseBelowMB = 512

	// memWarnBelowMB is where the operator is told to expect a slow, pressured
	// run. Not a refusal — a 1 GB box CAN complete a small account, and refusing
	// it would block legitimate work.
	memWarnBelowMB = 1536
)

// AvailableMemMB reports MemAvailable from /proc/meminfo in MB.
//
// MemAvailable rather than MemFree on purpose: MemFree excludes reclaimable page
// cache and would report a healthy host as nearly empty, making the check fire
// constantly on exactly the boxes that are fine.
//
// Returns 0 when it cannot be read, which callers treat as "unknown, do not
// block" — a check that cannot see must not veto a migration.
func AvailableMemMB() int {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	return parseMemAvailableMB(string(b))
}

// parseMemAvailableMB is split out so the parsing is testable without /proc.
func parseMemAvailableMB(meminfo string) int {
	for _, line := range strings.Split(meminfo, "\n") {
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		f := strings.Fields(line)
		// "MemAvailable:   1234567 kB"
		if len(f) < 2 {
			return 0
		}
		kb, err := strconv.Atoi(f[1])
		if err != nil || kb < 0 {
			return 0
		}
		return kb / 1024
	}
	return 0
}

// CheckRestoreMemory refuses a restore when the host is already out of memory,
// and returns a warning string when it is merely tight.
//
// Best-effort: an unreadable /proc/meminfo yields ("", nil) — no warning, no
// refusal. Consistent with the disk check, which also declines to block when it
// cannot measure.
func CheckRestoreMemory() (warning string, err error) {
	return checkRestoreMemoryFor(AvailableMemMB())
}

// checkRestoreMemoryFor is the decision split from the measurement so the
// thresholds are testable without pretending to be a low-memory host.
func checkRestoreMemoryFor(avail int) (warning string, err error) {
	if avail <= 0 {
		return "", nil // cannot measure — do not block
	}
	if avail < memRefuseBelowMB {
		return "", fmt.Errorf(
			"insufficient available memory to start a restore: %d MiB available, need at least %d MiB. "+
				"A restore's peak is dominated by mail ingest, and starting one on a host this "+
				"close to its limit is what OOM-killed Stalwart and wedged a box mid-migration. "+
				"Free memory (or add some) and re-run", avail, memRefuseBelowMB)
	}
	if avail < memWarnBelowMB {
		return fmt.Sprintf(
			"low memory: %d MiB available (comfortable is %d MiB+). The restore will run, but mail "+
				"ingest may be slow and the host will be under pressure — avoid running restores in "+
				"parallel on this box", avail, memWarnBelowMB), nil
	}
	return "", nil
}
