package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GH #2077: a destination on an SFTP-only account (a nologin shell, a storage
// box, a NAS user without shell access) failed Test with remote_mkdir_failed,
// even when its folder already existed. Before restic init the agent ran
// `ssh user@host -- mkdir -p <path>`, a shell command such an account refuses
// ("This account is currently not available."). restic init creates the
// repository folder and its parents over SFTP itself, so the agent no longer
// runs a shell command first.

// sftpOnlyHost puts fake restic, ssh and sshpass on PATH: restic finds no
// repository until init runs, and ssh/sshpass refuse every command, as an
// SFTP-only account does. It returns the log of the commands run.
func sftpOnlyHost(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	logf := filepath.Join(bin, "calls.log")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("restic", `echo "restic $*" >> "`+logf+`"
case " $* " in
  *" init "*) echo "created restic repository"; exit 0 ;;
  *) echo "Fatal: unable to open config file: Stat: file does not exist" >&2; exit 1 ;;
esac
`)
	refuse := `echo "$(basename "$0") $*" >> "` + logf + `"
echo "This account is currently not available." >&2
exit 1
`
	write("ssh", refuse)
	write("sshpass", refuse)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	prev := repoInitLockDir
	repoInitLockDir = t.TempDir()
	t.Cleanup(func() { repoInitLockDir = prev })
	return logf
}

func readCalls(t *testing.T, logf string) string {
	t.Helper()
	b, err := os.ReadFile(logf)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

// ranOverSSH reports a command run with ssh itself, not restic's SFTP
// session (whose sftp.command option also names ssh).
func ranOverSSH(calls string) bool {
	for _, line := range strings.Split(calls, "\n") {
		if strings.HasPrefix(line, "ssh ") || strings.HasPrefix(line, "sshpass ") {
			return true
		}
	}
	return false
}

var sftpOnlyInputs = &backupSFTPInputs{Host: "backup.example", User: "u", Path: "/srv/jabali", Auth: "password"}

func TestBackupDestTest_SFTPOnlyAccountInitsWithoutAShellCommand(t *testing.T) {
	logf := sftpOnlyHost(t)
	raw, _ := json.Marshal(backupDestTestParams{URL: "sftp:u@backup.example:/srv/jabali", SFTP: sftpOnlyInputs})

	out, err := backupDestTestHandler(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	res := out.(backupDestTestResult)
	calls := readCalls(t, logf)
	if res.Status != "ok" || !strings.Contains(res.StdoutPreview, "initialized") {
		t.Fatalf("result %+v, want the repository initialized; commands run:\n%s", res, calls)
	}
	if ranOverSSH(calls) {
		t.Fatalf("ran a shell command over ssh before init:\n%s", calls)
	}
	if !strings.Contains(calls, " init") {
		t.Fatalf("restic init didn't run:\n%s", calls)
	}
}

func TestEnsureRepoReady_SFTPOnlyAccountInitsWithoutAShellCommand(t *testing.T) {
	logf := sftpOnlyHost(t)

	err := bkEnsureRepoReady(context.Background(), "sftp:u@backup.example:/srv/jabali", "", "sftp", filepath.Join(t.TempDir(), "pw"), sftpOnlyInputs)
	calls := readCalls(t, logf)
	if err != nil {
		t.Fatalf("bkEnsureRepoReady: %v; commands run:\n%s", err, calls)
	}
	if ranOverSSH(calls) {
		t.Fatalf("ran a shell command over ssh before init:\n%s", calls)
	}
	if !strings.Contains(calls, " init") {
		t.Fatalf("restic init didn't run:\n%s", calls)
	}
}
