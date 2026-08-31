package commands

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// safe_tar_extract.go — GH #1408. Extract an UNTRUSTED, zstd-compressed tar
// (a re-uploaded account backup) into a root-owned staging dir.
//
// Unlike the restic restore path, whose input is a snapshot WE created, this tar
// is attacker-controllable: a tampered archive is extracted as root and its
// contents are later rsync'd into /home and loaded into databases. So the
// extractor is an allowlist, not a denylist:
//
//   - regular files, directories, and symlinks (preserved verbatim — a real home
//     legitimately carries absolute + out-of-tree symlinks, and the trusted
//     restic restore keeps them too).
//   - REJECT absolute entry PATHS and any ".." component (per-component, post-Clean).
//   - REJECT hardlinks (TypeLink) — a hardlink to /etc/shadow followed by an
//     entry that "overwrites" it is the classic root-extraction escape; our own
//     materialized backups never contain them.
//   - REJECT device / fifo / char / block special files.
//   - never write THROUGH a symlinked parent: every path component is created as
//     a real directory (refuse a pre-existing symlink component) and every file
//     is opened O_EXCL|O_NOFOLLOW. This — not the symlink target — is the real
//     boundary: an inert symlink is never followed during extraction, so where
//     it points can't redirect a root-side write.
//   - bounded: a max total-bytes budget (decompression bomb) and a max entry
//     count, both abort the extraction.
//
// zstd is streamed through `zstd -dc <src>` — src is a path WE chose (the
// upload staging file), never a value from the archive, so no attacker arg.

const (
	// safeTarMaxBytes caps the total uncompressed bytes written — a
	// decompression-bomb guard. 200 GiB is generous for a full account
	// (a per-user backup this large is already an outlier) while still
	// bounding a malicious 100:1 zstd bomb.
	safeTarMaxBytes = int64(200) << 30
	// safeTarMaxEntries caps the number of members so a tar of millions of
	// empty files can't exhaust inodes / spin forever.
	safeTarMaxEntries = 2_000_000
)

// safeExtractZstdTar decompresses the zstd tar at srcPath and extracts it into
// destRoot (which must already exist). Returns the number of bytes written.
// destRoot must be an absolute, cleaned path with no symlink components — the
// caller owns creating it fresh.
func safeExtractZstdTar(ctx context.Context, srcPath, destRoot string) (int64, error) {
	if !filepath.IsAbs(destRoot) || destRoot != filepath.Clean(destRoot) {
		return 0, fmt.Errorf("destRoot must be an absolute clean path")
	}
	// Decompress through the zstd CLI; srcPath is our own staging path.
	zstd := exec.CommandContext(ctx, "zstd", "-dc", srcPath)
	stdout, err := zstd.StdoutPipe()
	if err != nil {
		return 0, fmt.Errorf("zstd pipe: %w", err)
	}
	zstd.Stderr = io.Discard
	if err := zstd.Start(); err != nil {
		return 0, fmt.Errorf("zstd start: %w", err)
	}
	// Always drain/kill the decompressor so a rejected archive can't leave a
	// blocked child holding the pipe.
	defer func() {
		_ = stdout.Close()
		_ = zstd.Wait()
	}()

	written, err := extractTarStream(stdout, destRoot)
	if err != nil {
		return written, err
	}
	// The archive body was accepted; make sure the decompressor itself didn't
	// fail (truncated/corrupt zstd frame the tar reader didn't notice).
	if werr := zstd.Wait(); werr != nil {
		return written, fmt.Errorf("zstd decode failed (corrupt or not a zstd archive): %w", werr)
	}
	return written, nil
}

// extractTarStream reads a plain (already-decompressed) tar from r into destRoot
// with the allowlist + budget above. Split from the zstd wrapper so tests can
// drive it with a tar built in-memory.
func extractTarStream(r io.Reader, destRoot string) (int64, error) {
	tr := tar.NewReader(r)
	var written int64
	entries := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return written, fmt.Errorf("read tar entry: %w", err)
		}
		entries++
		if entries > safeTarMaxEntries {
			return written, fmt.Errorf("archive has too many entries (> %d)", safeTarMaxEntries)
		}

		rel, rerr := safeRelPath(hdr.Name)
		if rerr != nil {
			return written, fmt.Errorf("unsafe entry %q: %w", hdr.Name, rerr)
		}
		if rel == "" {
			continue // "." / root entry — nothing to create
		}
		dest := filepath.Join(destRoot, rel)

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := safeMkdirAll(destRoot, rel, 0o750); err != nil {
				return written, err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := safeMkdirAll(destRoot, filepath.Dir(rel), 0o750); err != nil {
				return written, err
			}
			n, werr := writeRegularNoFollow(dest, tr, hdr.FileInfo().Mode().Perm(), safeTarMaxBytes-written)
			written += n
			if werr != nil {
				return written, fmt.Errorf("write %q: %w", rel, werr)
			}
		case tar.TypeSymlink:
			if err := safeMkdirAll(destRoot, filepath.Dir(rel), 0o750); err != nil {
				return written, err
			}
			if err := safeSymlink(destRoot, rel, hdr.Linkname); err != nil {
				return written, fmt.Errorf("symlink %q: %w", rel, err)
			}
		case tar.TypeLink:
			return written, fmt.Errorf("refusing hardlink entry %q (target %q)", hdr.Name, hdr.Linkname)
		case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			return written, fmt.Errorf("refusing special (device/fifo) entry %q", hdr.Name)
		default:
			// TypeXGlobalHeader / pax extensions / unknown → skip silently.
			continue
		}
	}
	return written, nil
}

