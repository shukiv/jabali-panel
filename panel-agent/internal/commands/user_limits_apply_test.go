package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/limits"
)

// setupTempSystemdRoot isolates a test's writes to a tempdir so we never
// touch /etc/systemd. Mirrors the pattern in user_slice_ensure_test.go.
func setupTempSystemdRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	testMutex.Lock()
	origRoot := systemdRoot
	systemdRoot = func() string { return dir }
	testMutex.Unlock()
	t.Cleanup(func() {
		testMutex.Lock()
		systemdRoot = origRoot
		testMutex.Unlock()
	})
	return dir
}

// stubKillProcess replaces killProcess with a no-op so tests don't
// actually try to SIGHUP PID 1 (which fails with EPERM under any
// non-root test runner). Mirrors the systemd-root + runCmd stubs.
func stubKillProcess(t *testing.T) {
	t.Helper()
	testMutex.Lock()
	orig := killProcess
	killProcess = func(_ int, _ os.Signal) error { return nil }
	testMutex.Unlock()
	t.Cleanup(func() {
		testMutex.Lock()
		killProcess = orig
		testMutex.Unlock()
	})
}

// stubKillProcessCounter replaces killProcess with a no-op that counts
// how many times it fires, so tests can prove the SIGHUP daemon-reload is
// skipped on the no-change path. Mirrors stubKillProcess.
func stubKillProcessCounter(t *testing.T) *int {
	t.Helper()
	var count int
	testMutex.Lock()
	orig := killProcess
	killProcess = func(_ int, _ os.Signal) error { count++; return nil }
	testMutex.Unlock()
	t.Cleanup(func() {
		testMutex.Lock()
		killProcess = orig
		testMutex.Unlock()
	})
	return &count
}

// stubRunCmd replaces runCmd with a recorder that records the args of
// every invocation and returns predetermined stdout/stderr.
func stubRunCmd(t *testing.T) *[][]string {
	t.Helper()
	var calls [][]string
	testMutex.Lock()
	orig := runCmd
	runCmd = func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
		c := append([]string{name}, args...)
		calls = append(calls, c)
		return []byte(""), []byte(""), nil
	}
	testMutex.Unlock()
	t.Cleanup(func() {
		testMutex.Lock()
		runCmd = orig
		testMutex.Unlock()
	})
	return &calls
}

