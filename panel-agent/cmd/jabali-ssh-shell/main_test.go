package main

import (
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestFilterColonFile_PasswdHidesOtherTenants(t *testing.T) {
	tmp := t.TempDir() + "/passwd"
	content := strings.Join([]string{
		"root:x:0:0:root:/root:/bin/bash",
		"www-data:x:33:33:www-data:/var/www:/usr/sbin/nologin",
		"alice:x:1001:1001::/home/alice:/usr/local/bin/jabali-ssh-shell",
		"bob:x:1002:1002::/home/bob:/usr/local/bin/jabali-ssh-shell",
	}, "\n") + "\n"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := filterColonFile(tmp, func(f []string) bool {
		if len(f) < 3 {
			return false
		}
		if f[0] == "alice" {
			return true
		}
		uid, err := strconv.Atoi(f[2])
		return err == nil && uid < 1000
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "root:") || !strings.Contains(s, "www-data:") {
		t.Errorf("system users dropped: %q", s)
	}
	if !strings.Contains(s, "alice:") {
		t.Errorf("connecting user missing: %q", s)
	}
	if strings.Contains(s, "bob:") {
		t.Errorf("other tenant leaked: %q", s)
	}
}

func TestBindDataFD_DeliversContentThenEOF(t *testing.T) {
	want := []byte("alice:x:1001:1001::/home/alice:/bin/sh\n")
	fd, f, err := bindDataFD(want)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if fd != f.Fd() {
		t.Errorf("fd mismatch: %d vs %d", fd, f.Fd())
	}
	// FD_CLOEXEC must be cleared so the fd survives exec.
	flags, _, errno := syscallFcntlGetFD(f.Fd())
	if errno != 0 {
		t.Fatalf("fcntl getfd: %v", errno)
	}
	if flags&1 != 0 { // FD_CLOEXEC == 1
		t.Errorf("FD_CLOEXEC still set (%d) — fd would not survive exec", flags)
	}
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("read back %q, want %q", got, want)
	}
}

func TestBuildBwrapArgv_ForwardsCommandAndIsolates(t *testing.T) {
	// interactive
	argv := buildBwrapArgv("alice", "/home/alice", 7, 8, "/bin/bash", nil)
	joined := strings.Join(argv, " ")
	for _, must := range []string{
		"--bind /home/alice /home/alice",
		"--unshare-all", "--share-net",
		"--ro-bind-data 7 /etc/passwd", "--ro-bind-data 8 /etc/group",
		"--ro-bind-try /etc/php /etc/php", // GH #277: CLI php.ini + conf.d extensions
		"--ro-bind-try /etc/jabali/snuffleupagus /etc/jabali/snuffleupagus",
		"--tmpfs /run", "--proc /proc",
		"--ro-bind-try /run/mysqld/mysqld.sock /run/mysqld/mysqld.sock", // GH #285: shell DB access
	} {
		if !strings.Contains(joined, must) {
			t.Errorf("argv missing %q in: %s", must, joined)
		}
	}
	if argv[len(argv)-2] != "/bin/bash" || argv[len(argv)-1] != "-l" {
		t.Errorf("interactive should end in `bash -l`, got %v", argv[len(argv)-2:])
	}
	// command mode (scp/git/ssh host cmd)
	argv2 := buildBwrapArgv("alice", "/home/alice", 7, 8, "/bin/bash", []string{"-c", "scp -t /home/alice"})
	tail := argv2[len(argv2)-3:]
	if tail[0] != "/bin/bash" || tail[1] != "-c" || tail[2] != "scp -t /home/alice" {
		t.Errorf("command mode should forward `-c`, got %v", tail)
	}
}

// TestRootShellArgv guards GH #658: root's real login shell must preserve
// sshd's invocation — a leading-dash argv0 for an interactive login, and the
// passed args (e.g. `-c "cmd"`) for a remote command.
func TestRootShellArgv(t *testing.T) {
	if got := rootShellArgv("bash", []string{"-jabali-ssh-shell"}); len(got) != 1 || got[0] != "-bash" {
		t.Fatalf("interactive: got %v, want [-bash]", got)
	}
	got := rootShellArgv("bash", []string{"jabali-ssh-shell", "-c", "id"})
	if len(got) != 3 || got[0] != "bash" || got[1] != "-c" || got[2] != "id" {
		t.Fatalf("command: got %v, want [bash -c id]", got)
	}
}
