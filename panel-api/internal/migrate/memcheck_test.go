package migrate

import (
	"strings"
	"testing"
)

// MemAvailable, not MemFree: MemFree excludes reclaimable page cache, so a
// perfectly healthy host reports nearly nothing free and the check would fire on
// exactly the boxes that are fine.
func TestParseMemAvailableMB_PrefersMemAvailableOverMemFree(t *testing.T) {
	meminfo := `MemTotal:        8039132 kB
MemFree:          201332 kB
MemAvailable:    6100480 kB
Buffers:          104928 kB
`
	if got, want := parseMemAvailableMB(meminfo), 6100480/1024; got != want {
		t.Fatalf("got %d MB, want %d", got, want)
	}
}

// Unreadable or unexpected input must be reported as "unknown" (0), which the
// caller treats as do-not-block. A check that cannot see must not veto.
func TestParseMemAvailableMB_UnknownOnBadInput(t *testing.T) {
	for name, in := range map[string]string{
		"empty":        "",
		"no field":     "MemTotal: 8039132 kB\nMemFree: 201332 kB\n",
		"not a number": "MemAvailable:    banana kB\n",
		"truncated":    "MemAvailable:\n",
		"negative":     "MemAvailable:    -5 kB\n",
	} {
		if got := parseMemAvailableMB(in); got != 0 {
			t.Errorf("%s: got %d, want 0 (unknown)", name, got)
		}
	}
}

func TestCheckRestoreMemory_Thresholds(t *testing.T) {
	// The constants must stay ordered, or "warn" would be unreachable and a
	// tight-but-workable host would be refused outright.
	if memRefuseBelowMB >= memWarnBelowMB {
		t.Fatalf("refuse floor (%d) must be below the warn threshold (%d)", memRefuseBelowMB, memWarnBelowMB)
	}
}

// The refusal has to say what to do about it. The incident this came from was
// diagnosed from kernel logs after a console reboot; an operator hitting the
// guard should not need that.
func TestCheckRestoreMemory_RefusalIsActionable(t *testing.T) {
	// Exercise the message via the same formatting path the caller sees.
	_, err := checkRestoreMemoryFor(memRefuseBelowMB - 1)
	if err == nil {
		t.Fatal("expected a refusal below the floor")
	}
	for _, want := range []string{"available", "need at least", "Free memory"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal missing %q: %v", want, err)
		}
	}
}

func TestCheckRestoreMemory_WarnsButAllowsWhenTight(t *testing.T) {
	warn, err := checkRestoreMemoryFor(memWarnBelowMB - 1)
	if err != nil {
		t.Fatalf("a tight-but-workable host must not be refused: %v", err)
	}
	if warn == "" {
		t.Error("expected a warning between the floor and the comfortable threshold")
	}
}

func TestCheckRestoreMemory_SilentWhenHealthyOrUnknown(t *testing.T) {
	if warn, err := checkRestoreMemoryFor(memWarnBelowMB + 1); warn != "" || err != nil {
		t.Errorf("healthy host: warn=%q err=%v", warn, err)
	}
	// 0 == could not measure.
	if warn, err := checkRestoreMemoryFor(0); warn != "" || err != nil {
		t.Errorf("unmeasurable host must not block: warn=%q err=%v", warn, err)
	}
}
