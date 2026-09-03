// JAB-357 crit-7 — safe rewriting primitives for secret rotation.
//
// Every secret this tool rotates lives in a root-owned file (panel.env,
// db-password, pdns.env, …) that a service reads at start-up. A botched
// rewrite is a self-lockout: an empty or half-written file, a dropped sibling
// key, or a loosened mode. These helpers are deliberately pure (no exec, no
// network) so the bug-prone core is unit-tested without a box:
//
//   - atomicRewritePreserving — tmp+fsync+rename, keeping the file's EXISTING
//     owner and mode (never loosen; never create).
//   - backupToBak / restoreFromBak / purgeBak — a root-only .bak snapshot so a
//     failed rotation rolls back, and is purged once the new value is verified
//     (a lingering .bak is a fresh copy of the old credential the next audit
//     would flag).
//   - envReplaceKey — replace ONE key's value in an env file, preserving every
//     other line (dropping a sibling secret is the classic env-rewrite bug).
//   - dsnReplacePassword — swap only the password in a go-sql-driver DSN.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// fileIdentity captures a file's owner + permission bits so a rewrite can
// restore them byte-for-byte.
type fileIdentity struct {
	uid, gid int
	mode     os.FileMode
}

// statIdentity reads the current owner+mode. uid/gid are -1 when the platform
// does not expose them (never on the Linux target, but keeps chown optional).
func statIdentity(path string) (fileIdentity, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileIdentity{}, err
	}
	id := fileIdentity{mode: fi.Mode().Perm(), uid: -1, gid: -1}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		id.uid = int(st.Uid)
		id.gid = int(st.Gid)
	}
	return id, nil
}

// atomicRewritePreserving replaces an EXISTING file's contents atomically
// (temp in the same dir → fsync → rename) while preserving its current owner
// and mode. It refuses to create a new file: rotation only rewrites secrets
// that already exist, so a wrong path fails loudly instead of scattering a
// secret to a new location with default perms.
func atomicRewritePreserving(path, content string) error {
	id, err := statIdentity(path)
	if err != nil {
		return fmt.Errorf("stat %s (rotation rewrites existing secrets only): %w", path, err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".jabali-rotate-*")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed; cleans up on any early return
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	// Match the live file's mode BEFORE the rename so the secret is never
	// briefly world-readable at its final path.
	if err := os.Chmod(tmpName, id.mode); err != nil {
		return fmt.Errorf("chmod temp to %o: %w", id.mode, err)
	}
	if id.uid >= 0 && id.gid >= 0 {
		if err := os.Chown(tmpName, id.uid, id.gid); err != nil {
			return fmt.Errorf("chown temp to %d:%d: %w", id.uid, id.gid, err)
		}
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}

// bakSuffix is the rollback snapshot suffix. Kept root-only (0600).
const bakSuffix = ".rotate.bak"

// backupToBak snapshots path to path+bakSuffix (0600, current process owner —
// root during the operator ceremony) for rollback. Overwrites a stale bak.
func backupToBak(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s for backup: %w", path, err)
	}
	bak := path + bakSuffix
	if err := os.WriteFile(bak, data, 0o600); err != nil {
		return "", fmt.Errorf("write backup %s: %w", bak, err)
	}
	return bak, nil
}

// restoreFromBak rolls a file back to its pre-rotation contents, preserving the
// LIVE file's owner+mode, then removes the snapshot. Used when a post-rotation
// health probe fails.
func restoreFromBak(path, bak string) error {
	data, err := os.ReadFile(bak)
	if err != nil {
		return fmt.Errorf("read backup %s: %w", bak, err)
	}
	if err := atomicRewritePreserving(path, string(data)); err != nil {
		return fmt.Errorf("restore %s from backup: %w", path, err)
	}
	return os.Remove(bak)
}

// purgeBak removes a rollback snapshot once the rotation is verified. A missing
// bak is not an error.
func purgeBak(bak string) error {
	if bak == "" {
		return nil
	}
	if err := os.Remove(bak); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// envReplaceKey returns content with the first `KEY=…` line's value replaced by
// newVal, preserving every other line and the file's structure. found is false
// when the key is absent — callers rotate only existing secrets, so an absent
// key must be an error, never a silent append (which would leave the real
// secret in place and add a decoy).
func envReplaceKey(content, key, newVal string) (string, bool) {
	lines := strings.Split(content, "\n")
	prefix := key + "="
	for i, ln := range lines {
		if strings.HasPrefix(ln, prefix) {
			lines[i] = prefix + newVal
			return strings.Join(lines, "\n"), true
		}
	}
	return content, false
}

// dsnReplacePassword swaps the password in a go-sql-driver DSN of the form
// user:password@network(addr)/db?params. It replaces only the segment between
// the first ':' and the '@' that introduces the network, leaving user, host,
// db and query params untouched. The DB password is base64url (ids.NewSecret),
// which contains no ':' or '@', so the split is unambiguous.
func dsnReplacePassword(dsn, newPw string) (string, error) {
	at := strings.LastIndex(dsn, "@")
	if at < 0 {
		return "", fmt.Errorf("dsn has no '@': cannot locate credentials")
	}
	cred, rest := dsn[:at], dsn[at:] // "user:password", "@network(addr)/db?…"
	colon := strings.Index(cred, ":")
	if colon < 0 {
		return "", fmt.Errorf("dsn credentials have no ':': cannot locate password")
	}
	return cred[:colon] + ":" + newPw + rest, nil
}
