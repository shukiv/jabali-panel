package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/limits"
)

// killProcess wraps os.FindProcess + Signal so the limits handlers
// can swap in a fake during tests. Unit tests redirect to a no-op;
// production sends a real signal.
var killProcess = func(pid int, sig os.Signal) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(sig)
}

// user.limits.apply — writes /etc/systemd/system/jabali-user-<u>.slice.d/limits.conf
// with the effective cgroups v2 directives, calls setquota for the disk
// quota, reloads systemd, and puts the values on the user's running slice
// (convergeLiveLimits), checking the kernel took them.
//
// Zero = unlimited for every field — no directive emitted, and setquota
// is called with 0 0 0 0 which clears the quota on the target mount.
// This matches the resolver's invariant so the code path for "clear one
// field" is symmetric to the v1 user.limits.clear command.

type userLimitsApplyParams struct {
	Username        string `json:"username"`
	DiskQuotaMB     uint32 `json:"disk_quota_mb"`
	CPUQuotaPercent uint32 `json:"cpu_quota_percent"`
	MemoryLimitMB   uint32 `json:"memory_limit_mb"`
	IOReadMbps      uint32 `json:"io_read_mbps"`
	IOWriteMbps     uint32 `json:"io_write_mbps"`
	MaxTasks        uint32 `json:"max_tasks"`
	// QuotaMount is the explicit filesystem mount path for setquota.
	// Panel-api resolves it from limits.QuotaMountFor("/home") on startup
	// and passes it on every call — we NEVER use `setquota -a` (ADR-0032
	// §3). Empty string means "skip quota apply" (cgroups-only install).
	QuotaMount string `json:"quota_mount"`
}

type userLimitsApplyResponse struct {
	Username      string `json:"username"`
	DropinPath    string `json:"dropin_path"`
	CgroupApplied bool   `json:"cgroup_applied"`
	QuotaApplied  bool   `json:"quota_applied"`
	// Set when cgroup state doesn't match the rendered directive after
	// daemon-reload — drop-in is on disk but the kernel didn't pick it
	// up. Reconciler logs this and retries on the next pass.
	KernelMismatch string `json:"kernel_mismatch,omitempty"`
	NoChange       bool   `json:"no_change,omitempty"`
}

func userLimitsApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p userLimitsApplyParams
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

	// Defense-in-depth bounds validation — API layer validates, we
	// validate again here so a stray internal call can't slip through.
	effective := limits.EffectiveLimits{
		DiskQuotaMB:     p.DiskQuotaMB,
		CPUQuotaPercent: p.CPUQuotaPercent,
		MemoryLimitMB:   p.MemoryLimitMB,
		IOReadMbps:      p.IOReadMbps,
		IOWriteMbps:     p.IOWriteMbps,
		MaxTasks:        p.MaxTasks,
	}
	if err := effective.Validate(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: err.Error(),
		}
	}

	testMutex.Lock()
	systemdRootFn := systemdRoot
	runCmdFn := runCmd
	testMutex.Unlock()
	root := systemdRootFn()

	dropinDir := filepath.Join(root, fmt.Sprintf("jabali-user-%s.slice.d", p.Username))
	dropinPath := filepath.Join(dropinDir, "limits.conf")
	content := buildLimitsDropinContent(effective)

	// Idempotent: if drop-in content matches, we still call
	// daemon-reload (cheap, ensures verification below runs), but we
	// skip the write.
	existing, _ := os.ReadFile(dropinPath)
	noChange := bytes.Equal(existing, []byte(content))

	if !noChange {
		if err := os.MkdirAll(dropinDir, 0755); err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("mkdir %s: %v", dropinDir, err),
			}
		}
		if content == "" {
			// All zeros → no directives. Remove any existing drop-in to
			// avoid a stale file. (limits.clear covers the standalone
			// case; this catches "admin set everything back to package
			// defaults via an override.")
			if _, err := os.Stat(dropinPath); err == nil {
				if err := os.Remove(dropinPath); err != nil {
					return nil, &agentwire.AgentError{
						Code:    agentwire.CodeInternal,
						Message: fmt.Sprintf("remove stale drop-in: %v", err),
					}
				}
			}
		} else {
			if err := writeFileAtomically(dropinPath, []byte(content), 0644); err != nil {
				return nil, &agentwire.AgentError{
					Code:    agentwire.CodeInternal,
					Message: fmt.Sprintf("write drop-in: %v", err),
				}
			}
		}
	}

	// Disk quota — applied unconditionally, before the no-change early
	// return below. It does NOT depend on the cgroup drop-in content: a
	// raise that touches only disk_quota_mb leaves the drop-in bytes
	// identical (buildLimitsDropinContent renders no disk directive), so
	// gating setquota behind the drop-in diff dropped the new ceiling on
	// the floor (GH #1660). setquota is idempotent and cheap, so we run
	// it every call. Skipping when no mount is supplied lets the same
	// command work on cgroups-only hosts (early ops testing, CI).
	var quotaApplied bool
	if p.QuotaMount != "" {
		// setquota expects block counts in 1KB units; disk_quota_mb is MB,
		// so multiply by 1024. Zero → clears the quota (unlimited).
		blocks := uint64(p.DiskQuotaMB) * 1024
		blocksStr := fmt.Sprintf("%d", blocks)
		if _, stderr, err := runCmdFn(ctx, "setquota",
			"-u", p.Username,
			blocksStr, blocksStr, "0", "0",
			p.QuotaMount,
		); err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("setquota: %v (%s)", err, strings.TrimSpace(string(stderr))),
			}
		}
		quotaApplied = true
	}

	// Unchanged drop-in: no daemon-reload (the reconciler calls this every
	// ~60 s per user), only a read of the running slice's cgroup files. When
	// the kernel differs from the drop-in (a limit that never went live, or a
	// hand edit), convergeLiveLimits puts the drop-in's values on it.
	if noChange {
		resp := &userLimitsApplyResponse{
			Username:      p.Username,
			DropinPath:    dropinPath,
			CgroupApplied: true,
			QuotaApplied:  quotaApplied,
			NoChange:      true,
		}
		if root == "/etc/systemd/system" {
			if mismatch := convergeLiveLimits(ctx, runCmdFn, p.Username, effective, false); mismatch != "" {
				resp.KernelMismatch = mismatch
				resp.CgroupApplied = false
			}
		}
		return resp, nil
	}

	// SIGHUP to PID 1 is systemd's daemon-reload: it loads the changed
	// drop-in, which is what a slice gets when it next starts (a reboot).
	// It does not re-apply limits to a slice that is already running;
	// convergeLiveLimits below does that.
	if err := killProcess(1, syscall.SIGHUP); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("kill -HUP 1: %v", err),
		}
	}
	// systemd processes SIGHUP-induced daemon-reload async. Give
	// it a beat so the kernel-state verification below sees the
	// new drop-in.
	time.Sleep(250 * time.Millisecond)

	resp := &userLimitsApplyResponse{
		Username:      p.Username,
		DropinPath:    dropinPath,
		CgroupApplied: true,
		QuotaApplied:  quotaApplied,
		NoChange:      noChange,
	}

	// Put the new values on the running slice and check the kernel took
	// them (review M10). Skipped against a test JABALI_SYSTEMD_ROOT: the
	// kernel and systemd know nothing of fake slice files.
	if root == "/etc/systemd/system" {
		if mismatch := convergeLiveLimits(ctx, runCmdFn, p.Username, effective, true); mismatch != "" {
			resp.KernelMismatch = mismatch
			resp.CgroupApplied = false
		}
	}

	return resp, nil
}

// limitsCgroupParent is where systemd realizes jabali-user-<u>.slice (a var
// so tests can point it at a temp dir).
var limitsCgroupParent = "/sys/fs/cgroup/jabali.slice/jabali-user.slice"

