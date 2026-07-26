// jabali-ssh-shell — every hosting user's login shell (M13).
//
// Reads /etc/jabali/ssh-sandbox-mode (single line: 'bubblewrap' or
// 'nspawn'), dispatches into the matching sandbox, and on any
// config error falls back to /usr/sbin/nologin. Never produces
// an unsandboxed bash. Failure mode = "user can't shell in";
// they SFTP instead.
//
// Plan invariants (plans/m13-ssh-shell-sandbox.md §0):
//
//	#1: Two modes only — bubblewrap (default) + nspawn (opt-in).
//	    No 'none' / plain bash mode.
//	#2: Default mode = bubblewrap (lightweight, no rootfs cost).
//	#4: Fallback on bad config = nologin. Never bash.
//	#8: bwrap relies on its setuid bit; no sudo needed.
//	#9: Mode toggle is a single-file write.
//
// Step 1 ships the dispatch skeleton + nologin fallback. bwrap
// argv assembly + nspawn privilege bridge ship in Step 2 +
// Step 3 follow-ups; today's binary always lands in nologin
// because mode_dispatch returns an explicit "step-1-not-yet-
// wired" error so an operator running the wrapper before the
// sandbox bits ship gets a clear signal rather than silent
// passthrough.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const (
	modeFile = "/etc/jabali/ssh-sandbox-mode"
	nologin  = "/usr/sbin/nologin"

	modeBubblewrap = "bubblewrap"
	modeNspawn     = "nspawn"
)

func main() {
	if err := run(); err != nil {
		// Best-effort surface a one-liner so SSH client sees a
		// clean rejection. nologin's own message lands when we
		// exec to it; this stderr line is for the cases where
		// nologin itself isn't found (defensive — every Debian/
		// Ubuntu host ships /usr/sbin/nologin).
		fmt.Fprintf(os.Stderr, "jabali-ssh-shell: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// GH #658: uid 0 (root) must never be trapped in the tenant sandbox. If
	// this wrapper is ever root's login shell — e.g. a mis-created "root"
	// panel user (GH #429) chsh'd root here — hand root a real login shell
	// instead of the sandbox/nologin path, so a chsh mistake can't lock the
	// operator out of SSH. The sandbox is only ever for hosting tenants.
	if os.Getuid() == 0 {
		return execRootLoginShell()
	}
	mode, err := readMode()
	if err != nil {
		return execNologin(fmt.Sprintf("read mode: %v", err))
	}
	switch mode {
	case modeBubblewrap:
		return dispatchBubblewrap()
	case modeNspawn:
		return dispatchNspawn()
	default:
		return execNologin(fmt.Sprintf("unrecognised mode %q in %s (allowed: %s, %s)",
			mode, modeFile, modeBubblewrap, modeNspawn))
	}
}

// readMode pulls the single-line mode from /etc/jabali/ssh-sandbox-mode.
// Missing file → defaults to bubblewrap (plan §0 #2). Empty file or
// trim-only-whitespace → bubblewrap. Other content → returned
// verbatim for the dispatch switch to recognize / reject.
func readMode() (string, error) {
	raw, err := os.ReadFile(modeFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return modeBubblewrap, nil
		}
		return "", err
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return modeBubblewrap, nil
	}
	return trimmed, nil
}

// dispatchBubblewrap exec's /usr/bin/bwrap with the per-user jail
// (M13 Step 2). The sandbox binds a read-only system (/usr + the
// usr-merged symlinks), a filtered /etc/passwd + /etc/group (system
// users + the connecting user only — no tenant enumeration), the
// user's own /home/<user> rw, fresh tmpfs at /tmp /run /var, an
// isolated PID namespace, and shared networking (egress stays open for
// git/composer/wp-cli). Hidden by omission: /etc/shadow, /etc/jabali,
// /etc/ssh, every other tenant's /home, and every host Unix socket
// (/run is a fresh tmpfs). On any setup error we fall back to nologin —
// never an unsandboxed shell.
func dispatchBubblewrap() error {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return execNologin(fmt.Sprintf("bwrap not found in PATH (apt install bubblewrap): %v", err))
	}

	name, _, _, home, err := currentUser()
	if err != nil {
		return execNologin(fmt.Sprintf("resolve current user: %v", err))
	}
	// Defense-in-depth: this wrapper is only ever a hosting user's login
	// shell, whose home is /home/<user>. Refuse to bind anything else —
	// a non-/home home (root, a system account) must never reach a shell
	// through this path.
	if home != "/home/"+name {
		return execNologin(fmt.Sprintf("refusing sandbox: user %q home %q is not /home/%s", name, home, name))
	}
	if fi, err := os.Stat(home); err != nil || !fi.IsDir() {
		return execNologin(fmt.Sprintf("home %q missing or not a directory", home))
	}

	passwdData, err := filterPasswd(name)
	if err != nil {
		return execNologin(fmt.Sprintf("build filtered passwd: %v", err))
	}
	groupData, err := filterGroup(name)
	if err != nil {
		return execNologin(fmt.Sprintf("build filtered group: %v", err))
	}

	// Feed the filtered files to bwrap over pipe FDs (--ro-bind-data) so
	// nothing touches the host filesystem. The read ends must outlive
	// syscall.Exec, so keep the *os.File refs alive (keepAlive) until the
	// exec replaces us, and clear FD_CLOEXEC so the fds survive it.
	passwdFD, f1, err := bindDataFD(passwdData)
	if err != nil {
		return execNologin(fmt.Sprintf("passwd bind-data fd: %v", err))
	}
	groupFD, f2, err := bindDataFD(groupData)
	if err != nil {
		return execNologin(fmt.Sprintf("group bind-data fd: %v", err))
	}
	keepAlive := []*os.File{f1, f2}

	shell := pickShell()
	argv := buildBwrapArgv(name, home, passwdFD, groupFD, shell, os.Args[1:])

	err = syscall.Exec(bwrap, argv, sandboxBwrapEnv())
	// Exec only returns on failure. Reference keepAlive past the exec so
	// the compiler/GC can't close the pipe fds before bwrap reads them.
	runtimeKeepAlive(keepAlive)
	return execNologin(fmt.Sprintf("exec bwrap: %v", err))
}

