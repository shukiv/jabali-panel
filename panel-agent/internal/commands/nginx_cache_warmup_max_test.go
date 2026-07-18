package commands

import "testing"

// JAB-95 Phase 1: the effective warmup budget is reported (clamped_to), so the
// clamp is never silent.
func TestEffectiveWarmupMax(t *testing.T) {
	cases := []struct{ req, want int }{
		{0, cacheWarmupDefaultMax},   // unset -> default
		{-5, cacheWarmupDefaultMax},  // negative -> default
		{10, 10},                     // within range -> honored
		{cacheWarmupHardMax, cacheWarmupHardMax},
		{100, cacheWarmupHardMax},    // over the cap -> clamped, and reportable
	}
	for _, c := range cases {
		if got := effectiveWarmupMax(c.req); got != c.want {
			t.Errorf("effectiveWarmupMax(%d)=%d, want %d", c.req, got, c.want)
		}
	}
}