// liveLimitsProps renders the `systemctl set-property` assignments for every
// directive buildLimitsDropinContent can write, with systemd's reset value for
// a field that is 0 (unlimited), so the runtime values never keep a limit the
// drop-in no longer has. IO comes back separately (see convergeLiveLimits).
func liveLimitsProps(e limits.EffectiveLimits) (props, ioProps []string) {
	cpu, mem, high, tasks := "CPUQuota=", "MemoryMax=infinity", "MemoryHigh=infinity", "TasksMax=infinity"
	if e.CPUQuotaPercent > 0 {
		cpu = fmt.Sprintf("CPUQuota=%d%%", e.CPUQuotaPercent)
	}
	if e.MemoryLimitMB > 0 {
		mem = fmt.Sprintf("MemoryMax=%dM", e.MemoryLimitMB)
		high = fmt.Sprintf("MemoryHigh=%dM", limits.MemoryHighMB(e.MemoryLimitMB))
	}
	if e.MaxTasks > 0 {
		tasks = fmt.Sprintf("TasksMax=%d", e.MaxTasks)
	}
	rd, wr := "IOReadBandwidthMax=", "IOWriteBandwidthMax="
	if e.IOReadMbps > 0 {
		rd = fmt.Sprintf("IOReadBandwidthMax=/ %dM", e.IOReadMbps)
	}
	if e.IOWriteMbps > 0 {
		wr = fmt.Sprintf("IOWriteBandwidthMax=/ %dM", e.IOWriteMbps)
	}
	return []string{cpu, mem, high, tasks}, []string{rd, wr}
}

// convergeLiveLimits makes a running user slice's cgroup match want. A
// daemon-reload loads a changed drop-in but leaves a slice that is already
// running on its old limits until it restarts, which in practice is the next
// reboot. `systemctl set-property --runtime` applies them now; systemd keeps
// those values under /run/systemd/system.control until reboot, when the
// drop-in alone applies again (both carry every property, so they agree).
//
// force runs set-property after a drop-in change; otherwise it runs only when
// the kernel differs from want. Returns "" when the kernel matches or the slice
// is not running (its drop-in applies when it starts), else what still differs.
func convergeLiveLimits(ctx context.Context, run func(context.Context, string, ...string) ([]byte, []byte, error), username string, want limits.EffectiveLimits, force bool) string {
	cgroupDir := filepath.Join(limitsCgroupParent, fmt.Sprintf("jabali-user-%s.slice", username))
	if _, err := os.Stat(cgroupDir); err != nil {
		return ""
	}
	if !force && verifyCgroupState(cgroupDir, want) == "" {
		return ""
	}
	unit := fmt.Sprintf("jabali-user-%s.slice", username)
	props, ioProps := liveLimitsProps(want)
	var notes []string
	if _, stderr, err := run(ctx, "systemctl", append([]string{"set-property", "--runtime", unit}, props...)...); err != nil {
		notes = append(notes, fmt.Sprintf("set-property: %v (%s)", err, strings.TrimSpace(string(stderr))))
	}
	// IO in its own call: where "/" resolves to no block device, the IO
	// assignment must not keep the CPU, memory and task limits from applying.
	if _, stderr, err := run(ctx, "systemctl", append([]string{"set-property", "--runtime", unit}, ioProps...)...); err != nil {
		notes = append(notes, fmt.Sprintf("set-property io: %v (%s)", err, strings.TrimSpace(string(stderr))))
	}
	if mismatch := verifyCgroupState(cgroupDir, want); mismatch != "" {
		notes = append(notes, mismatch)
	}
	return strings.Join(notes, "; ")
}

