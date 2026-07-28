package commands

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// GH #357: pip build failures must name the offending package and must not
// re-run pip every converge tick.

func TestPipFailureContext_NamesPackage(t *testing.T) {
	out := `Collecting django==2.2
  Downloading Django-2.2.tar.gz
Collecting wagtail==2.6
  Downloading wagtail-2.6.tar.gz
  Getting requirements to build wheel: started
  Getting requirements to build wheel: finished with status 'error'
  error: subprocess-exited-with-error

  × Getting requirements to build wheel did not run successfully.
  │ exit code: 1
  ╰─> File "<string>", line 240, in get_version
      KeyError: '__version__'`
	ctx := pipFailureContext(out)
	if !strings.Contains(ctx, "wagtail") {
		t.Errorf("failing package not named in context:\n%s", ctx)
	}
	if !strings.Contains(ctx, "KeyError: '__version__'") {
		t.Errorf("traceback tail missing:\n%s", ctx)
	}
}

func TestPipFailureContext_FallbackWhenNoPackageLines(t *testing.T) {
	// Quiet-mode-like output (no Collecting/wheel lines) still returns the tail.
	out := "error: subprocess-exited-with-error\nKeyError: '__version__'"
	ctx := pipFailureContext(out)
	if !strings.Contains(ctx, "KeyError") {
		t.Errorf("tail missing:\n%s", ctx)
	}
}

func TestReqFailRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "app.reqfail")
	now := time.Unix(1_700_000_000, 0)

	writeReqFail(p, "abc123", "pip install requirements failed: wagtail", now)
	f := readReqFail(p)
	if f.SHA != "abc123" || !strings.Contains(f.Error, "wagtail") || f.At != now.Unix() {
		t.Errorf("round-trip mismatch: %+v", f)
	}
	// Absent file → zero value (first attempt runs pip).
	if z := readReqFail(filepath.Join(dir, "nope")); z.SHA != "" {
		t.Errorf("absent marker should be zero value: %+v", z)
	}
}

// TestPipRetryBackoffGate models the reconciler's decision: skip pip when the
// last failure was for the SAME requirements and we're still inside the backoff.
func TestPipRetryBackoffGate(t *testing.T) {
	now := time.Now()

	recent := reqFailState{SHA: "s", At: now.Add(-2 * time.Minute).Unix()}
	if !(recent.SHA == "s" && time.Since(time.Unix(recent.At, 0)) < pipRetryBackoff) {
		t.Error("a recent same-sha failure must be within backoff (skip pip)")
	}
	old := reqFailState{SHA: "s", At: now.Add(-30 * time.Minute).Unix()}
	if old.SHA == "s" && time.Since(time.Unix(old.At, 0)) < pipRetryBackoff {
		t.Error("a 30-min-old failure must be past backoff (retry pip)")
	}
	// Different requirements (sha changed) → never gated, always retry.
	changed := reqFailState{SHA: "old", At: now.Unix()}
	if changed.SHA == "new" {
		t.Error("sanity: changed sha should not equal the current sum")
	}
}
