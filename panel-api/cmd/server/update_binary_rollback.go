package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// managedBinaries are the executables `jabali update` replaces on disk, whether
// they come from a release tarball or a source build. They are versioned as a
// set — jabali-panel and jabali-agent speak a wire contract to each other — so
// a rollback has to put all of them back, not just the one that ran migrations.
var managedBinaries = []string{
	"/usr/local/bin/jabali-panel",
	"/usr/local/bin/jabali-agent",
	"/usr/local/bin/jabali-ssh-shell",
	"/usr/local/bin/jabali-mailhook",
	"/usr/local/libexec/jabali/jabali-sendmail",
}

// binarySnapshot holds byte copies of the binaries as they were before an
// update swapped them, so a failed `migrate up` can put the host back exactly
// where it started.
//
// Why this exists (JAB-210): the update installs new binaries first and runs
// migrations after. When `migrate up` fails, the schema stays where it was
// while the NEW binary sits on disk, and the next service restart — a reboot,
// a crash, an unrelated deploy — runs code against a schema it does not match.
// On the reported box that state was reached by a different route (an older
// release installed over a newer binary), but the hazard is the ordering
// itself. Restoring the previous binaries makes the failure atomic from the
// operator's point of view: either both the binary and the schema moved, or
// neither did.
type binarySnapshot struct {
	dir   string
	files map[string]string // live path -> snapshot copy
}

// snapshotBinaries copies each existing path into a private temp dir. Paths
// that are not installed on this host are skipped, not an error: jabali-mailhook
// is absent wherever the mail module was never provisioned.
func snapshotBinaries(paths []string) (*binarySnapshot, error) {
	dir, err := os.MkdirTemp("", "jabali-update-rollback-")
	if err != nil {
		return nil, fmt.Errorf("snapshot binaries: %w", err)
	}
	s := &binarySnapshot{dir: dir, files: make(map[string]string, len(paths))}
	for _, live := range paths {
		info, err := os.Stat(live)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			s.cleanup()
			return nil, fmt.Errorf("snapshot %s: %w", live, err)
		}
		snap := filepath.Join(dir, filepath.Base(live))
		if err := copyFileAtomic(live, snap, info.Mode().Perm()); err != nil {
			s.cleanup()
			return nil, fmt.Errorf("snapshot %s: %w", live, err)
		}
		s.files[live] = snap
	}
	return s, nil
}

// restore puts every snapshotted binary back. It attempts all of them even if
// one fails, because a half-restored set is the worst possible outcome and the
// operator needs to know exactly which paths are still wrong.
func (s *binarySnapshot) restore() error {
	if s == nil || len(s.files) == 0 {
		return errors.New("no binary snapshot was taken")
	}
	var failures []error
	for live, snap := range s.files {
		mode := os.FileMode(0o755)
		if info, err := os.Stat(snap); err == nil {
			mode = info.Mode().Perm()
		}
		if err := copyFileAtomic(snap, live, mode); err != nil {
			failures = append(failures, fmt.Errorf("restore %s: %w", live, err))
		}
	}
	return errors.Join(failures...)
}

// cleanup drops the snapshot. Safe on a nil receiver and safe to call twice, so
// callers can defer it unconditionally.
func (s *binarySnapshot) cleanup() {
	if s == nil || s.dir == "" {
		return
	}
	_ = os.RemoveAll(s.dir)
	s.dir = ""
	s.files = nil
}

// copyFileAtomic writes src to dst via a sibling temp file + rename, so a
// reader never observes a partially-written executable and a crash mid-copy
// cannot leave a truncated binary in place. No chown: the caller runs as root,
// and root-created files are already root-owned.
func copyFileAtomic(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp := dst + ".rollback-tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer func() {
		out.Close()
		_ = os.Remove(tmp) // no-op once Rename succeeded
	}()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Chmod(mode); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// rollbackAfterFailedMigrate restores the pre-update binaries and composes the
// error the operator sees. The migration failure is the headline — the rollback
// is the reassurance — and a rollback that itself failed has to be louder than
// either, because that is the one state needing hands on the box.
func rollbackAfterFailedMigrate(snap *binarySnapshot, migrateErr error, logf func(string, ...any)) error {
	if rErr := snap.restore(); rErr != nil {
		return fmt.Errorf(
			"migrations failed AND the previous binaries could not be restored: %w\n"+
				"  migrate error: %v\n"+
				"  This host now has NEW binaries on disk against an UNMIGRATED schema. Do not\n"+
				"  restart jabali-panel or jabali-agent until it is resolved — re-run\n"+
				"  `jabali update` once the migration cause is fixed, or reinstall the previous\n"+
				"  release explicitly.", rErr, migrateErr)
	}
	logf("restored the previous binaries — this host is back on the build it was running before this update")
	return fmt.Errorf(
		"migrations failed, so the binary swap was rolled back: %w\n"+
			"  The previous binaries are back in place and the schema is untouched, so the host\n"+
			"  is consistent and safe to restart. Fix the migration cause and re-run\n"+
			"  `jabali update`.", migrateErr)
}
