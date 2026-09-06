package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func newRetentionTestCmd() *cobra.Command {
	c := &cobra.Command{}
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	return c
}

func testDest() *models.BackupDestination {
	return &models.BackupDestination{ID: "d1", Name: "nightly", URL: "/var/lib/jabali-backups/repo"}
}

func TestRetention_UnlocksAndRetriesOnStaleLock(t *testing.T) {
	orig := retentionExec
	t.Cleanup(func() { retentionExec = orig })

	var seq []string
	forgetN := 0
	retentionExec = func(_ context.Context, _ []string, _ io.Writer, stderr io.Writer, _ string, args ...string) error {
		seq = append(seq, strings.Join(args, " "))
		if hasArg(args, "unlock") {
			return nil // unlock succeeds
		}
		forgetN++
		if forgetN == 1 {
			// First forget: the stale-lock error, written to stderr like restic.
			io.WriteString(stderr, "unable to create lock in backend: repository is already locked by PID 2540018 on host by root\n")
			return errors.New("exit status 11")
		}
		return nil // retry succeeds
	}

	err := runResticWithLockRecovery(context.Background(), newRetentionTestCmd(), testDest(),
		[]string{"--repo", "/var/lib/jabali-backups/repo", "forget", "--tag", "schedule-id=s1"})
	if err != nil {
		t.Fatalf("expected recovery to succeed, got %v", err)
	}
	if len(seq) != 3 {
		t.Fatalf("expected forget→unlock→forget (3 calls), got %d: %v", len(seq), seq)
	}
	if !hasArg(strings.Fields(seq[1]), "unlock") {
		t.Fatalf("2nd call must be `restic unlock`, got %q", seq[1])
	}
	// Never --remove-all: that would drop a live backup's lock.
	if hasArg(strings.Fields(seq[1]), "--remove-all") {
		t.Fatalf("unlock must NOT use --remove-all: %q", seq[1])
	}
}

func TestRetention_NoLockRunsOnceNoUnlock(t *testing.T) {
	orig := retentionExec
	t.Cleanup(func() { retentionExec = orig })

	var seq []string
	retentionExec = func(_ context.Context, _ []string, _, _ io.Writer, _ string, args ...string) error {
		seq = append(seq, strings.Join(args, " "))
		return nil
	}
	if err := runResticWithLockRecovery(context.Background(), newRetentionTestCmd(), testDest(),
		[]string{"--repo", "/repo", "prune"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(seq) != 1 {
		t.Fatalf("clean run must be a single call, got %d: %v", len(seq), seq)
	}
	if hasArg(strings.Fields(seq[0]), "unlock") {
		t.Fatalf("must not unlock when there is no lock error")
	}
}

func TestRetention_NonLockErrorDoesNotRetry(t *testing.T) {
	orig := retentionExec
	t.Cleanup(func() { retentionExec = orig })

	calls := 0
	retentionExec = func(_ context.Context, _ []string, _, stderr io.Writer, _ string, args ...string) error {
		calls++
		io.WriteString(stderr, "Fatal: wrong password or no key found\n")
		return errors.New("exit status 1")
	}
	err := runResticWithLockRecovery(context.Background(), newRetentionTestCmd(), testDest(),
		[]string{"--repo", "/repo", "forget"})
	if err == nil {
		t.Fatalf("expected the original error to surface")
	}
	if calls != 1 {
		t.Fatalf("a non-lock error must NOT trigger unlock+retry, got %d calls", calls)
	}
}

func TestRetention_UnlockFailureSurfaces(t *testing.T) {
	orig := retentionExec
	t.Cleanup(func() { retentionExec = orig })

	retentionExec = func(_ context.Context, _ []string, _, stderr io.Writer, _ string, args ...string) error {
		if hasArg(args, "unlock") {
			return errors.New("unlock exit status 1")
		}
		io.WriteString(stderr, "repository is already locked\n")
		return errors.New("exit status 11")
	}
	err := runResticWithLockRecovery(context.Background(), newRetentionTestCmd(), testDest(),
		[]string{"--repo", "/repo", "forget"})
	if err == nil || !strings.Contains(err.Error(), "unlock") {
		t.Fatalf("unlock failure should surface, got %v", err)
	}
}

func TestRetention_CatalogHasRetentionFailEvent(t *testing.T) {
	var meta *models.NotificationEventKindMeta
	for i := range models.AllNotificationEventKinds {
		if models.AllNotificationEventKinds[i].Kind == "backup.retention.fail" {
			meta = &models.AllNotificationEventKinds[i]
			break
		}
	}
	if meta == nil {
		t.Fatalf("backup.retention.fail missing from the notification event catalog")
	}
	if meta.Severity != "error" || !meta.DefaultOn {
		t.Fatalf("backup.retention.fail should be severity=error DefaultOn=true, got %q on=%v", meta.Severity, meta.DefaultOn)
	}
}
