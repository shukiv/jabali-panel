package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/limits"
)

// fakeSlice is a running user slice's cgroup dir under a temp limitsCgroupParent.
func fakeSlice(t *testing.T, user string, files map[string]string) string {
	t.Helper()
	parent := t.TempDir()
	orig := limitsCgroupParent
	limitsCgroupParent = parent
	t.Cleanup(func() { limitsCgroupParent = orig })
	dir := filepath.Join(parent, "jabali-user-"+user+".slice")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCgroup(t, dir, files)
	return dir
}

func writeCgroup(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, v := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

var unlimitedCgroup = map[string]string{"cpu.max": "max 100000", "memory.max": "max", "memory.high": "max", "pids.max": "max"}

var limited = limits.EffectiveLimits{CPUQuotaPercent: 150, MemoryLimitMB: 1024, MaxTasks: 300, IOReadMbps: 20}

// limitedCgroup is what the kernel shows once `limited` is applied.
func limitedCgroup() map[string]string {
	return map[string]string{
		"cpu.max":     "150000 100000",
		"memory.max":  "1073741824",
		"memory.high": fmt.Sprint(uint64(limits.MemoryHighMB(1024)) * 1024 * 1024),
		"pids.max":    "300",
	}
}

type recordedCalls [][]string

// runner records each command; fail makes one fail, apply plays systemd.
func (r *recordedCalls) runner(fail func(args []string) error, apply func(args []string)) func(context.Context, string, ...string) ([]byte, []byte, error) {
	return func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
		*r = append(*r, append([]string{name}, args...))
		if fail != nil {
			if err := fail(args); err != nil {
				return nil, []byte("boom"), err
			}
		}
		if apply != nil {
			apply(args)
		}
		return nil, nil, nil
	}
}

// The bug: a limit written to the drop-in never reached a slice that was
// already running. A running slice still on "max" must get set-property with
// every value, and the call reports nothing once the kernel holds them.
func TestConvergeLiveLimits_PutsLimitsOnRunningSlice(t *testing.T) {
	dir := fakeSlice(t, "alice", unlimitedCgroup)
	var calls recordedCalls
	run := calls.runner(nil, func(args []string) {
		if args[3] == "CPUQuota=150%" {
			writeCgroup(t, dir, limitedCgroup())
		}
	})
	if got := convergeLiveLimits(context.Background(), run, "alice", limited, false); got != "" {
		t.Fatalf("mismatch after converge: %q", got)
	}
	want := recordedCalls{
		{"systemctl", "set-property", "--runtime", "jabali-user-alice.slice", "CPUQuota=150%", "MemoryMax=1024M", fmt.Sprintf("MemoryHigh=%dM", limits.MemoryHighMB(1024)), "TasksMax=300"},
		{"systemctl", "set-property", "--runtime", "jabali-user-alice.slice", "IOReadBandwidthMax=/ 20M", "IOWriteBandwidthMax="},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls:\n got %q\nwant %q", calls, want)
	}
}

func TestConvergeLiveLimits_MatchingKernelIsLeftAlone(t *testing.T) {
	fakeSlice(t, "alice", limitedCgroup())
	var calls recordedCalls
	if got := convergeLiveLimits(context.Background(), calls.runner(nil, nil), "alice", limited, false); got != "" || len(calls) != 0 {
		t.Fatalf("got %q with %d calls, want no call", got, len(calls))
	}
}

func TestConvergeLiveLimits_ForceRunsEvenWhenMatching(t *testing.T) {
	fakeSlice(t, "alice", limitedCgroup())
	var calls recordedCalls
	convergeLiveLimits(context.Background(), calls.runner(nil, nil), "alice", limited, true)
	if len(calls) != 2 {
		t.Fatalf("force: %d calls, want 2", len(calls))
	}
}

func TestConvergeLiveLimits_IdleSliceIsNotAMismatch(t *testing.T) {
	orig := limitsCgroupParent
	limitsCgroupParent = t.TempDir()
	t.Cleanup(func() { limitsCgroupParent = orig })
	var calls recordedCalls
	if got := convergeLiveLimits(context.Background(), calls.runner(nil, nil), "alice", limited, true); got != "" || len(calls) != 0 {
		t.Fatalf("got %q with %d calls, want nothing for a slice that is not running", got, len(calls))
	}
}

// A clear must take a limit off a running slice: resets, not omissions,
// or the runtime values set earlier would keep it limited until reboot.
func TestConvergeLiveLimits_ClearResetsEveryProperty(t *testing.T) {
	dir := fakeSlice(t, "alice", limitedCgroup())
	var calls recordedCalls
	run := calls.runner(nil, func(args []string) {
		if args[3] == "CPUQuota=" {
			writeCgroup(t, dir, unlimitedCgroup)
		}
	})
	if got := convergeLiveLimits(context.Background(), run, "alice", limits.EffectiveLimits{}, false); got != "" {
		t.Fatalf("mismatch after clear: %q", got)
	}
	want := recordedCalls{
		{"systemctl", "set-property", "--runtime", "jabali-user-alice.slice", "CPUQuota=", "MemoryMax=infinity", "MemoryHigh=infinity", "TasksMax=infinity"},
		{"systemctl", "set-property", "--runtime", "jabali-user-alice.slice", "IOReadBandwidthMax=", "IOWriteBandwidthMax="},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls:\n got %q\nwant %q", calls, want)
	}
}

func TestConvergeLiveLimits_ReportsWhatStillDiffers(t *testing.T) {
	fakeSlice(t, "alice", unlimitedCgroup)
	var calls recordedCalls
	failIO := func(args []string) error {
		if strings.HasPrefix(args[3], "IORead") {
			return errors.New("exit status 1")
		}
		return nil
	}
	got := convergeLiveLimits(context.Background(), calls.runner(failIO, nil), "alice", limited, false)
	for _, want := range []string{"set-property io: exit status 1 (boom)", "memory.max=max want=1073741824", "pids.max=max want=300", `cpu.max="max 100000" want=150%`} {
		if !strings.Contains(got, want) {
			t.Errorf("mismatch %q lacks %q", got, want)
		}
	}
}

func TestCPUMaxMatches(t *testing.T) {
	for _, c := range []struct {
		cpuMax  string
		percent uint32
		want    bool
	}{
		{"max 100000", 0, true},
		{"150000 100000", 0, false},
		{"150000 100000", 150, true},
		{"150500 100000", 150, true}, // rounding
		{"200000 100000", 150, false},
		{"max 100000", 150, false},
		{"30000 20000", 150, true}, // another period
		{"garbage", 150, true},     // unknown format: leave alone
	} {
		if got := cpuMaxMatches(c.cpuMax, c.percent); got != c.want {
			t.Errorf("cpuMaxMatches(%q, %d) = %v, want %v", c.cpuMax, c.percent, got, c.want)
		}
	}
}
