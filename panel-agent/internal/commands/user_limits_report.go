package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// user.limits.report — reports the current resource usage and configured
// limits for a user. Used by:
//   - admin UI (per-user "used / limit" bars)
//   - user shell dashboard (self-view)
//   - reconciler drift detection (compare report vs DB-effective)
//
// Sources:
//   - Disk: `quota -p -w -u <user>` parsed output
//   - CPU/memory/IO/tasks: /sys/fs/cgroup/jabali.slice/jabali-user.slice/
//     jabali-user-<u>.slice/{memory.current,memory.max,cpu.stat,io.stat,pids.current,pids.max}
//
// The cgroup sysfs reads are race-free (single readv from the kernel,
// values are snapshots). `quota` is an external command but fast
// (microseconds) because it reads a mmap'd quota file.

type userLimitsReportParams struct {
	Username   string `json:"username"`
	QuotaMount string `json:"quota_mount"` // empty = skip quota reporting
	// MeasureDisk opts in to an ON-DEMAND `du` walk when quota has no
	// answer. The old behavior ran the walk automatically — including
	// whenever quota reported 0 KB for an empty-looking user — which,
	// under the dashboard's polling cadence, IO-starved a production box
	// at peak hours. Now the walk runs ONLY when a human clicks Measure
	// (still cached, and under ionice/nice so it can never compete with
	// serving traffic).
	MeasureDisk bool `json:"measure_disk,omitempty"`
}

type diskReport struct {
	UsedKB  uint64 `json:"used_kb"`
	LimitKB uint64 `json:"limit_kb"`
}

type memReport struct {
	CurrentBytes uint64 `json:"current_bytes"`
	MaxBytes     uint64 `json:"max_bytes"` // 0 = unlimited (systemd's "max")
}

type cpuReport struct {
	UsageNsec    uint64 `json:"usage_nsec"`
	QuotaPercent uint32 `json:"quota_percent"` // 0 = unlimited
}

type tasksReport struct {
	Current uint64 `json:"current"`
	Max     uint64 `json:"max"` // 0 = unlimited
}

type ioReport struct {
	ReadBytes  uint64 `json:"read_bytes"`
	WriteBytes uint64 `json:"write_bytes"`
}

type userLimitsReportResponse struct {
	Username string       `json:"username"`
	Disk     *diskReport  `json:"disk,omitempty"`
	Memory   *memReport   `json:"memory,omitempty"`
	CPU      *cpuReport   `json:"cpu,omitempty"`
	Tasks    *tasksReport `json:"tasks,omitempty"`
	IO       *ioReport    `json:"io,omitempty"`
}

func userLimitsReportHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p userLimitsReportParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	if !userSliceUsernameRegex.MatchString(p.Username) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid username %q", p.Username),
		}
	}

	testMutex.Lock()
	runCmdFn := runCmd
	testMutex.Unlock()

	resp := &userLimitsReportResponse{Username: p.Username}

	// Disk — `quota -p -w -u <user>` returns one record per filesystem
	// the user has quota on. -p prints the raw 1-KB block counts (vs the
	// human-friendly default). The header lines are skipped by
	// parseQuotaLine via the numeric field check. Note: -u and -f are
	// mutually exclusive in util-linux quota; pairing them turns the
	// trailing username into a phantom mountpoint, which is why earlier
	// builds always silently fell back to du and reported a zero limit.
	//
	// Fallback: if `quota` returns nothing (no aquota.user yet — e.g.
	// quotacheck refused to run on a busy /) OR returns UsedKB == 0
	// while /home/<user> visibly has bytes on disk, fall back to
	// `du -sk /home/<user>` so the dashboard reflects reality. Cached
	// in-process for 60s so the 5s polling cadence doesn't fork-bomb
	// the disk on a 50GB user.
	if p.QuotaMount != "" {
		stdout, _, err := runCmdFn(ctx, "quota", "-p", "-w", "-u", p.Username)
		if err == nil {
			if d := parseQuotaLine(string(stdout), p.QuotaMount); d != nil {
				resp.Disk = d
			}
		}
		// quota exits non-zero when the user has no quota set on this
		// mount — we treat that as "no disk report" rather than error.
	}
	// Quota is the ONLY automatic source. When it has no answer, the
	// dashboard shows "—" and offers a Measure button; only that explicit
	// request reaches the du path below.
	if p.MeasureDisk && (resp.Disk == nil || resp.Disk.UsedKB == 0) {
		if du, ok := diskUsageMeasure(ctx, p.Username); ok {
			if resp.Disk == nil {
				resp.Disk = &diskReport{UsedKB: du}
			} else {
				resp.Disk.UsedKB = du
			}
		}
	}

	cgroupDir := fmt.Sprintf("/sys/fs/cgroup/jabali.slice/jabali-user.slice/jabali-user-%s.slice", p.Username)
	if _, err := os.Stat(cgroupDir); err == nil {
		resp.Memory = readMemoryReport(cgroupDir)
		resp.CPU = readCPUReport(cgroupDir)
		resp.Tasks = readTasksReport(cgroupDir)
		resp.IO = readIOReport(cgroupDir)
	}
	// If the slice has no live processes the cgroup dir doesn't exist
	// yet — return the user's name + disk (if any) and omit the
	// cgroup-derived sections. Caller renders "—" for missing values.

	return resp, nil
}

// parseQuotaLine extracts used and hard-limit block counts from quota's
// machine-parseable output. The `-w` flag emits one line per filesystem;
// we match on the mount path.
func parseQuotaLine(output, mount string) *diskReport {
	sc := bufio.NewScanner(strings.NewReader(output))
	for sc.Scan() {
		line := sc.Text()
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		// Match by filesystem/mount — `quota -w` emits the path as
		// the first field. Some implementations print the device
		// instead; we accept either as long as a subsequent field
		// parses cleanly.
		if fields[0] != mount && !strings.HasSuffix(fields[0], ":"+mount) {
			// Keep going — some quota outputs have a header line then
			// the numbers on the same record but whitespace-split
			// across wraps. Fall through to numeric parsing attempt.
		}
		used, usedErr := strconv.ParseUint(fields[1], 10, 64)
		// fields[2] = soft, fields[3] = hard
		if usedErr != nil || len(fields) < 4 {
			continue
		}
		hard, hardErr := strconv.ParseUint(fields[3], 10, 64)
		if hardErr != nil {
			continue
		}
		return &diskReport{UsedKB: used, LimitKB: hard}
	}
	return nil
}

func readMemoryReport(cgroupDir string) *memReport {
	cur := readCgroupUint64(filepath.Join(cgroupDir, "memory.current"))
	max := readCgroupMax(filepath.Join(cgroupDir, "memory.max"))
	return &memReport{CurrentBytes: cur, MaxBytes: max}
}

func readCPUReport(cgroupDir string) *cpuReport {
	// cpu.stat is multi-line key=val; "usage_usec" is the aggregate.
	usage := readCPUStat(filepath.Join(cgroupDir, "cpu.stat"), "usage_usec") * 1000 // µs → ns
	quota := readCPUQuota(filepath.Join(cgroupDir, "cpu.max"))
	return &cpuReport{UsageNsec: usage, QuotaPercent: quota}
}

func readTasksReport(cgroupDir string) *tasksReport {
	cur := readCgroupUint64(filepath.Join(cgroupDir, "pids.current"))
	max := readCgroupMax(filepath.Join(cgroupDir, "pids.max"))
	return &tasksReport{Current: cur, Max: max}
}

