// Package fsusage is how full a filesystem is, the way df reports it (GH
// #2029). The agent's disk figures, the panel's disk alerts, the disk-full
// notifications and `jabali system info` all use it, so they agree with each
// other and with df.
//
// ext4 reserves blocks for root (5% by default). Those blocks are neither
// used nor available: df's Used is what files take, its Available is what
// non-root can still write, and Used + Available is less than the size.
package fsusage

import (
	"math/bits"
	"syscall"
)

// FromStatfs returns df's Size, Used and Available in bytes.
func FromStatfs(st *syscall.Statfs_t) (total, used, avail uint64) {
	bsize := uint64(st.Bsize)
	total = st.Blocks * bsize
	avail = st.Bavail * bsize
	if st.Bfree < st.Blocks {
		used = (st.Blocks - st.Bfree) * bsize
	}
	return total, used, avail
}

// UsedPercent is df's Use%: used as a share of the space non-root can use
// (used + avail), rounded up to a whole percent. A filesystem is 100% full
// when nothing is left to non-root, whatever root still has in reserve.
func UsedPercent(used, avail uint64) int {
	n, carry := bits.Add64(used, avail, 0)
	if carry != 0 {
		// Past 16 EiB: halve both; the share is the same.
		used, avail = used>>1, avail>>1
		n = used + avail
	}
	if n == 0 {
		return 0
	}
	// used <= n, so the high word is below n and Div64 can't overflow.
	hi, lo := bits.Mul64(used, 100)
	q, r := bits.Div64(hi, lo, n)
	if r != 0 {
		q++
	}
	return int(q)
}
