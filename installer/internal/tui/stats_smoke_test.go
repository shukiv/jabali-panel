package tui

import (
	"testing"
	"time"
)

// Smoke: sampling /proc twice yields sane rates without panicking.
func TestSampleSys_Smoke(t *testing.T) {
	a := sampleSys(time.Unix(1000, 0))
	if !a.ok {
		t.Fatal("first sample not ok")
	}
	b := sampleSys(time.Unix(1001, 0)) // +1s
	st := computeStats(a, b)
	if st.cpuPct < 0 || st.cpuPct > 100 {
		t.Errorf("cpu%% out of range: %v", st.cpuPct)
	}
	if st.memPct < 0 || st.memPct > 100 {
		t.Errorf("mem%% out of range: %v", st.memPct)
	}
	_ = statsFooter(st, 0)  // no size yet: natural width, must not panic
	_ = statsFooter(st, 40) // narrow terminal: must shed segments, not panic
}