// buildLimitsDropinContent renders the [Slice] drop-in body. Returns
// empty string when every field is zero (caller deletes any existing
// drop-in rather than writing a no-op file).
func buildLimitsDropinContent(e limits.EffectiveLimits) string {
	var b strings.Builder
	b.WriteString("[Slice]\n")
	hasAny := false
	if e.CPUQuotaPercent > 0 {
		fmt.Fprintf(&b, "CPUQuota=%d%%\n", e.CPUQuotaPercent)
		hasAny = true
	}
	if e.MemoryLimitMB > 0 {
		fmt.Fprintf(&b, "MemoryMax=%dM\n", e.MemoryLimitMB)
		fmt.Fprintf(&b, "MemoryHigh=%dM\n", limits.MemoryHighMB(e.MemoryLimitMB))
		hasAny = true
	}
	if e.IOReadMbps > 0 {
		// systemd expects path + bandwidth. We apply to /, the block-
		// device root — cgroup v2 IO controller aggregates per-device
		// but the "/" path works as a catch-all for hosting workloads.
		fmt.Fprintf(&b, "IOReadBandwidthMax=/ %dM\n", e.IOReadMbps)
		hasAny = true
	}
	if e.IOWriteMbps > 0 {
		fmt.Fprintf(&b, "IOWriteBandwidthMax=/ %dM\n", e.IOWriteMbps)
		hasAny = true
	}
	if e.MaxTasks > 0 {
		fmt.Fprintf(&b, "TasksMax=%d\n", e.MaxTasks)
		hasAny = true
	}
	if !hasAny {
		return ""
	}
	return b.String()
}

// verifyCgroupState compares a running slice's cgroup v2 files with want,
// unlimited fields included ("max"), and returns what differs ("" when all
// match). A file that is missing (its controller is not enabled for the slice)
// is not a mismatch. IO is not compared: io.max is keyed by device numbers.
func verifyCgroupState(cgroupDir string, want limits.EffectiveLimits) string {
	var diffs []string
	check := func(file, wantVal string) {
		data, err := os.ReadFile(filepath.Join(cgroupDir, file))
		if err != nil {
			return
		}
		if got := strings.TrimSpace(string(data)); got != wantVal {
			diffs = append(diffs, fmt.Sprintf("%s=%s want=%s", file, got, wantVal))
		}
	}
	mib := func(mb uint32) string {
		if mb == 0 {
			return "max"
		}
		return fmt.Sprintf("%d", uint64(mb)*1024*1024)
	}
	check("memory.max", mib(want.MemoryLimitMB))
	if want.MemoryLimitMB > 0 {
		check("memory.high", mib(limits.MemoryHighMB(want.MemoryLimitMB)))
	} else {
		check("memory.high", "max")
	}
	if want.MaxTasks > 0 {
		check("pids.max", fmt.Sprintf("%d", want.MaxTasks))
	} else {
		check("pids.max", "max")
	}
	if data, err := os.ReadFile(filepath.Join(cgroupDir, "cpu.max")); err == nil {
		if got := strings.TrimSpace(string(data)); !cpuMaxMatches(got, want.CPUQuotaPercent) {
			diffs = append(diffs, fmt.Sprintf("cpu.max=%q want=%d%%", got, want.CPUQuotaPercent))
		}
	}
	return strings.Join(diffs, " ")
}

// cpuMaxMatches reports whether cpu.max ("<quota> <period>" or "max <period>")
// holds percent of one CPU (0 = no quota). systemd turns CPUQuota=150% into
// "150000 100000"; a difference under 1% is rounding, not a mismatch.
func cpuMaxMatches(cpuMax string, percent uint32) bool {
	f := strings.Fields(cpuMax)
	if len(f) != 2 {
		return true // not a format we know: leave it alone rather than re-apply every pass
	}
	if percent == 0 {
		return f[0] == "max"
	}
	quota, err1 := strconv.ParseInt(f[0], 10, 64)
	period, err2 := strconv.ParseInt(f[1], 10, 64)
	if err1 != nil || err2 != nil || period <= 0 {
		return false
	}
	diff := quota*100 - int64(percent)*period
	if diff < 0 {
		diff = -diff
	}
	return diff < period
}

func init() {
	Default.Register("user.limits.apply", userLimitsApplyHandler)
}
