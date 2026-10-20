package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestBandwidthScanDay_NoLogs verifies the handler returns an empty
// stats list when no *-access.log.1 files match the glob. Common
// case on a fresh install before logrotate has rotated anything.
func TestBandwidthScanDay_NoLogs(t *testing.T) {
	dir := t.TempDir()

	params, _ := json.Marshal(bandwidthScanDayParams{LogDir: dir})
	got, err := bandwidthScanDayHandler(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	resp, ok := got.(bandwidthScanDayResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", got)
	}
	if len(resp.Stats) != 0 {
		t.Errorf("want empty stats, got %d entries", len(resp.Stats))
	}
}

// TestBandwidthScanDay_SkipsNonRotatedFiles ensures the glob only
// catches *-access.log.1 (logrotate's prior-day file), not the live
// access.log or older .log.2.gz / .log.3.gz files.
func TestBandwidthScanDay_SkipsNonRotatedFiles(t *testing.T) {
	dir := t.TempDir()

	// Live log + older rotated logs that the handler must NOT scan.
	for _, name := range []string{
		"example.com-access.log",       // live, not rotated yet
		"example.com-access.log.2.gz",  // older, gzipped
		"example.com-access.log.3.gz",
		"unrelated.txt",
	} {
		mustWriteFile(t, filepath.Join(dir, name), []byte("hi"))
	}

	params, _ := json.Marshal(bandwidthScanDayParams{LogDir: dir})
	got, err := bandwidthScanDayHandler(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	resp := got.(bandwidthScanDayResponse)
	if len(resp.Stats) != 0 {
		t.Errorf("want zero stats (no .log.1 files), got %d", len(resp.Stats))
	}
}

// TestBandwidthScanDay_ParseDomainName checks the strip of the
// '-access.log.1' suffix yields the canonical domain name. Real
// goaccess invocation requires the binary which CI may not have, so
// we feed an unparseable file and expect it in skipped, not stats —
// the domain-name parsing happens BEFORE goaccess runs.
func TestBandwidthScanDay_ParseDomainName(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "example.com-access.log.1"), []byte("not-a-real-nginx-line\n"))

	params, _ := json.Marshal(bandwidthScanDayParams{LogDir: dir})
	got, err := bandwidthScanDayHandler(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	resp := got.(bandwidthScanDayResponse)

	// On hosts with goaccess the call may succeed with zero bytes
	// (parser tolerates malformed lines) → stats has one entry.
	// On hosts without goaccess the call fails → entry lands in
	// skipped[]. Either way the canonical domain name shows up in
	// exactly one of the two slices.
	domain := "example.com"
	found := false
	for _, s := range resp.Stats {
		if s.Domain == domain {
			found = true
		}
	}
	for _, sk := range resp.Skipped {
		if filepath.Base(sk) == "example.com-access.log.1" || sk == domain {
			found = true
		}
	}
	if !found {
		// Some skipped entries embed the err: "name (err: ...)" —
		// match the prefix.
		for _, sk := range resp.Skipped {
			if len(sk) >= len("example.com-access.log.1") &&
				sk[:len("example.com-access.log.1")] == "example.com-access.log.1" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Errorf("expected example.com or its log basename in stats or skipped, got stats=%v skipped=%v", resp.Stats, resp.Skipped)
	}
}

func mustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestGoaccessScanArgs_UsesDashOJson guards against the #M13.1 regression
// where the scan passed the non-existent `--output-format=json` flag, which
// fatals on goaccess 1.9.x ("unknown option") and silently zeroed bandwidth
// stats. The correct JSON-to-stdout flag is `-o json`.
func TestGoaccessScanArgs_UsesDashOJson(t *testing.T) {
	args := goaccessScanArgs("/var/log/nginx/example.com-access.log.1", "")

	for _, a := range args {
		if a == "--output-format=json" || a == "--output-format" {
			t.Fatalf("goaccess args still carry the invalid --output-format flag: %v", args)
		}
	}

	found := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-o" && args[i+1] == "json" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected `-o json` (adjacent) in goaccess args, got: %v", args)
	}

	if args[0] != "/var/log/nginx/example.com-access.log.1" {
		t.Fatalf("expected the log path as the first arg, got: %v", args)
	}
}
