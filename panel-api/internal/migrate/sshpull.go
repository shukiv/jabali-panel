package migrate

import (
	"context"
	"fmt"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/hostreserve"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

// PullFileViaSSH streams a remote absolute path to a local absolute
// path via SSH exec `cat`. Shared across cpanel / directadmin /
// hestiacp importers — io.Copy + an SSH session pipe avoids
// buffering the whole tarball in memory.
//
// Cancellation propagates to the remote via SIGKILL on the SSH
// session; a cancelled job doesn't leak a stranded cat on the
// source.
//
// Caller owns the *ssh.Client lifetime. localPath's parent is
// MkdirAll'd at 0750.
// PullFileViaSSH streams with only the host-reserve guard (JAB-240):
// operator-driven panel migrations carry no per-job budget, but the host
// floor still holds.
func PullFileViaSSH(ctx context.Context, client *ssh.Client, remotePath, localPath string) (int64, error) {
	return PullFileViaSSHBudget(ctx, client, remotePath, localPath, nil)
}

// PullFileViaSSHBudget is PullFileViaSSH drawing from a job byte budget
// (nil = reserve guard only).
func PullFileViaSSHBudget(ctx context.Context, client *ssh.Client, remotePath, localPath string, budget *hostreserve.Budget) (int64, error) {
	if client == nil {
		return 0, fmt.Errorf("PullFileViaSSH: client nil")
	}
	if !filepath.IsAbs(remotePath) {
		return 0, fmt.Errorf("PullFileViaSSH: remote path must be absolute, got %q", remotePath)
	}
	if !filepath.IsAbs(localPath) {
		return 0, fmt.Errorf("PullFileViaSSH: local path must be absolute, got %q", localPath)
	}
	if err := os.MkdirAll(filepath.Dir(localPath), 0o750); err != nil {
		return 0, fmt.Errorf("mkdir local: %w", err)
	}
	w, err := os.OpenFile(localPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return 0, fmt.Errorf("open local: %w", err)
	}
	defer w.Close()

	sess, err := client.NewSession()
	if err != nil {
		return 0, fmt.Errorf("ssh new session: %w", err)
	}
	defer sess.Close()

	stdout, err := sess.StdoutPipe()
	if err != nil {
		return 0, fmt.Errorf("ssh stdout pipe: %w", err)
	}
	cmd := fmt.Sprintf("cat '%s'", shellEscapeForCat(remotePath))
	if err := sess.Start(cmd); err != nil {
		return 0, fmt.Errorf("ssh start cat: %w", err)
	}

	done := make(chan error, 1)
	var copied int64
	go func() {
		n, err := io.Copy(budget.Writer(w, filepath.Dir(localPath)), stdout)
		copied = n
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			_ = sess.Signal(ssh.SIGKILL)
			return copied, fmt.Errorf("pull copy: %w", err)
		}
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGKILL)
		return copied, ctx.Err()
	}
	if err := sess.Wait(); err != nil {
		return copied, fmt.Errorf("ssh cat exit: %w", err)
	}
	return copied, nil
}

// shellEscapeForCat handles single quotes in the path. Caller-side
// validation (filepath.IsAbs + per-importer username/path
// whitelisting) is the primary gate; this is the belt-and-braces
// fallback for the single-quote interpolation we do throughout
// the importer packages.
func shellEscapeForCat(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '\'' {
			out = append(out, []rune(`'\''`)...)
			continue
		}
		out = append(out, r)
	}
	return string(out)
}
