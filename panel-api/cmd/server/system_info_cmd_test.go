package main

import (
	"bytes"
	"strings"
	"testing"
)

// GH #2029: `jabali system info` shows a disk's usage as df's Use%. The
// reporter's root filesystem is 5% in df; the share of the whole size
// (4.26%) printed 4%.
func TestPrintPartitions_UsageIsDfUsePercent(t *testing.T) {
	var buf bytes.Buffer
	printPartitions(&buf, []cliPartition{
		{MountPoint: "/", TotalBytes: 760740884480, UsedBytes: 32386052096, FreeBytes: 697378873344},
		{MountPoint: "/home", TotalBytes: 100_000, UsedBytes: 95_000, FreeBytes: 0},
	})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want a header and two disks:\n%s", len(lines), buf.String())
	}
	for i, want := range []string{"5%", "100%"} {
		f := strings.Fields(lines[i+1])
		if got := f[len(f)-1]; got != want {
			t.Errorf("%s usage %s, want %s", f[0], got, want)
		}
	}
}
