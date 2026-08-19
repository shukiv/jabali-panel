package commands

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// swapRunCmd installs a fake runCmd and returns a restore func.
func swapRunCmd(fn func(ctx context.Context, name string, args ...string) ([]byte, []byte, error)) func() {
	testMutex.Lock()
	orig := runCmd
	runCmd = fn
	testMutex.Unlock()
	return func() {
		testMutex.Lock()
		runCmd = orig
		testMutex.Unlock()
	}
}

// The automatic du fallback IO-starved a production box at peak (dashboard
// polls every 5s; quota reporting 0 KB for an empty-looking user triggered a
// full home walk each time). Quota is now the ONLY automatic source — a
// default report must NEVER exec du, even when quota has no answer.
func TestUserLimitsReport_NoMeasureNeverRunsDu(t *testing.T) {
	restore := swapRunCmd(func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
		if name != "quota" {
			t.Errorf("unexpected exec without measure_disk: %s %v", name, args)
		}
		return []byte(""), nil, nil
	})
	defer restore()

	raw, _ := json.Marshal(map[string]any{"username": "nosuchuser-drill", "quota_mount": "/"})
	if _, err := userLimitsReportHandler(context.Background(), raw); err != nil {
		t.Fatalf("report: %v", err)
	}
}

// measure_disk=true is the Measure button: the walk runs, but only under
// ionice -c3 / nice -n19 so it can never compete with serving traffic.
func TestUserLimitsReport_MeasureRunsIonicedDu(t *testing.T) {
	var duArgs []string
	restore := swapRunCmd(func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
		if name == "ionice" {
			duArgs = append([]string{name}, args...)
			return []byte("4096\t/home/root"), nil, nil
		}
		return []byte(""), nil, nil
	})
	defer restore()

	got, ok := measureHomeKB(context.Background(), "/home/whoever")
	if !ok || got != 4096 {
		t.Fatalf("want 4096 KB, got %d ok=%v", got, ok)
	}
	joined := strings.Join(duArgs, " ")
	if joined != "ionice -c3 nice -n19 du -sk /home/whoever" {
		t.Fatalf("measure must run under ionice -c3 nice -n19, got: %s", joined)
	}

	// And a missing home short-circuits before any exec.
	if got, ok := diskUsageMeasure(context.Background(), "definitely-missing-user-xyz"); ok || got != 0 {
		t.Fatalf("missing home must (0,false), got %d %v", got, ok)
	}
}
