package wordpressssh

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
)

// FileExcludes are the transient/cache paths never worth migrating. Kept as one
// exported list so #648's REST file walk shares the SAME source of truth (they
// must not drift). Paths are relative to the WP root (tar -C <root>).
var FileExcludes = []string{
	"./wp-content/cache",
	"./wp-content/advanced-cache.php",
	"./wp-content/object-cache.php",
	"./wp-content/*.log",
	"./*.log",
	"./wp-content/uploads/cache",
}

// ExportDatabase dumps the source DB to dstSQL (local staging). PRIMARY path is
// `wp db export`, which reads DB credentials from wp-config on the source — the
// password is NEVER in argv/ps/logs. The temp .sql on the source is removed on
// success AND failure. If WP-CLI is absent this returns a clear error; the
// password-safe mysqldump fallback (temp defaults-extra-file on the source) is
// intentionally NOT half-implemented here — it lands as its own reviewed change.
func (s *Session) ExportDatabase(ctx context.Context, root, jobID, dstSQL string) error {
	// Deterministic per-job remote temp path (no user input interpolated).
	remoteSQL := "/tmp/jabali-wp-migrate-" + sanitizeJobID(jobID) + ".sql"
	// Always attempt cleanup of the remote temp, whatever happens below.
	defer func() {
		_, _ = s.run(ctx, defaultCmdTimeout, "rm -f "+shellQuote(remoteSQL))
	}()

	if _, err := s.run(ctx, defaultCmdTimeout, "command -v wp >/dev/null 2>&1 && echo yes"); err != nil {
		return fmt.Errorf("wordpressssh.ExportDatabase: WP-CLI not found on the source — the mysqldump fallback is not yet available; install wp-cli on the source or use a WP-CLI-capable host")
	}
	// wp reads creds from wp-config → no password on the command line.
	dumpCmd := "wp --allow-root --path=" + shellQuote(root) + " --skip-plugins --skip-themes db export " + shellQuote(remoteSQL) +
		" --single-transaction --quick --skip-lock-tables 2>&1"
	if out, err := s.run(ctx, 30*time.Minute, dumpCmd); err != nil {
		return fmt.Errorf("wordpressssh.ExportDatabase: wp db export failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	// Pull the dump to local staging via the proven cat-stream (reuses the
	// authenticated, rebind-safe SSH client).
	if err := os.MkdirAll(filepath.Dir(dstSQL), 0o750); err != nil {
		return fmt.Errorf("mkdir staging: %w", err)
	}
	if _, err := migrate.PullFileViaSSHBudget(ctx, s.client, remoteSQL, dstSQL, s.budget); err != nil {
		return fmt.Errorf("wordpressssh.ExportDatabase: pull dump: %w", err)
	}
	return nil
}

// PullFilesTarball streams a gzip tarball of the WP root (excludes applied at
// tar time) over the reused SSH session into dstTarball (local staging). It does
// NOT extract — S4 (import_wp) extracts with containment (a hostile source tar
// with `..`/symlinks must be extracted safely, which belongs at the write-into-
// docroot boundary, not here).
func (s *Session) PullFilesTarball(ctx context.Context, root, dstTarball string) error {
	if err := os.MkdirAll(filepath.Dir(dstTarball), 0o750); err != nil {
		return fmt.Errorf("mkdir staging: %w", err)
	}
	var excl strings.Builder
	for _, e := range FileExcludes {
		excl.WriteString(" --exclude=" + shellQuote(e))
	}
	// `tar czf - -C <root> <excludes> .` → stdout stream → local file.
	cmd := "tar czf - -C " + shellQuote(root) + excl.String() + " . 2>/dev/null"
	return s.streamToFile(ctx, 60*time.Minute, cmd, dstTarball)
}

// streamToFile runs cmd on the source and writes its stdout to localPath.
func (s *Session) streamToFile(ctx context.Context, timeout time.Duration, cmd, localPath string) error {
	subctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	sess, err := s.client.NewSession()
	if err != nil {
		return fmt.Errorf("ssh new session: %w", err)
	}
	defer sess.Close()
	f, err := os.OpenFile(localPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return fmt.Errorf("open local %s: %w", localPath, err)
	}
	defer f.Close()
	// JAB-240: the remote command controls this stream's length — bound it
	// by the job budget and the host reserve floor. An overrun write-errors,
	// which kills sess.Run with a broken pipe.
	sess.Stdout = s.budget.Writer(f, filepath.Dir(localPath))
	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("ssh stream %q: %w", cmd, err)
		}
		return nil
	case <-subctx.Done():
		return fmt.Errorf("ssh stream %q: timed out after %s", cmd, timeout)
	}
}

// sanitizeJobID keeps only ULID-safe chars so the remote temp path is a fixed,
// injection-proof shape even though jobID is already a server-minted ULID.
func sanitizeJobID(id string) string {
	var b strings.Builder
	for _, r := range id {
		if (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "job"
	}
	return b.String()
}