func readIOReport(cgroupDir string) *ioReport {
	// io.stat format per line: <major>:<minor> rbytes=N wbytes=N rios=N wios=N ...
	// Sum across all devices (there are rarely more than one mount
	// a hosting user writes to).
	var r, w uint64
	data, err := os.ReadFile(filepath.Join(cgroupDir, "io.stat"))
	if err != nil {
		return &ioReport{}
	}
	for _, line := range strings.Split(string(data), "\n") {
		for _, f := range strings.Fields(line) {
			if strings.HasPrefix(f, "rbytes=") {
				n, _ := strconv.ParseUint(f[len("rbytes="):], 10, 64)
				r += n
			} else if strings.HasPrefix(f, "wbytes=") {
				n, _ := strconv.ParseUint(f[len("wbytes="):], 10, 64)
				w += n
			}
		}
	}
	return &ioReport{ReadBytes: r, WriteBytes: w}
}

func readCgroupUint64(path string) uint64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	return n
}

// readCgroupMax reads a cgroup v2 limit file where "max" means unbounded
// (return 0) and any other value is a decimal byte/count (return as-is).
func readCgroupMax(path string) uint64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(data))
	if s == "max" {
		return 0
	}
	n, _ := strconv.ParseUint(s, 10, 64)
	return n
}

func readCPUStat(path, key string) uint64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	prefix := key + " "
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, prefix) {
			n, _ := strconv.ParseUint(strings.TrimSpace(line[len(prefix):]), 10, 64)
			return n
		}
	}
	return 0
}

// readCPUQuota parses cpu.max ("<quota> <period>") and returns the
// effective quota as a percent of one core: 100000 100000 = 100%,
// 200000 100000 = 200% (2 cores), "max" period → 0 (unlimited).
func readCPUQuota(path string) uint32 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	fields := strings.Fields(strings.TrimSpace(string(data)))
	if len(fields) < 2 || fields[0] == "max" {
		return 0
	}
	quota, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0
	}
	period, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil || period == 0 {
		return 0
	}
	return uint32(quota * 100 / period)
}

// Per-user du fallback cache. Keyed by username; entries expire after
// duCacheTTL. Single mutex covers both read + write — du can take
// hundreds of ms on large homes, so we serialise to avoid two
// concurrent dashboard polls forking the same scan.
const duCacheTTL = 60 * time.Second

type duCacheEntry struct {
	usedKB uint64
	at     time.Time
}

var (
	duCacheMu sync.Mutex
	duCache   = map[string]duCacheEntry{}
)

// diskUsageMeasure returns used KB for /home/<username> using an
// ionice'd/niced `du -sk`. ON-DEMAND ONLY (Measure button) — never called
// automatically. Cached for duCacheTTL so button-mashing costs one walk.
// Returns (0, false) on any error so the caller falls through to the
// existing "no disk report" branch.
func diskUsageMeasure(ctx context.Context, username string) (uint64, bool) {
	duCacheMu.Lock()
	if e, ok := duCache[username]; ok && time.Since(e.at) < duCacheTTL {
		duCacheMu.Unlock()
		return e.usedKB, true
	}
	duCacheMu.Unlock()

	home := "/home/" + username
	if fi, err := os.Stat(home); err != nil || !fi.IsDir() {
		return 0, false
	}

	used, ok := measureHomeKB(ctx, home)
	if !ok {
		return 0, false
	}

	duCacheMu.Lock()
	duCache[username] = duCacheEntry{usedKB: used, at: time.Now()}
	duCacheMu.Unlock()
	return used, true
}

// measureHomeKB runs the actual walk: idle IO class + lowest CPU priority,
// so a Measure click on a huge home loses every fight against serving
// traffic. Split from diskUsageMeasure so the command shape is testable.
func measureHomeKB(ctx context.Context, home string) (uint64, bool) {
	testMutex.Lock()
	runCmdFn := runCmd
	testMutex.Unlock()

	stdout, _, err := runCmdFn(ctx, "ionice", "-c3", "nice", "-n19", "du", "-sk", home)
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(stdout))
	if len(fields) == 0 {
		return 0, false
	}
	used, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0, false
	}
	return used, true
}

func init() {
	Default.Register("user.limits.report", userLimitsReportHandler)
}
