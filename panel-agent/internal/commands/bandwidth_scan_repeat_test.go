package commands

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// nginx's logrotate config has notifempty: after a day with no requests the
// live log is not rotated, and <domain>-access.log.1 still holds the last
// day that had traffic. The scan must count that file once, not once more
// every day until the domain gets traffic again.
func TestBandwidthScanDay_UnrotatedLogCountedOnce(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "quiet.example-access.log.1")
	mustWriteFile(t, log, []byte("x\n"))

	prevState, prevNow, prevExec := bandwidthScanStatePath, bandwidthNow, execCommandContext
	bandwidthScanStatePath = filepath.Join(t.TempDir(), "state.json")
	execCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "printf", "%s", `{"general": {"total_requests": 9, "bandwidth": 900}}`)
	}
	t.Cleanup(func() { bandwidthScanStatePath, bandwidthNow, execCommandContext = prevState, prevNow, prevExec })

	scan := func(now string) []string {
		t.Helper()
		ts, _ := time.Parse(time.RFC3339, now)
		bandwidthNow = func() time.Time { return ts }
		params, _ := json.Marshal(bandwidthScanDayParams{LogDir: dir})
		out, err := bandwidthScanDayHandler(context.Background(), params)
		if err != nil {
			t.Fatal(err)
		}
		var days []string
		for _, s := range out.(bandwidthScanDayResponse).Stats {
			days = append(days, s.Domain+"@"+s.Day)
		}
		return days
	}

	if got := scan("2026-10-16T08:00:00Z"); len(got) != 1 || got[0] != "quiet.example@2026-10-15" {
		t.Fatalf("first scan = %v, want quiet.example@2026-10-15", got)
	}
	// A second scan the same day (a panel restart) writes the same day again.
	if got := scan("2026-10-16T09:00:00Z"); len(got) != 1 || got[0] != "quiet.example@2026-10-15" {
		t.Fatalf("same-day rescan = %v, want quiet.example@2026-10-15 again", got)
	}
	// Next day, no rotation happened: the file is 10-15's, not 10-16's.
	if got := scan("2026-10-17T08:00:00Z"); len(got) != 0 {
		t.Fatalf("scan after a day without rotation = %v, want nothing", got)
	}
	// The domain had traffic and the log rotated: the new file is counted.
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, log, []byte("x\ny\n"))
	if got := scan("2026-10-18T08:00:00Z"); len(got) != 1 || got[0] != "quiet.example@2026-10-17" {
		t.Fatalf("scan after rotation = %v, want quiet.example@2026-10-17", got)
	}
}
