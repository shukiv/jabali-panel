package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The JAB-405 key listing and move-aside run `ls` and `mv` over ssh. ssh joins
// the remote command's arguments with spaces and the remote user's shell runs
// that string, so a repository path is shell input on the backup host: a space
// split it, and a `;` ran a second command (seen on a real sshd: the path
// "/x;touch PWNED" created PWNED). A path with any character a shell treats
// specially is now single-quoted; a plain path is sent as before.

// remoteShellHost puts a fake ssh on PATH that does what sshd does with the
// remote command (joins it with spaces and runs it through sh -c), and fake ls
// and mv that log the arguments they receive. It returns the log path and the
// directory a stray command would write to.
func remoteShellHost(t *testing.T) (string, string) {
	t.Helper()
	bin := t.TempDir()
	logf := filepath.Join(bin, "calls.log")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Like ssh: options up to `--`, then the host, then the remote command.
	write("ssh", `while [ $# -gt 0 ] && [ "$1" != "--" ]; do shift; done
shift 2
exec sh -c "$*"
`)
	logArgs := `{ echo "$(basename "$0") argc=$#"; for a in "$@"; do echo "arg=$a"; done; } >> "` + logf + `"
`
	write("ls", logArgs)
	write("mv", logArgs)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logf, t.TempDir()
}

// remotePaths are repository paths that must each reach ls or mv as one
// argument, with nothing else run. stray is where an injected command writes.
func remotePaths(stray string) map[string]string {
	return map[string]string{
		"space and semicolon":  "/srv/back ups;touch " + stray + ";true",
		"no whitespace":        "/srv/backups;touch${IFS}" + stray,
		"command substitution": "/srv/backups$(touch " + stray + ")",
		"single quote":         "/srv/back ups/keys/3c0b'd",
		"plain":                "/home/puzzle/jabali/keys",
	}
}

func TestListRemoteSFTP_PathStaysOneArgumentOnTheRemoteShell(t *testing.T) {
	for name := range remotePaths("") {
		t.Run(name, func(t *testing.T) {
			logf, dir := remoteShellHost(t)
			stray := filepath.Join(dir, "PWNED")
			path := remotePaths(stray)[name]

			_, err := ListRemoteSFTP(context.Background(), SFTPInputs{User: "u", Host: "h"}, path, nil)
			got, _ := os.ReadFile(logf)
			if err != nil {
				t.Fatalf("ListRemoteSFTP: %v; remote ls received:\n%s", err, got)
			}
			if _, err := os.Stat(stray); err == nil {
				t.Fatalf("the path ran a second command on the remote shell; remote ls received:\n%s", got)
			}
			want := "ls argc=2\narg=-1\narg=" + path + "\n"
			if string(got) != want {
				t.Fatalf("remote ls received:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

// A relative path starting with a dash must not reach ls as an option.
func TestListRemoteSFTP_LeadingDashIsNotAnOption(t *testing.T) {
	logf, _ := remoteShellHost(t)

	if _, err := ListRemoteSFTP(context.Background(), SFTPInputs{User: "u", Host: "h"}, "-x/keys", nil); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(logf)
	if want := "ls argc=2\narg=-1\narg=./-x/keys\n"; string(got) != want {
		t.Fatalf("remote ls received:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenameRemoteSFTP_PathsStayOneArgumentEachOnTheRemoteShell(t *testing.T) {
	for name := range remotePaths("") {
		t.Run(name, func(t *testing.T) {
			logf, dir := remoteShellHost(t)
			stray := filepath.Join(dir, "PWNED")
			src := "/srv/back ups/keys/3c0b'd"
			dst := remotePaths(stray)[name]

			_, err := RenameRemoteSFTP(context.Background(), SFTPInputs{User: "u", Host: "h"}, src, dst, nil)
			got, _ := os.ReadFile(logf)
			if err != nil {
				t.Fatalf("RenameRemoteSFTP: %v; remote mv received:\n%s", err, got)
			}
			if _, err := os.Stat(stray); err == nil {
				t.Fatalf("the path ran a second command on the remote shell; remote mv received:\n%s", got)
			}
			want := "mv argc=3\narg=-n\narg=" + src + "\narg=" + dst + "\n"
			if string(got) != want {
				t.Fatalf("remote mv received:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}
