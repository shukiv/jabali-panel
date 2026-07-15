package api

import "testing"

// GH #429: a source SSH port defaults to 22 when unset/out of range.
func TestNormalizeSourcePort(t *testing.T) {
	cases := map[int]int{0: 22, -1: 22, 65536: 22, 22: 22, 2222: 2222, 1: 1, 65535: 65535}
	for in, want := range cases {
		if got := normalizeSourcePort(in); got != want {
			t.Errorf("normalizeSourcePort(%d) = %d, want %d", in, got, want)
		}
	}
}
