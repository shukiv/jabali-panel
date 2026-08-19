package repository

import "testing"

// GH #1175: the shared allocator hands out the lowest free port in the pool,
// skipping taken ones and returning 0 (exhausted) when the pool is full.
func TestLowestFreePort(t *testing.T) {
	cases := []struct {
		name           string
		used           []int
		min, max, want int
	}{
		{"empty pool → min", nil, 30000, 30002, 30000},
		{"lowest gap at start", []int{30001, 30002}, 30000, 30002, 30000},
		{"fills the first gap", []int{30000, 30002}, 30000, 30002, 30001},
		{"next after a run", []int{30000, 30001}, 30000, 30002, 30002},
		{"exhausted → 0", []int{30000, 30001, 30002}, 30000, 30002, 0},
		{"ignores out-of-pool used", []int{9999, 40000}, 30000, 30001, 30000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := lowestFreePort(tc.used, tc.min, tc.max); got != tc.want {
				t.Fatalf("lowestFreePort(%v, %d, %d) = %d, want %d", tc.used, tc.min, tc.max, got, tc.want)
			}
		})
	}
}
