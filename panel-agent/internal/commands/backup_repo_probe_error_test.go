package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GH #2245: a destination that can't be reached failed with restic's stderr
// in the job's error twice, the second copy lowercased, because the probe
// error already carries it. The job row (and the panel) shows it once.
func TestEnsureRepoReady_UnreachableDestinationNamesStderrOnce(t *testing.T) {
	bin := t.TempDir()
	const stderr = "subprocess ssh: ssh: connect to host 203.0.113.1 port 22: Connection timed out"
	script := "#!/bin/sh\necho '" + stderr + "' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "restic"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	prev := repoInitLockDir
	repoInitLockDir = t.TempDir()
	t.Cleanup(func() { repoInitLockDir = prev })
	pw := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(pw, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := bkEnsureRepoReady(context.Background(), "sftp:u@203.0.113.1:/r", "", "sftp", pw, nil)
	if err == nil {
		t.Fatal("expected an error for an unreachable destination")
	}
	if n := strings.Count(strings.ToLower(err.Error()), strings.ToLower(stderr)); n != 1 {
		t.Fatalf("stderr appears %d times in %q, want once", n, err.Error())
	}
}
