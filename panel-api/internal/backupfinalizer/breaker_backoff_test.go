package backupfinalizer

import (
	"testing"
	"time"
)

// JAB-362: exponential dispatch backoff, base 1h, cap 24h.
func TestBreakerBackoffFor(t *testing.T) {
	cases := []struct {
		n    int
		want time.Duration
	}{
		{0, time.Hour},        // clamped to n>=1
		{1, time.Hour},        // base
		{2, 2 * time.Hour},    // *2
		{3, 4 * time.Hour},    // *4
		{4, 8 * time.Hour},    // *8
		{5, 16 * time.Hour},   // *16
		{6, 24 * time.Hour},   // 32h capped to 24h
		{100, 24 * time.Hour}, // stays capped, no overflow
	}
	for _, c := range cases {
		if got := breakerBackoffFor(c.n); got != c.want {
			t.Errorf("breakerBackoffFor(%d) = %s; want %s", c.n, got, c.want)
		}
	}
}