// buildBwrapArgv assembles the bwrap command line. When the wrapper was
// invoked as `jabali-ssh-shell -c "<cmd>"` (sshd's non-interactive form,
// used by scp/rsync/git-over-ssh and `ssh host cmd`), the command is
// forwarded into the sandbox as `<shell> -c "<cmd>"`. Otherwise a login
// shell. invokedArgs is os.Args[1:].
func buildBwrapArgv(name, home string, passwdFD, groupFD uintptr, shell string, invokedArgs []string) []string {
	argv := []string{
		"bwrap",
		"--ro-bind", "/usr", "/usr",
		// usr-merged layout (Debian 12+/Ubuntu): /bin /sbin /lib /lib64
		// are symlinks into /usr. Recreate them so the dynamic linker
		// and #!-shebangs resolve.
		"--symlink", "usr/bin", "/bin",
		"--symlink", "usr/sbin", "/sbin",
		"--symlink", "usr/lib", "/lib",
		"--symlink", "usr/lib64", "/lib64",
		// Minimal read-only system config a shell needs.
		"--ro-bind-try", "/etc/ssl", "/etc/ssl",
		"--ro-bind-try", "/etc/ca-certificates", "/etc/ca-certificates",
		"--ro-bind-try", "/etc/ca-certificates.conf", "/etc/ca-certificates.conf",
		"--ro-bind-try", "/etc/resolv.conf", "/etc/resolv.conf",
		"--ro-bind-try", "/etc/hosts", "/etc/hosts",
		"--ro-bind-try", "/etc/hostname", "/etc/hostname",
		"--ro-bind-try", "/etc/nsswitch.conf", "/etc/nsswitch.conf",
		"--ro-bind-try", "/etc/localtime", "/etc/localtime",
		"--ro-bind-try", "/etc/profile", "/etc/profile",
		"--ro-bind-try", "/etc/profile.d", "/etc/profile.d",
		"--ro-bind-try", "/etc/bash.bashrc", "/etc/bash.bashrc",
		"--ro-bind-try", "/etc/alternatives", "/etc/alternatives",
		// /etc/php carries the CLI php.ini + conf.d extension config for every
		// installed version. Without it the jailed `php` (and Composer / wp-cli)
		// loads ZERO conf.d extensions — the binary + .so files live under the
		// already-bound /usr, but PHP can't find its config, so a tenant sees a
		// minimal module set while root sees the full one (GH #277). Read-only;
		// php config holds no tenant secrets.
		"--ro-bind-try", "/etc/php", "/etc/php",
		// /opt/wp-cli holds the wp-cli phar; /usr/local/bin/wp (on PATH, under
		// the already-bound /usr) is a symlink into it. Without this bind the
		// symlink dangles inside the jail and `wp` is "command not found" even
		// though the host link is healthy — the tenant-visible half of GH #298.
		// Read-only; the phar is not a tenant secret.
		"--ro-bind-try", "/opt/wp-cli", "/opt/wp-cli",
		// Snuffleupagus (loaded by the php conf.d above) hard-fails if it can't
		// read its ruleset, so bind ONLY that subdir of the otherwise-hidden
		// /etc/jabali (bwrap auto-creates the /etc/jabali parent empty — panel.env
		// and the rest stay invisible). The rules aren't tenant secrets. Without
		// this, binding /etc/php would make CLI php FATAL instead of work (#277).
		"--ro-bind-try", "/etc/jabali/snuffleupagus", "/etc/jabali/snuffleupagus",
		"--ro-bind-data", strconv.Itoa(int(passwdFD)), "/etc/passwd",
		"--ro-bind-data", strconv.Itoa(int(groupFD)), "/etc/group",
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		"--tmpfs", "/run",
		"--tmpfs", "/var",
		// Mirror the host's /var/run -> /run symlink (GH #333). PHP's
		// mysqli/pdo_mysql default_socket is /var/run/mysqld/mysqld.sock on
		// Debian; without this symlink the bound /run/mysqld/mysqld.sock is
		// unreachable via that path inside the fresh /var tmpfs, so CLI PHP
		// (cron scripts, `php app.php`) fails mysqli_connect with "No such file
		// or directory" even though the same app works under FPM on the web.
		"--symlink", "/run", "/var/run",
		// Expose ONLY the MariaDB socket on the fresh /run tmpfs so a tenant can
		// run `mysql -u <dbuser> -p <db>` / import a .sql from the shell (GH #285).
		// The socket is world-connectable but MariaDB auth gates access, so a
		// tenant still needs valid DB credentials and only reaches DBs their user
		// is granted — same path phpMyAdmin uses. Nothing else under /run leaks.
		"--ro-bind-try", "/run/mysqld/mysqld.sock", "/run/mysqld/mysqld.sock",
		"--bind", home, home,
		"--chdir", home,
		// Isolate everything, then re-share only the network so egress
		// (git/composer/wp-cli) keeps working. PID-namespace isolation
		// is what hides other tenants' processes.
		"--unshare-all",
		"--share-net",
		"--die-with-parent",
		// Build the sandbox env explicitly — don't leak the host's.
		"--clearenv",
		"--setenv", "HOME", home,
		"--setenv", "USER", name,
		"--setenv", "LOGNAME", name,
		"--setenv", "SHELL", shell,
		// Per-user CLI php wrapper first so `php`/Composer/wp-cli use the
		// user's pinned PHP version (GH #184), then the normal dirs.
		"--setenv", "PATH", home + "/.jabali/bin:/usr/local/bin:/usr/bin:/bin",
		"--setenv", "TERM", envOr("TERM", "xterm"),
	}
	if lang := os.Getenv("LANG"); lang != "" {
		argv = append(argv, "--setenv", "LANG", lang)
	}
	argv = append(argv, "--")
	// Forward an SSH-supplied command, else an interactive login shell.
	if len(invokedArgs) >= 2 && invokedArgs[0] == "-c" {
		argv = append(argv, shell, "-c", invokedArgs[1])
	} else {
		argv = append(argv, shell, "-l")
	}
	return argv
}

