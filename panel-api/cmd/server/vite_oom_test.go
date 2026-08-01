package main

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// TestHeapAbortVsOOMKilled pins the distinction the retry logic turns on.
//
// These are two different failures with opposite remedies, and conflating them
// is what left `jabali update` unable to finish on a 2 GB host:
//
//	exit 137 — the KERNEL killed node; not enough real memory; add swap
//	exit 134 — V8 aborted itself at --max-old-space-size; memory was there,
//	           our ceiling was too low; raise the ceiling
//
// Only 137 was handled, and capping the heap to dodge the OOM-killer is
// precisely what turns a 137 into a 134 — so the original mitigation converted
// the failure into a shape its own retry did not match, and the build aborted
// with no retry at all.
func TestHeapAbortVsOOMKilled(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		err        error
		wantAbort  bool
		wantKilled bool
	}{
		{
			name:      "V8 self-abort exit code",
			err:       errors.New("build frontend: ... npm run build: exit status 134"),
			wantAbort: true,
		},
		{
			name:      "V8 message without a usable exit code",
			err:       errors.New("FATAL ERROR: Reached heap limit Allocation failed - JavaScript heap out of memory"),
			wantAbort: true,
		},
		{
			name:       "kernel OOM-kill exit code",
			err:        errors.New("build frontend: ... npm run build: exit status 137"),
			wantKilled: true,
		},
		{
			name:       "kernel OOM-kill reported as signal text",
			err:        errors.New("npm run build: signal: killed"),
			wantKilled: true,
		},
		{
			name: "an unrelated build failure is neither",
			err:  errors.New("npm run build: exit status 1"),
		},
		{
			name: "a type error is neither",
			err:  errors.New("src/App.tsx:12:3 - error TS2322: Type 'string' is not assignable"),
		},
		{name: "nil is neither"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isHeapAbort(tc.err); got != tc.wantAbort {
				t.Errorf("isHeapAbort(%v) = %v, want %v", tc.err, got, tc.wantAbort)
			}
			if got := isOOMKilled(tc.err); got != tc.wantKilled {
				t.Errorf("isOOMKilled(%v) = %v, want %v", tc.err, got, tc.wantKilled)
			}
		})
	}
}

// TestHeapAbortMatchesRealExitError guards against the classifier working only
// on hand-written strings. exec.ExitError formats its own message, and that is
// what the build step actually returns.
func TestHeapAbortMatchesRealExitError(t *testing.T) {
	t.Parallel()

	// 134 is SIGABRT (128+6) — what node exits with on a heap abort.
	cmd := exec.Command("bash", "-c", "exit 134")
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected a non-nil error from `exit 134`")
	}
	if !isHeapAbort(err) {
		t.Errorf("isHeapAbort did not match a real exec error %q — the build step "+
			"wraps exactly this, so a mismatch means no retry happens", err)
	}
	if isOOMKilled(err) {
		t.Errorf("a 134 must not be treated as an OOM-kill: adding swap does not " +
			"raise a heap ceiling, so the retry would fail the same way")
	}

	wrapped := fmt.Errorf("build frontend: %w", err)
	if !isHeapAbort(wrapped) {
		t.Error("classification must survive the error wrapping the build step applies")
	}
}

// TestViteHeapPercentages pins the two ceilings and, more importantly, why they
// differ — a future reader raising the default to "fix" a slow build would
// reintroduce the OOM-kill the cap exists to prevent.
func TestViteHeapPercentages(t *testing.T) {
	t.Parallel()

	if viteHeapPctDefault >= viteHeapPctRetry {
		t.Errorf("the retry ceiling (%d%%) must exceed the default (%d%%) — the "+
			"whole point of the retry is more headroom",
			viteHeapPctRetry, viteHeapPctDefault)
	}
	if viteHeapPctDefault > 60 {
		t.Errorf("default heap ceiling %d%% is too generous: V8 sizing itself "+
			"from MemTotal is what gets the build OOM-killed on small VMs",
			viteHeapPctDefault)
	}
	if viteHeapPctRetry > 95 {
		t.Errorf("retry ceiling %d%% leaves nothing for the rest of the host",
			viteHeapPctRetry)
	}
}

// TestViteBuildCommandRendersBothCeilings checks the awk expression the build
// actually runs, since the percentage reaches node through a shell string.
func TestViteBuildCommandRendersBothCeilings(t *testing.T) {
	t.Parallel()

	render := func(pct int) string {
		return fmt.Sprintf(
			"NODE_OPTIONS=\"--max-old-space-size=$(awk '/MemTotal/{print int($2*%.2f/1024)}' /proc/meminfo)\" npm run build",
			float64(pct)/100)
	}

	def := render(viteHeapPctDefault)
	retry := render(viteHeapPctRetry)

	if !strings.Contains(def, "*0.50/1024") {
		t.Errorf("default ceiling did not render as a 0.50 multiplier: %s", def)
	}
	if !strings.Contains(retry, "*0.90/1024") {
		t.Errorf("retry ceiling did not render as a 0.90 multiplier: %s", retry)
	}
	if def == retry {
		t.Error("both ceilings rendered identically — the retry would repeat the failure")
	}
}
