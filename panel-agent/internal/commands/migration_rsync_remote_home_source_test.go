package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// A root session must not read the source as root: root follows a symlink the
// account planted in its own tree. The source-side rsync runs as the owner of
// the account's top directory.
func TestSourceRsyncPathOpt_RootSessionRunsAsAccountOwner(t *testing.T) {
	cases := []struct {
		name, srcRoot string
		override      bool
		srcPath       string
		wantRoot      string
	}{
		{"cpanel mail", "/home/webup", false, "/home/webup/mail", "/home/webup"},
		{"cyberpanel dotted home", "/home/smoke.example.com", false, "/home/smoke.example.com/public_html", "/home/smoke.example.com"},
		{"plesk docroot", "/var/www/vhosts", true, "/var/www/vhosts/example.com/httpdocs", "/var/www/vhosts/example.com"},
		{"plesk vhost root", "/var/www/vhosts", true, "/var/www/vhosts/example.com", "/var/www/vhosts/example.com"},
		{"plesk mail", "/var/qmail/mailnames", true, "/var/qmail/mailnames/example.com/info/Maildir", "/var/qmail/mailnames/example.com"},
	}
	for _, c := range cases {
		for _, sshUser := range []string{"root", ""} {
			t.Run(c.name+"/"+sshUser, func(t *testing.T) {
				opt, msg := sourceRsyncPathOpt(sshUser, c.srcRoot, c.override, c.srcPath)
				if msg != "" {
					t.Fatalf("refused: %s", msg)
				}
				if !strings.HasPrefix(opt, "--rsync-path=") ||
					!strings.Contains(opt, "stat -c %U -- '"+c.wantRoot+"'") ||
					!strings.HasSuffix(opt, `exec runuser -u "$u" -- rsync`) {
					t.Fatalf("option = %q, want runuser as the owner of %s", opt, c.wantRoot)
				}
			})
		}
	}
}

// A non-root login is already the boundary; no --rsync-path, no extra checks.
func TestSourceRsyncPathOpt_NonRootLoginUnchanged(t *testing.T) {
	opt, msg := sourceRsyncPathOpt("webup", "/home/we$up", false, "/home/we$up/mail")
	if opt != "" || msg != "" {
		t.Fatalf("got opt=%q msg=%q, want neither for a non-root login", opt, msg)
	}
}

func TestSourceAccountRoot_Refuses(t *testing.T) {
	cases := []struct {
		name, srcRoot string
		override      bool
		srcPath       string
	}{
		{"override root itself", "/var/www/vhosts", true, "/var/www/vhosts"},
		{"override outside root", "/var/www/vhosts", true, "/var/www/other/x"},
		{"dollar", "/home/a$b", false, "/home/a$b/mail"},
		{"quote", "/home/a'b", false, "/home/a'b/mail"},
		{"space", "/home/a b", false, "/home/a b/mail"},
		{"backtick", "/var/www/vhosts", true, "/var/www/vhosts/a`id`/httpdocs"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if root, msg := sourceAccountRoot(c.srcRoot, c.override, c.srcPath); msg == "" {
				t.Fatalf("accepted %q (root %q), want refused", c.srcPath, root)
			}
		})
	}
}

// The handler must apply the check: a root session over an account name the
// source shell would interpret is refused before anything connects.
func TestRsyncRemoteHome_RootSessionRefusesShellUnsafeAccount(t *testing.T) {
	m := map[string]any{
		"job_id": "j1", "host": "src.example.com", "ssh_user": "root",
		"secret_path": "/etc/jabali-panel/migration-secrets/j1.env",
		"src_account": "a$b", "src_path": "/home/a$b/mail",
		"dest_path": "/home/alice/mail", "dest_user": "alice",
	}
	raw, _ := json.Marshal(m)
	_, err := migrationRsyncRemoteHomeHandler(context.Background(), raw)
	if err == nil || !strings.Contains(err.Error(), "may only use letters") {
		t.Fatalf("got err=%v, want the shell-unsafe account refused", err)
	}
}

// Run the --rsync-path command the way the source's shell would, with a fake
// runuser that prints its arguments.
func runSourceCommand(t *testing.T, acctRoot string) (string, error) {
	return runSourceCommandStat(t, acctRoot, "")
}

// runSourceCommandStat also replaces stat with a script, when statScript is set.
func runSourceCommandStat(t *testing.T, acctRoot, statScript string) (string, error) {
	t.Helper()
	bin := t.TempDir()
	fake := "#!/bin/sh\necho RUNUSER \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "runuser"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	if statScript != "" {
		if err := os.WriteFile(filepath.Join(bin, "stat"), []byte("#!/bin/sh\n"+statScript+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", "-c", sourceRsyncAsOwner(acctRoot)+" --server --sender . /x/")
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestSourceRsyncAsOwner_RunsAsDirectoryOwner(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("needs a non-root owner for the temp dir")
	}
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	out, err := runSourceCommand(t, t.TempDir())
	if err != nil {
		t.Fatalf("command failed: %v: %s", err, out)
	}
	want := "RUNUSER -u " + me.Username + " -- rsync --server --sender . /x/"
	if strings.TrimSpace(out) != want {
		t.Fatalf("got %q, want %q", strings.TrimSpace(out), want)
	}
}

// stat without -L: a symlinked account directory runs as the link's owner,
// never as the owner of what it points at (here root's /etc).
func TestSourceRsyncAsOwner_SymlinkIsNotFollowed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("needs a non-root owner for the link")
	}
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "acct")
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatal(err)
	}
	out, err := runSourceCommand(t, link)
	if err != nil || !strings.HasPrefix(out, "RUNUSER -u "+me.Username+" -- ") {
		t.Fatalf("got err=%v out=%q, want the link's own owner %s", err, out, me.Username)
	}
}