// filterPasswd returns /etc/passwd reduced to system accounts (uid <
// 1000) plus the connecting user's own row. Hides other tenants from
// enumeration inside the sandbox while keeping `id`, prompt expansion,
// and tool uid->name lookups working.
func filterPasswd(username string) ([]byte, error) {
	return filterColonFile("/etc/passwd", func(fields []string) bool {
		if len(fields) < 3 {
			return false
		}
		if fields[0] == username {
			return true
		}
		uid, err := strconv.Atoi(fields[2])
		return err == nil && uid < 1000
	})
}

// filterGroup returns /etc/group reduced to system groups (gid < 1000)
// plus the user's own group, www-data (the SSH-mode home group), and any
// group listing the user as a member.
func filterGroup(username string) ([]byte, error) {
	return filterColonFile("/etc/group", func(fields []string) bool {
		if len(fields) < 4 {
			return false
		}
		if fields[0] == username || fields[0] == "www-data" {
			return true
		}
		if gid, err := strconv.Atoi(fields[2]); err == nil && gid < 1000 {
			return true
		}
		for _, m := range strings.Split(fields[3], ",") {
			if m == username {
				return true
			}
		}
		return false
	})
}

// filterColonFile reads a colon-delimited file (passwd/group) and keeps
// only the lines whose split fields satisfy keep. Preserves order and a
// trailing newline.
func filterColonFile(path string, keep func(fields []string) bool) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		if keep(strings.Split(line, ":")) {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return []byte(b.String()), nil
}

