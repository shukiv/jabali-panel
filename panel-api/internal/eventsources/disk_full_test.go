package eventsources

import (
	"errors"
	"syscall"
	"testing"
)

func stubStatfs(t *testing.T, st syscall.Statfs_t, err error) {
	t.Helper()
	prev := statfs
	statfs = func(_ string, out *syscall.Statfs_t) error {
		*out = st
		return err
	}
	t.Cleanup(func() { statfs = prev })
}

// GH #2029: the notification's percent is df's Use%. On the reporter's root
// filesystem (about 29 GiB reserved for root) that is 5%, not the 9% that
// counting the reserve as used gave.
func TestDiskUsedPercent_IsDfUsePercent(t *testing.T) {
	stubStatfs(t, syscall.Statfs_t{Bsize: 4096, Blocks: 185727755, Bfree: 177821004, Bavail: 170258514}, nil)

	if pct, ok := diskUsedPercent("/"); !ok || pct != 5 {
		t.Errorf("got %d%% ok=%v, want 5%% ok=true", pct, ok)
	}
}

// Full to non-root is 100%, whatever root still has in reserve.
func TestDiskUsedPercent_NothingLeftToNonRootIsFull(t *testing.T) {
	stubStatfs(t, syscall.Statfs_t{Bsize: 4096, Blocks: 1000, Bfree: 50, Bavail: 0}, nil)

	if pct, ok := diskUsedPercent("/"); !ok || pct != 100 {
		t.Errorf("got %d%% ok=%v, want 100%% ok=true", pct, ok)
	}
}

func TestDiskUsedPercent_SkipsMissingAndSizeless(t *testing.T) {
	// The error alone decides: whatever the struct holds is not read.
	stubStatfs(t, syscall.Statfs_t{Bsize: 4096, Blocks: 1000, Bfree: 500, Bavail: 450}, errors.New("no such file or directory"))
	if _, ok := diskUsedPercent("/var/lib/mysql"); ok {
		t.Error("a missing mount was not skipped")
	}

	stubStatfs(t, syscall.Statfs_t{Bsize: 4096}, nil)
	if _, ok := diskUsedPercent("/"); ok {
		t.Error("a filesystem with no size was not skipped")
	}
}
