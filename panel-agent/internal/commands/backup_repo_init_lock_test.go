package commands

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// setRepoInitLockDir redirects the package-global lock dir to a temp dir for the
// duration of one test and restores it afterwards. Tests that touch it must not
// run in parallel with each other.
func setRepoInitLockDir(t *testing.T, dir string) {
	t.Helper()
	prev := repoInitLockDir
	repoInitLockDir = dir
	t.Cleanup(func() { repoInitLockDir = prev })
}

// TestWithRepoInitLock_SerializesSameRepo is the JAB-405 guarantee: concurrent
// jobs for the SAME destination never run their probe→init bodies at the same
// time, so two `restic init` calls can't race on one empty repo. An atomic
// in-flight counter records any overlap; removing the flock reddens this.
func TestWithRepoInitLock_SerializesSameRepo(t *testing.T) {
	setRepoInitLockDir(t, t.TempDir())
	const n = 20
	var active, violations int32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = withRepoInitLock(context.Background(), "sftp:user@host:/repo", func() error {
				if atomic.AddInt32(&active, 1) > 1 {
					atomic.AddInt32(&violations, 1)
				}
				time.Sleep(5 * time.Millisecond)
				atomic.AddInt32(&active, -1)
				return nil
			})
		}()
	}
	wg.Wait()
	if violations != 0 {
		t.Fatalf("init not serialized for one destination: %d overlapping runs", violations)
	}
}

// TestWithRepoInitLock_DistinctReposDoNotBlock is the discriminator against a
// single global lock: a second destination must be able to run its body while a
// first destination still holds its lock. Keying the lock file on a constant
// (instead of the repo URL) reddens this.
func TestWithRepoInitLock_DistinctReposDoNotBlock(t *testing.T) {
	setRepoInitLockDir(t, t.TempDir())
	held := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = withRepoInitLock(context.Background(), "repo-A", func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held // repo-A is now holding its lock

	ranB := make(chan struct{})
	go func() {
		_ = withRepoInitLock(context.Background(), "repo-B", func() error {
			close(ranB)
			return nil
		})
	}()
	select {
	case <-ranB: // repo-B ran while repo-A held its lock → per-destination
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("repo-B blocked while repo-A held its lock — lock is global, not per-destination")
	}
	close(release)
}

// TestWithRepoInitLock_OpenErrorFailsLoud: a lock that cannot be created must
// return an error and NOT run fn — running init unserialized is the bug. A
// read-only lock dir makes OpenFile fail; swallowing that error reddens this.
func TestWithRepoInitLock_OpenErrorFailsLoud(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions, so O_CREATE would succeed")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil { // r-x: no write, so O_CREATE fails
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) // let TempDir cleanup remove it
	setRepoInitLockDir(t, dir)

	ran := false
	err := withRepoInitLock(context.Background(), "repo", func() error {
		ran = true
		return nil
	})
	if err == nil {
		t.Fatal("expected an error when the lock file cannot be opened in a read-only dir")
	}
	// Pin the open-error branch specifically: an open failure that is swallowed
	// falls through to the flock attempt on a nil file (a different error), so
	// asserting the identity of the message — not merely "some error" — is what
	// keeps this branch honest.
	if !strings.Contains(err.Error(), "open repo-init lock") {
		t.Fatalf("want the open-lock error surfaced, got %v", err)
	}
	if ran {
		t.Fatal("fn must not run when the lock could not be acquired")
	}
}

// TestWithRepoInitLock_CtxCancelWhileHeld: while another job holds the lock, a
// caller whose ctx is cancelled must return promptly with a ctx error rather
// than block for the whole max-wait — flock has no ctx support, so the poll
// loop must select on ctx.Done(). Deleting that select arm reddens this.
func TestWithRepoInitLock_CtxCancelWhileHeld(t *testing.T) {
	setRepoInitLockDir(t, t.TempDir())
	held := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = withRepoInitLock(context.Background(), "repo", func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held // the lock is held by another job

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before we try to acquire
	done := make(chan error, 1)
	ran := make(chan struct{}, 1)
	go func() {
		done <- withRepoInitLock(ctx, "repo", func() error {
			ran <- struct{}{}
			return nil
		})
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a ctx error while the lock was held by another job")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want a context.Canceled error, got %v", err)
		}
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("withRepoInitLock ignored ctx cancellation while the lock was held")
	}
	select {
	case <-ran:
		t.Fatal("fn must not run when acquisition was cancelled")
	default:
	}
	close(release)
}

// TestBackupDestTestConn_InitSerialized pins that the backup.dest.test handler's
// auto-init is routed through withRepoInitLock (JAB-405): it is a second init
// door that a scheduled first-run can race, and its behavior is not cheaply
// testable (it shells out to real restic), so the wiring is pinned at the source
// level. Reverting to a bare backup.InitRemote reddens this.
func TestBackupDestTestConn_InitSerialized(t *testing.T) {
	b, err := os.ReadFile("backup_dest_testconn.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	src := string(b)
	wrap := strings.Index(src, "withRepoInitLock(ctx, p.URL")
	if wrap < 0 {
		t.Fatal("backup.dest.test init must be serialized through withRepoInitLock (JAB-405)")
	}
	initIdx := strings.Index(src, "backup.InitRemote(")
	if initIdx < 0 || initIdx < wrap {
		t.Fatal("backup.InitRemote must sit inside the withRepoInitLock closure, not before it")
	}
}