func TestBuildLimitsDropinContent(t *testing.T) {
	tests := []struct {
		name    string
		in      limits.EffectiveLimits
		want    string
		wantEmpty bool
	}{
		{
			name: "all zero → empty",
			in:   limits.EffectiveLimits{},
			wantEmpty: true,
		},
		{
			name: "full package",
			in: limits.EffectiveLimits{
				CPUQuotaPercent: 200,
				MemoryLimitMB:   4096,
				IOReadMbps:      100,
				IOWriteMbps:     50,
				MaxTasks:        500,
			},
			want: "[Slice]\nCPUQuota=200%\nMemoryMax=4096M\nMemoryHigh=3686M\nIOReadBandwidthMax=/ 100M\nIOWriteBandwidthMax=/ 50M\nTasksMax=500\n",
		},
		{
			name: "cpu only",
			in:   limits.EffectiveLimits{CPUQuotaPercent: 100},
			want: "[Slice]\nCPUQuota=100%\n",
		},
		{
			name: "memory only → high derived",
			in:   limits.EffectiveLimits{MemoryLimitMB: 1000},
			want: "[Slice]\nMemoryMax=1000M\nMemoryHigh=900M\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildLimitsDropinContent(tt.in)
			if tt.wantEmpty {
				if got != "" {
					t.Errorf("expected empty, got %q", got)
				}
				return
			}
			if got != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestUserLimitsApply_WritesDropin(t *testing.T) {
	root := setupTempSystemdRoot(t)
	stubKillProcess(t)
	calls := stubRunCmd(t)

	params, _ := json.Marshal(userLimitsApplyParams{
		Username:        "testuser",
		CPUQuotaPercent: 200,
		MemoryLimitMB:   1024,
		MaxTasks:        500,
		QuotaMount:      "/home",
		DiskQuotaMB:     5120,
	})
	result, err := userLimitsApplyHandler(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resp := result.(*userLimitsApplyResponse)
	if !resp.CgroupApplied {
		t.Error("CgroupApplied should be true")
	}
	if !resp.QuotaApplied {
		t.Error("QuotaApplied should be true")
	}

	// Drop-in exists with expected content.
	expectedPath := filepath.Join(root, "jabali-user-testuser.slice.d", "limits.conf")
	data, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("drop-in missing: %v", err)
	}
	content := string(data)
	for _, want := range []string{"[Slice]", "CPUQuota=200%", "MemoryMax=1024M", "MemoryHigh=921M", "TasksMax=500"} {
		if !strings.Contains(content, want) {
			t.Errorf("drop-in missing %q:\n%s", want, content)
		}
	}

	// Verify setquota was called with the explicit mount path (not -a)
	// and the right block count (5120 MB × 1024 = 5242880 KB).
	var found bool
	for _, c := range *calls {
		if len(c) > 0 && c[0] == "setquota" {
			found = true
			args := strings.Join(c, " ")
			if !strings.Contains(args, "5242880") {
				t.Errorf("setquota blocks wrong: %s", args)
			}
			if !strings.Contains(args, "/home") {
				t.Errorf("setquota missing explicit /home mount: %s", args)
			}
			if strings.Contains(args, "-a") {
				t.Errorf("setquota must NOT use -a: %s", args)
			}
		}
	}
	if !found {
		t.Error("setquota was not called")
	}
}

func TestUserLimitsApply_AllZeros_RemovesDropin(t *testing.T) {
	root := setupTempSystemdRoot(t)
	stubKillProcess(t)
	_ = stubRunCmd(t)

	// First apply real limits → drop-in gets created.
	params, _ := json.Marshal(userLimitsApplyParams{
		Username: "z", MemoryLimitMB: 512, QuotaMount: "/home",
	})
	if _, err := userLimitsApplyHandler(context.Background(), params); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	dropinPath := filepath.Join(root, "jabali-user-z.slice.d", "limits.conf")
	if _, err := os.Stat(dropinPath); err != nil {
		t.Fatalf("first apply did not create drop-in: %v", err)
	}

	// Now apply all-zero limits → drop-in should be deleted.
	paramsZero, _ := json.Marshal(userLimitsApplyParams{Username: "z", QuotaMount: "/home"})
	if _, err := userLimitsApplyHandler(context.Background(), paramsZero); err != nil {
		t.Fatalf("zero apply: %v", err)
	}
	if _, err := os.Stat(dropinPath); !os.IsNotExist(err) {
		t.Errorf("drop-in should have been removed; err=%v", err)
	}
}

func TestUserLimitsApply_Idempotent(t *testing.T) {
	setupTempSystemdRoot(t)
	killed := stubKillProcessCounter(t)
	calls := stubRunCmd(t)

	params, _ := json.Marshal(userLimitsApplyParams{
		Username: "idempotent", MemoryLimitMB: 1024, QuotaMount: "/home",
	})
	// First apply.
	r1, _ := userLimitsApplyHandler(context.Background(), params)
	// Second apply with identical input.
	r2, err := userLimitsApplyHandler(context.Background(), params)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if r1.(*userLimitsApplyResponse).NoChange {
		t.Error("first apply should have NoChange=false")
	}
	if !r2.(*userLimitsApplyResponse).NoChange {
		t.Error("second apply should have NoChange=true")
	}
	// GH #1660: the disk quota is idempotently re-asserted on EVERY apply,
	// so setquota runs on both the first (changed) and second (no-change)
	// applies. What the no-change path skips is the expensive systemd
	// daemon-reload (SIGHUP), which fires only on the first apply.
	setquotaCalls := 0
	for _, c := range *calls {
		if len(c) > 0 && c[0] == "setquota" {
			setquotaCalls++
		}
	}
	if setquotaCalls != 2 {
		t.Errorf("expected setquota on both applies (idempotent disk re-assert), got %d", setquotaCalls)
	}
	if *killed != 1 {
		t.Errorf("expected SIGHUP daemon-reload on the first (changed) apply only, got %d", *killed)
	}
}

func TestUserLimitsApply_InvalidUsername(t *testing.T) {
	setupTempSystemdRoot(t)
	stubRunCmd(t)
	bad := []string{"UPPERCASE", "0startsnum", "has space", "..", "has/slash"}
	for _, u := range bad {
		params, _ := json.Marshal(userLimitsApplyParams{Username: u})
		if _, err := userLimitsApplyHandler(context.Background(), params); err == nil {
			t.Errorf("username %q should have been rejected", u)
		}
	}
}

func TestUserLimitsApply_BoundsValidation(t *testing.T) {
	setupTempSystemdRoot(t)
	stubRunCmd(t)
	// cpu_quota_percent above max → reject.
	params, _ := json.Marshal(userLimitsApplyParams{
		Username:        "boundstest",
		CPUQuotaPercent: limits.MaxCPUQuotaPercent + 1,
	})
	if _, err := userLimitsApplyHandler(context.Background(), params); err == nil {
		t.Error("over-cap CPU quota should have been rejected")
	}
}

func TestUserLimitsApply_NoQuotaMount_SkipsSetquota(t *testing.T) {
	setupTempSystemdRoot(t)
	stubKillProcess(t)
	calls := stubRunCmd(t)
	params, _ := json.Marshal(userLimitsApplyParams{
		Username: "cgrouponly", MemoryLimitMB: 512,
		// QuotaMount intentionally empty.
	})
	r, err := userLimitsApplyHandler(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if r.(*userLimitsApplyResponse).QuotaApplied {
		t.Error("QuotaApplied should be false when no mount supplied")
	}
	for _, c := range *calls {
		if len(c) > 0 && c[0] == "setquota" {
			t.Error("setquota should not have been called without QuotaMount")
		}
	}
}

// TestUserLimitsApply_DiskOnlyChange_ReAppliesQuota is the GH #1660
// regression guard. When only the disk quota changes and the cgroup
// limits are untouched, the rendered drop-in is byte-identical to what is
// on disk (buildLimitsDropinContent emits no disk directive), so the
// handler takes the noChange path. The bug was that setquota sat AFTER the
// noChange early-return, so a disk-quota raise was silently dropped and
// the kernel kept the stale ceiling. The fix runs setquota unconditionally
// before that return; this test fails RED on the pre-fix ordering.
func TestUserLimitsApply_DiskOnlyChange_ReAppliesQuota(t *testing.T) {
	root := setupTempSystemdRoot(t)
	killed := stubKillProcessCounter(t)
	calls := stubRunCmd(t)

	// Pre-write the drop-in with the SAME cgroup values the apply renders,
	// so the cgroup content is unchanged and the handler sees noChange.
	effective := limits.EffectiveLimits{MemoryLimitMB: 1024}
	dropinDir := filepath.Join(root, "jabali-user-racingtips.slice.d")
	if err := os.MkdirAll(dropinDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dropinDir, "limits.conf"),
		[]byte(buildLimitsDropinContent(effective)), 0644); err != nil {
		t.Fatal(err)
	}

	// Apply the identical cgroup limits but a NEW disk quota (6144 MB).
	params, _ := json.Marshal(userLimitsApplyParams{
		Username:      "racingtips",
		MemoryLimitMB: 1024,
		QuotaMount:    "/",
		DiskQuotaMB:   6144,
	})
	result, err := userLimitsApplyHandler(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resp := result.(*userLimitsApplyResponse)
	if !resp.NoChange {
		t.Fatalf("expected NoChange=true (cgroup drop-in identical), got false — test premise broken")
	}
	if !resp.QuotaApplied {
		t.Error("QuotaApplied should be true even when the cgroup drop-in is unchanged (GH #1660)")
	}

	// The optimization must survive: no SIGHUP daemon-reload on the
	// no-change path.
	if *killed != 0 {
		t.Errorf("SIGHUP daemon-reload should be skipped on the no-change path, fired %d times", *killed)
	}

	// setquota MUST have run with the new ceiling (6144 MB × 1024 =
	// 6291456 KB) on the explicit "/" mount, despite the unchanged drop-in.
	var found bool
	for _, c := range *calls {
		if len(c) > 0 && c[0] == "setquota" {
			found = true
			args := strings.Join(c, " ")
			if !strings.Contains(args, "6291456") {
				t.Errorf("setquota blocks wrong: %s", args)
			}
			if c[len(c)-1] != "/" {
				t.Errorf("setquota missing explicit / mount: %s", args)
			}
		}
	}
	if !found {
		t.Error("setquota was not called on a disk-only quota change (GH #1660 regression)")
	}
}