// bindDataFD writes content into a pipe, closes the write end (so bwrap's
// --ro-bind-data read sees EOF), clears FD_CLOEXEC on the read end so it
// survives syscall.Exec, and returns the read-end fd. The caller MUST
// keep the returned *os.File alive until after exec — otherwise the GC
// finalizer closes the fd. passwd/group are far below the 64 KiB pipe
// buffer, so the single Write never blocks.
func bindDataFD(content []byte) (uintptr, *os.File, error) {
	if len(content) > 60000 {
		return 0, nil, fmt.Errorf("bind-data content too large (%d bytes)", len(content))
	}
	r, w, err := os.Pipe()
	if err != nil {
		return 0, nil, err
	}
	if _, err := w.Write(content); err != nil {
		r.Close()
		w.Close()
		return 0, nil, err
	}
	if err := w.Close(); err != nil {
		r.Close()
		return 0, nil, err
	}
	fd := r.Fd()
	if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_SETFD, 0); errno != 0 {
		r.Close()
		return 0, nil, fmt.Errorf("clear cloexec: %v", errno)
	}
	return fd, r, nil
}

// pickShell returns the interactive shell to run inside the sandbox.
// The user's /etc/passwd shell is this wrapper, so it can't be the
// source; prefer bash, fall back to sh.
func pickShell() string {
	for _, sh := range []string{"/bin/bash", "/usr/bin/bash", "/bin/sh"} {
		if fi, err := os.Stat(sh); err == nil && !fi.IsDir() {
			return sh
		}
	}
	return "/bin/sh"
}

