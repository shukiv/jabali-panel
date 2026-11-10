package commands

import (
	"errors"
	"fmt"
	"strings"
)

// sourceRsyncPathOpt returns the --rsync-path option for the copy, or "" when
// the SSH login is not root. A root login runs the source-side rsync as the
// owner of the account's top directory (see sourceRsyncAsOwner), so the copy
// can only read what the account itself can read. The second return value is
// a refusal message.
func sourceRsyncPathOpt(sshUser, srcRoot string, override bool, srcPath string) (string, string) {
	if sshUser != "" && sshUser != "root" {
		return "", ""
	}
	acctRoot, msg := sourceAccountRoot(srcRoot, override, srcPath)
	if msg != "" {
		return "", msg
	}
	return "--rsync-path=" + sourceRsyncAsOwner(acctRoot), ""
}

// sourceAccountRoot returns the account's top directory on the source: the
// /home/<account> root itself, or, for a caller-provided root (Plesk
// /var/www/vhosts, /var/qmail/mailnames), the first directory under it. Its
// parent belongs to root or the mail system, so the account cannot swap it.
// srcRoot and srcPath arrive cleaned and already scoped by the handler.
func sourceAccountRoot(srcRoot string, override bool, srcPath string) (string, string) {
	root := srcRoot
	if override {
		rel, ok := strings.CutPrefix(srcPath, srcRoot+"/")
		first, _, _ := strings.Cut(rel, "/")
		if !ok || first == "" {
			return "", "src_path must name a directory under " + srcRoot
		}
		root = srcRoot + "/" + first
	}
	if !shellSafeSourcePath(root) {
		return "", fmt.Sprintf("source account directory %q may only use letters, digits, '.', '_', '-' and '/'", root)
	}
	return root, ""
}

// shellSafeSourcePath accepts an absolute path made only of characters that
// mean nothing to a shell, since sourceRsyncAsOwner puts it into the command
// root's login shell runs on the source.
func shellSafeSourcePath(s string) bool {
	if !strings.HasPrefix(s, "/") || len(s) > 255 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-', r == '/':
		default:
			return false
		}
	}
	return true
}

// sourceRsyncAsOwner is the --rsync-path command for a root session.
//
// rsync appends its server arguments to this string, and root's login shell
// on the source runs the result, so it must end in "rsync" and acctRoot must
// already be shell-safe. stat runs WITHOUT -L, so it reads the directory's own
// owner, never a symlink target's. An owner of root, or one stat cannot read,
// refuses the copy rather than falling back to a root read.
func sourceRsyncAsOwner(acctRoot string) string {
	return "u=$(stat -c %U -- '" + acctRoot + "' 2>/dev/null); " +
		`case "$u" in ''|root|UNKNOWN) echo "jabali: source copy refused: ` + acctRoot +
		` must be owned by the account, not '$u'" >&2; exit 1;; esac; ` +
		`exec runuser -u "$u" -- rsync`
}

// remoteRsyncTail appends the remote-shell option and the source and
// destination arguments. The --rsync-path option goes in before them.
func remoteRsyncTail(args []string, rsyncPathOpt, sshOpt, userHost, srcPath, destPath string) []string {
	if rsyncPathOpt != "" {
		args = append(args, rsyncPathOpt)
	}
	return append(args,
		"-e", "ssh "+sshOpt,
		userHost+":"+strings.TrimRight(srcPath, "/")+"/",
		strings.TrimRight(destPath, "/")+"/",
	)
}

// rsyncPartialExit returns 23 or 24 when rsync finished with a partial copy,
// and 0 for success or any other failure. 23: some files could not be read
// or transferred (an account-run copy meets files the account cannot read).
// 24: source files vanished during the copy (a live Maildir, a cache).
func rsyncPartialExit(err error) int {
	var ee interface{ ExitCode() int }
	if errors.As(err, &ee) {
		switch c := ee.ExitCode(); c {
		case 23, 24:
			return c
		}
	}
	return 0
}

// rsyncErrorLines keeps rsync's own error and vanished-file lines from its
// output, bounded so a tree full of unreadable files cannot bloat the reply.
func rsyncErrorLines(out string) []string {
	const maxLines, maxLen = 20, 300
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if !strings.HasPrefix(l, "rsync: ") && !strings.HasPrefix(l, "rsync error: ") &&
			!strings.HasPrefix(l, "file has vanished: ") {
			continue
		}
		if len(lines) == maxLines {
			lines = append(lines, "... more rsync lines cut")
			break
		}
		lines = append(lines, truncate(l, maxLen))
	}
	return lines
}
