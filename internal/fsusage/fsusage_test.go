package fsusage

import (
	"syscall"
	"testing"
)

// GH #2029: the reporter's root filesystem. df -B1 shows 760740884480 total,
// 32386052096 used, 697378873344 available, 5%; about 29 GiB is reserved for
// root and is neither used nor available.
func TestFromStatfs_ReservedBlocksAreNotUsed(t *testing.T) {
	st := syscall.Statfs_t{Bsize: 4096, Blocks: 185727755, Bfree: 177821004, Bavail: 170258514}

	total, used, avail := FromStatfs(&st)

	if total != 760740884480 || used != 32386052096 || avail != 697378873344 {
		t.Errorf("got total %d used %d avail %d, want df's 760740884480 32386052096 697378873344", total, used, avail)
	}
}

func TestFromStatfs_MoreFreeThanBlocksIsNotAWrap(t *testing.T) {
	st := syscall.Statfs_t{Bsize: 4096, Blocks: 10, Bfree: 12, Bavail: 12}

	if _, used, _ := FromStatfs(&st); used != 0 {
		t.Errorf("used %d, want 0", used)
	}
}

func TestUsedPercent(t *testing.T) {
	cases := []struct {
		name        string
		used, avail uint64
		want        int
	}{
		{"the reporter's disk, as df", 32386052096, 697378873344, 5},
		{"rounds up, as df", 1, 999, 1},
		{"exact stays exact", 76_000, 19_000, 80},
		{"just over 94 shows 95", 94_010, 5_990, 95},
		{"nothing left to non-root", 95_000, 0, 100},
		{"empty filesystem", 0, 1000, 0},
		{"no size at all", 0, 0, 0},
		{"exabyte sizes don't overflow", 1 << 62, 1 << 62, 50},
		{"past 16 EiB doesn't wrap", 1 << 63, 1 << 63, 50},
	}
	for _, tc := range cases {
		if got := UsedPercent(tc.used, tc.avail); got != tc.want {
			t.Errorf("%s: UsedPercent(%d, %d) = %d, want %d", tc.name, tc.used, tc.avail, got, tc.want)
		}
	}
}