func TestSourceRsyncAsOwner_RefusesRootOrMissing(t *testing.T) {
	for _, dir := range []string{"/etc", "/nonexistent-jabali-test-dir"} {
		t.Run(dir, func(t *testing.T) {
			if fi, err := os.Stat(dir); err == nil {
				// A user-namespace sandbox shows root's files as nobody's.
				if st, ok := fi.Sys().(*syscall.Stat_t); !ok || st.Uid != 0 {
					t.Skipf("%s is not owned by uid 0 here", dir)
				}
			}
			out, err := runSourceCommand(t, dir)
			if err == nil || strings.Contains(out, "RUNUSER") || !strings.Contains(out, "source copy refused") {
				t.Fatalf("got err=%v out=%q, want refused without running rsync", err, out)
			}
		})
	}
}

// The owner check itself, independent of who owns what on the test host.
func TestSourceRsyncAsOwner_RefusesRootUnknownOrNoOwner(t *testing.T) {
	for name, script := range map[string]string{
		"root":     "echo root",
		"unknown":  "echo UNKNOWN",
		"no owner": "exit 1",
	} {
		t.Run(name, func(t *testing.T) {
			out, err := runSourceCommandStat(t, "/home/webup", script)
			if err == nil || strings.Contains(out, "RUNUSER") || !strings.Contains(out, "source copy refused") {
				t.Fatalf("got err=%v out=%q, want refused without running rsync", err, out)
			}
		})
	}
	out, err := runSourceCommandStat(t, "/home/webup", "echo webup")
	if err != nil || strings.TrimSpace(out) != "RUNUSER -u webup -- rsync --server --sender . /x/" {
		t.Fatalf("owner webup: got err=%v out=%q", err, out)
	}
}

func TestRemoteRsyncTail_RsyncPathBeforeSource(t *testing.T) {
	args := remoteRsyncTail([]string{"-aH"}, "--rsync-path=X", "-p 22", "root@h", "/home/a/mail", "/home/b/mail")
	want := []string{"-aH", "--rsync-path=X", "-e", "ssh -p 22", "root@h:/home/a/mail/", "/home/b/mail/"}
	if strings.Join(args, "|") != strings.Join(want, "|") {
		t.Fatalf("args = %q, want %q", args, want)
	}
	args = remoteRsyncTail([]string{"-aH"}, "", "-p 22", "webup@h", "/home/webup/mail/", "/home/b/mail")
	if strings.Contains(strings.Join(args, " "), "rsync-path") {
		t.Fatalf("args = %q, want no --rsync-path for a non-root login", args)
	}
}

// 23/24 leave the rest of the tree copied; anything else is a failure.
func TestRsyncPartialExit(t *testing.T) {
	exitErr := func(code string) error {
		return exec.Command("sh", "-c", "exit "+code).Run()
	}
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"vanished", exitErr("24"), 24},
		{"partial", exitErr("23"), 23},
		{"protocol", exitErr("12"), 0},
		{"timeout", exitErr("30"), 0},
		{"plain error", errors.New("boom"), 0},
		{"nil", nil, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := rsyncPartialExit(c.err); got != c.want {
				t.Fatalf("rsyncPartialExit = %d, want %d", got, c.want)
			}
		})
	}
}

func TestRsyncErrorLines(t *testing.T) {
	out := strings.Join([]string{
		"Number of files: 12 (reg: 10, dir: 2)",
		`file has vanished: "/home/webup/mail/new/1700000000.M1.mx"`,
		`rsync: [sender] send_files failed to open "/home/webup/x": Permission denied (13)`,
		"Total bytes sent: 934",
		"rsync error: some files/attrs were not transferred (see previous errors) (code 23) at main.c(1207) [generator=3.4.1]",
	}, "\n")
	got := rsyncErrorLines(out)
	if len(got) != 3 || !strings.HasPrefix(got[0], "file has vanished") || !strings.Contains(got[2], "code 23") {
		t.Fatalf("lines = %q, want the vanished, rsync: and rsync error: lines only", got)
	}

	many := strings.Repeat("rsync: x\n", 50)
	if got := rsyncErrorLines(many); len(got) != 21 || !strings.Contains(got[20], "cut") {
		t.Fatalf("got %d lines, want 20 plus a cut marker", len(got))
	}
}
