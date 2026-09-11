package commands

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// JAB-405: the manual "test connection" path is the second door onto the same
// restic probe as a real backup run. Before the split it returned the raw restic
// error for anything but a missing repo, so an operator who hit "Test" on a
// dual-init-corrupted destination saw "config or key <id> is damaged: ciphertext
// verification failed" — the same unactionable dump that made GH #454 read as
// "backups broken". backupDestTestFailureResult routes a repo-exists-but-unopenable
// failure through the shared classifier + message so both doors give the same
// actionable Detail. The raw stderr is always preserved in Stderr.

func TestBackupDestTestFailureResult(t *testing.T) {
	const (
		url = "sftp:puzzle@host:/home/puzzle/jabali"
		pw  = "/etc/jabali-panel/restic-repo.password"
	)

	t.Run("key/config mismatch gets the race message", func(t *testing.T) {
		stderr := "Fatal: config or key abcd is damaged: ciphertext verification failed"
		res := backupDestTestFailureResult(url, pw, stderr, errors.New("snapshots: exit status 1"), nil, nil)
		if res.Status != "error" {
			t.Fatalf("status = %q, want error", res.Status)
		}
		// Detail must be the actionable mismatch message, NOT the raw probe error.
		if !strings.Contains(res.Detail, "password is CORRECT") ||
			!strings.Contains(res.Detail, "MORE THAN ONE") {
			t.Errorf("Detail is not the mismatch message: %q", res.Detail)
		}
		if strings.Contains(res.Detail, "exit status 1") {
			t.Errorf("Detail leaked the raw probe error instead of the actionable message: %q", res.Detail)
		}
		if res.Stderr != stderr {
			t.Errorf("Stderr = %q, want the raw restic stderr %q", res.Stderr, stderr)
		}
	})

	t.Run("wrong password gets the reinstall message", func(t *testing.T) {
		stderr := "Fatal: wrong password or no key found"
		res := backupDestTestFailureResult(url, pw, stderr, errors.New("snapshots: exit status 1"), nil, nil)
		if !strings.Contains(res.Detail, "reinstalled or regenerated") {
			t.Errorf("Detail is not the wrong-password message: %q", res.Detail)
		}
		if strings.Contains(res.Detail, "MORE THAN ONE") {
			t.Errorf("Detail leaked the mismatch recovery into the wrong-password case: %q", res.Detail)
		}
	})

	t.Run("unknown failure surfaces the raw error", func(t *testing.T) {
		stderr := "Fatal: unable to connect: connection refused"
		res := backupDestTestFailureResult(url, pw, stderr, errors.New("snapshots: exit status 1"), nil, nil)
		if res.Detail != "snapshots: exit status 1" {
			t.Errorf("Detail = %q, want the raw probe error for an unclassified failure", res.Detail)
		}
		if res.Stderr != stderr {
			t.Errorf("Stderr = %q, want %q", res.Stderr, stderr)
		}
	})
}

// TestBackupDestTestHandlerRoutesUnopenable pins the wiring: the handler's
// non-missing failure branch must delegate to backupDestTestFailureResult, not
// re-inline a raw `Detail: err.Error()`. The behavioural test above proves the
// helper is correct; this proves the handler actually calls it. A value test
// can't cover the handler itself — it shells out to real restic and reads a
// fixed /etc password file — so the source pin is the guard, same approach as
// TestStageMarshalsForwardPasswordFile.
func TestBackupDestTestHandlerRoutesUnopenable(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("backup_dest_testconn.go")
	if err != nil {
		t.Fatalf("read backup_dest_testconn.go: %v", err)
	}
	body := string(src)

	start := strings.Index(body, "func backupDestTestHandler(")
	if start < 0 {
		t.Fatal("backupDestTestHandler not found — if it was renamed, update this test; " +
			"the rule (route unopenable failures through the shared message) still applies")
	}
	end := strings.Index(body[start+1:], "\nfunc ")
	region := body[start:]
	if end >= 0 {
		region = body[start : start+1+end]
	}

	if !strings.Contains(region, "backupDestTestFailureResult(") {
		t.Error("backupDestTestHandler must route a non-missing probe failure through " +
			"backupDestTestFailureResult so a repo that exists but can't be opened gets the " +
			"actionable message, not a raw restic dump")
	}
	if strings.Contains(region, "Detail: err.Error()") {
		t.Error("backupDestTestHandler still returns a raw `Detail: err.Error()` for a probe " +
			"failure — that is the pre-JAB-405 unactionable path; route it through backupDestTestFailureResult")
	}
}