// sandboxBwrapEnv is the env handed to the bwrap PROCESS itself (not the
// sandboxed shell — that one is built via --clearenv/--setenv). Kept
// minimal so bwrap inherits nothing sensitive.
func sandboxBwrapEnv() []string {
	return []string{"PATH=/usr/bin:/bin"}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// runtimeKeepAlive is a no-op that references the pipe files so the
// compiler can't consider them dead before syscall.Exec runs.
func runtimeKeepAlive(files []*os.File) {
	for _, f := range files {
		if f != nil {
			_ = f.Fd()
		}
	}
}

// dispatchNspawn will read /etc/jabali/users/<username>/nspawn-
// image (per-user image pin) and sudo into the M13-shipped
// jabali-nspawn-enter helper. Step 3 follow-up.
func dispatchNspawn() error {
	if _, err := exec.LookPath("systemd-nspawn"); err != nil {
		return execNologin(fmt.Sprintf("systemd-nspawn not found (apt install systemd-container): %v", err))
	}
	if _, err := exec.LookPath("sudo"); err != nil {
		return execNologin(fmt.Sprintf("sudo not found: %v", err))
	}
	username, err := currentUsername()
	if err != nil {
		return execNologin(fmt.Sprintf("resolve current user: %v", err))
	}
	pinPath := filepath.Join("/etc/jabali/users", username, "nspawn-image")
	pinRaw, err := os.ReadFile(pinPath)
	if err != nil {
		return execNologin(fmt.Sprintf("read image pin %s: %v", pinPath, err))
	}
	image := strings.TrimSpace(string(pinRaw))
	if image == "" {
		return execNologin(fmt.Sprintf("image pin %s is empty", pinPath))
	}
	// TODO(M13 Step 3): sudo /usr/local/bin/jabali-nspawn-enter
	// <image>; the helper validates image against the allowlist
	// scan of /var/lib/jabali-nspawn/images/ + exec's
	// systemd-nspawn --ephemeral --image=<path> --bind=/home/<u>.
	return execNologin("M13 Step 1: nspawn dispatch not yet wired (Step 3 ships sudo bridge)")
}

// currentUser resolves the connecting identity from the real uid via
// /etc/passwd — deliberately NOT from $USER. The home-dir bind is keyed
// off this name, so trusting a client-settable env var could let one
// tenant bind another's home. sshd doesn't normally let the client set
// $USER, but the sandbox must not depend on that.
func currentUser() (name string, uid, gid int, home string, err error) {
	uid = os.Getuid()
	gid = os.Getgid()
	pw, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return "", uid, gid, "", err
	}
	want := strconv.Itoa(uid)
	for _, line := range strings.Split(string(pw), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) < 6 {
			continue
		}
		if fields[2] == want {
			return fields[0], uid, gid, fields[5], nil
		}
	}
	return "", uid, gid, "", fmt.Errorf("uid %d not in /etc/passwd", uid)
}

// currentUsername returns just the login name (nspawn path). uid-derived
// for the same reason as currentUser — no $USER trust.
func currentUsername() (string, error) {
	name, _, _, _, err := currentUser()
	return name, err
}

// rootShellArgv builds the argv for root's real login shell, preserving
// sshd's invocation: a remote command (`shell -c "cmd"`, len(osArgs) > 1) keeps
// its args under a plain argv0; an interactive login gets a leading-dash argv0.
func rootShellArgv(base string, osArgs []string) []string {
	if len(osArgs) > 1 {
		return append([]string{base}, osArgs[1:]...)
	}
	return []string{"-" + base}
}

// execRootLoginShell replaces the process with a real root login shell
// (GH #658). Prefers /bin/bash, falls back to /bin/sh.
func execRootLoginShell() error {
	shell, base := "/bin/bash", "bash"
	if _, err := os.Stat(shell); err != nil {
		shell, base = "/bin/sh", "sh"
	}
	return syscall.Exec(shell, rootShellArgv(base, os.Args), os.Environ())
}

// execNologin replaces the current process with /usr/sbin/nologin.
// The reason string is logged to stderr so an operator inspecting
// auth.log sees why the wrapper bailed.
//
// Returns nil on successful exec replacement (which doesn't return
// at all). Returns the underlying error when nologin can't be
// invoked (missing binary; rare). Caller's main() prints the
// fallback-of-fallback diagnostic.
func execNologin(reason string) error {
	if reason != "" {
		fmt.Fprintf(os.Stderr, "jabali-ssh-shell: %s — falling back to nologin\n", reason)
	}
	if _, err := os.Stat(nologin); err != nil {
		// Belt-and-braces — write 'no shell' to STDERR (never
		// stdout) so the SSH client sees an explicit message even
		// when the system's nologin is broken. Critically, stderr
		// keeps this OFF the channel's data stream: an SFTP/scp
		// client that ended up running the shell (server's
		// `Subsystem sftp` didn't catch the request, so the client
		// fell back to sftp-over-shell) would otherwise read this
		// text as a binary protocol frame and die with "Received
		// message too long" (GH #211). stderr → the transfer fails
		// cleanly instead.
		_, _ = io.WriteString(os.Stderr, "This account is not configured for shell access.\n")
		return fmt.Errorf("nologin missing at %s: %w", nologin, err)
	}
	return syscall.Exec(nologin, []string{nologin}, os.Environ())
}