// safeRelPath normalizes a tar member name to a relative path under the root, or
// errors if it is absolute or escapes via "..". Returns "" for the root itself.
func safeRelPath(name string) (string, error) {
	if name == "" {
		return "", nil
	}
	// Reject a Windows-style or POSIX absolute path outright.
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) {
		return "", fmt.Errorf("absolute path")
	}
	clean := filepath.Clean("/" + name) // anchor, then strip — collapses any "..".
	rel := strings.TrimPrefix(clean, "/")
	if rel == "" || rel == "." {
		return "", nil
	}
	// After anchoring at "/", a Clean result can never contain "..", but check
	// the raw components too so a "foo/../../bar" that Clean would collapse to an
	// in-root path is still rejected as suspicious input.
	for _, comp := range strings.Split(filepath.ToSlash(name), "/") {
		if comp == ".." {
			return "", fmt.Errorf("contains a .. component")
		}
	}
	return rel, nil
}

// safeMkdirAll creates rel (a cleaned, escape-checked relative path) under root,
// component by component, refusing to traverse an existing symlink — so an
// earlier in-tree symlink entry can't redirect a later mkdir out of the tree.
func safeMkdirAll(root, rel string, perm os.FileMode) error {
	if rel == "" || rel == "." {
		return nil
	}
	cur := root
	for _, comp := range strings.Split(rel, string(filepath.Separator)) {
		if comp == "" {
			continue
		}
		cur = filepath.Join(cur, comp)
		fi, err := os.Lstat(cur)
		if err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("refusing to descend through symlink component %q", cur)
			}
			if !fi.IsDir() {
				return fmt.Errorf("path component %q exists and is not a directory", cur)
			}
			continue
		}
		if !os.IsNotExist(err) {
			return err
		}
		if err := os.Mkdir(cur, perm); err != nil {
			return err
		}
	}
	return nil
}

// writeRegularNoFollow writes a regular file, refusing to follow a symlink at the
// leaf (O_EXCL|O_NOFOLLOW), and copies at most `budget` bytes before aborting as
// a decompression-bomb guard.
func writeRegularNoFollow(dest string, src io.Reader, perm os.FileMode, budget int64) (int64, error) {
	if budget <= 0 {
		return 0, fmt.Errorf("total extraction budget (%d bytes) exceeded", safeTarMaxBytes)
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, perm&0o777)
	if err != nil {
		return 0, err
	}
	// +1 so a file exactly at the remaining budget still copies, and a file that
	// would push us over trips the guard instead of silently truncating.
	n, cerr := io.CopyN(f, src, budget+1)
	closeErr := f.Close()
	if cerr != nil && cerr != io.EOF {
		return n, cerr
	}
	if n > budget {
		return n, fmt.Errorf("total extraction budget (%d bytes) exceeded", safeTarMaxBytes)
	}
	if closeErr != nil {
		return n, closeErr
	}
	return n, nil
}

// safeSymlink creates the symlink at rel pointing to linkname, VERBATIM — a real
// home legitimately carries absolute (e.g. ~/.jabali/bin/php8.5 -> /usr/bin/
// php8.5) and out-of-tree symlinks, and the trusted restic restore preserves
// them the same way. The link target is never a write vector here: extraction
// never follows a symlink, safeMkdirAll refuses to descend through a symlink
// parent, and files open O_NOFOLLOW — so a symlink can't redirect any root-side
// write no matter where it points. The parent chain was already created as real
// directories by the caller's safeMkdirAll before this runs.
func safeSymlink(root, rel, linkname string) error {
	if linkname == "" {
		return fmt.Errorf("empty symlink target")
	}
	dest := filepath.Join(root, rel)
	// No-clobber: refuse to replace an existing leaf.
	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("target already exists")
	}
	return os.Symlink(linkname, dest)
}
