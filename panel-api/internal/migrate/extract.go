package migrate

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/hostreserve"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// maxExtractFileBytes caps a single extracted file (loose backstop against a
// degenerate tar entry). The manifest capacity gate is the real limit.
const maxExtractFileBytes = 100 << 30 // 100 GiB

// JAB-241 extraction budgets. Per-file alone lets a bomb of many
// individually-legal entries fill the disk or exhaust inodes.
var (
	// maxExtractEntries bounds the entry count (inode exhaustion).
	// Real cpmove/WP trees run tens-to-hundreds of thousands of files;
	// two million is far past any legitimate account. var (not const)
	// only so tests can lower it; production never mutates it.
	maxExtractEntries = 2 << 20
	// maxExtractTotalBytes is the absolute backstop when the archive
	// could not be measured (corrupt index). var only for tests.
	maxExtractTotalBytes = uint64(1) << 40 // 1 TiB
)

const (
	// lyingHeader ratio: when the archive was measured, cumulative writes
	// may exceed the measurement by 50% + the standard margin before we
	// call the index a lie and abort. Headers are only trusted as an
	// upper-bound hint — enforcement counts written bytes.
	lyingHeaderNum, lyingHeaderDen = uint64(3), uint64(2)
	// extractReserveEvery: re-check the host reserve floor after this
	// many written bytes, so concurrent disk pressure stops an extract
	// that started with room (live check, not a one-shot preflight).
	extractReserveEvery = 256 << 20
)

// ExtractTarGz extracts a (optionally gzip'd) tarball into dest with path
// containment: header names with `../`, `/../`, or absolute paths are skipped,
// the joined output path is re-verified to stay under dest (belt-and-suspenders),
// and symlink/hardlink entries are skipped entirely (no write-through). Lifted
// from the migrate CLI so the wordpress_ssh/plugin imports (GH #647/#648) reuse
// one proven containment-safe extractor for the UNTRUSTED source tarball.
func ExtractTarGz(tarPath, dest string) error {
	return extractTarGzWithCap(tarPath, dest, extractTotalCap(tarPath))
}

// extractTarGzWithCap is ExtractTarGz with the cumulative write ceiling
// injected — split out so the enforcement loop is testable without a
// terabyte fixture (JAB-241).
func extractTarGzWithCap(tarPath, dest string, totalCap uint64) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()

	buf := make([]byte, 2)
	if _, err := io.ReadFull(f, buf); err != nil {
		return fmt.Errorf("magic: %w", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	var src io.Reader = f
	if buf[0] == 0x1f && buf[1] == 0x8b {
		gz, gerr := gzip.NewReader(f)
		if gerr != nil {
			return fmt.Errorf("gunzip: %w", gerr)
		}
		defer gz.Close()
		src = gz
	}

	cleanDest := filepath.Clean(dest)
	tr := tar.NewReader(src)
	var written, sinceReserve uint64
	var entries int
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read: %w", err)
		}
		entries++
		if entries > maxExtractEntries {
			return fmt.Errorf("archive exceeds %d entries — refusing (inode-exhaustion guard, JAB-241)", maxExtractEntries)
		}
		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") || filepath.IsAbs(clean) {
			continue // path traversal in the header name
		}
		out := filepath.Join(cleanDest, clean)
		// Belt-and-suspenders: the joined path must remain under dest.
		if out != cleanDest && !strings.HasPrefix(out, cleanDest+string(os.PathSeparator)) {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(out, 0o750); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
				return err
			}
			w, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
			if err != nil {
				return err
			}
			// GuardedWriter gives MID-ENTRY reserve cadence — one entry can
			// be 100 GiB, and "check every 256 MiB" must hold inside it,
			// not only at entry boundaries (JAB-241).
			n, err := io.Copy(hostreserve.GuardedWriter(w, cleanDest), io.LimitReader(tr, maxExtractFileBytes))
			if err != nil {
				_ = w.Close()
				return err
			}
			if err := w.Close(); err != nil {
				return err
			}
			written += uint64(n)
			if written > totalCap {
				return fmt.Errorf("archive wrote %d MiB, past the %d MiB budget — refusing (decompression-bomb guard, JAB-241)", written>>20, totalCap>>20)
			}
			sinceReserve += uint64(n)
			if sinceReserve >= extractReserveEvery {
				sinceReserve = 0
				// Live low-water check (JAB-241): whatever the preflight
				// said, the disk state NOW wins — concurrent writers may
				// have eaten the room this extract started with.
				if err := hostreserve.CheckReserve(cleanDest, 0); err != nil {
					return fmt.Errorf("extraction stopped: %w", err)
				}
			}
			// TypeSymlink / TypeLink and everything else: skipped (no write-through).
		}
	}
	return nil
}

// fallbackRatio is the compressed-size multiple used ONLY when the archive
// cannot be measured. It is the pre-JAB-219 heuristic, kept as a last resort
// because some estimate beats none when the tar index is unreadable.
//
// It is not used for a verdict on a readable archive, because measurements from
// real cpmove tarballs show it wrong in BOTH directions:
//
//   - Too high for already-compressed accounts (media, WordPress uploads, old
//     backup archives): the tree is roughly the tarball size, so ×5 demanded
//     five times the real requirement. An 18 GiB account was refused twice on a
//     host with 60-77 GiB free — the only failure in a 50-account program.
//   - Too LOW for compressible accounts, which is the more dangerous error. A
//     real cpmove tarball measured here: 3.47 GiB compressed expanding to 19.81
//     GiB — a ratio of 5.71. With 18 GiB free, ×5 would have waved it through
//     and then run out of disk mid-extract, which is precisely the half-extract
//     failure this check exists to prevent (JAB-41).
//
// No fixed ratio can bound gzip expansion, so a verdict has to come from the
// archive itself.
const fallbackRatio = 5

// extractMarginFraction and extractMarginFloor are the working headroom added
// on top of the MEASURED uncompressed size: the restore writes its own copies
// while unpacking, so "exactly the tree size" is not enough.
// The floor is deliberately small. It exists so a tiny archive still gets some
// slack, not as a fixed tax: a multi-gigabyte floor would refuse a 1 KiB
// tarball on any host with a modest staging filesystem, which is the same class
// of wrong answer this change is fixing. For anything above ~256 MiB the
// fraction dominates anyway.
const (
	extractMarginFraction = 4        // needed += measured/4  (i.e. +25%)
	extractMarginFloor    = 64 << 20 // ...but never less than 64 MiB
)

// MeasureUncompressedSize streams the tarball's index and sums every entry's
// size, WITHOUT writing anything to disk. This is the real uncompressed
// footprint rather than a guess derived from a compression ratio.
//
// It costs one decompression pass — tens of seconds for a multi-GiB account,
// against a restore that then extracts, rsyncs and imports databases. Paying it
// buys the only number that is right in both directions, and the alternative is
// either refusing feasible migrations or half-extracting infeasible ones.
func MeasureUncompressedSize(tarballPath string) (uint64, error) {
	f, err := os.Open(tarballPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	magic := make([]byte, 2)
	if _, err := io.ReadFull(f, magic); err != nil {
		return 0, fmt.Errorf("magic: %w", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	var src io.Reader = f
	if magic[0] == 0x1f && magic[1] == 0x8b {
		gz, gerr := gzip.NewReader(f)
		if gerr != nil {
			return 0, fmt.Errorf("gunzip: %w", gerr)
		}
		defer gz.Close()
		src = gz
	}

	var total uint64
	tr := tar.NewReader(src)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("read tar index: %w", err)
		}
		if h.Size > 0 {
			total += uint64(h.Size)
		}
	}
	return total, nil
}

// CheckExtractDiskSpace refuses to extract a backup when the destination
// filesystem genuinely lacks room for the uncompressed tree, so a low-disk host
// fails FAST with a clear message instead of half-extracting and then erroring
// deep in the restore pipeline (JAB-41).
//
// The verdict comes from MEASURING the archive, never from a compression-ratio
// guess (JAB-219) — see fallbackRatio for why every fixed multiple is wrong in
// one direction or the other.
//
// Best-effort throughout: if the tarball or filesystem cannot be inspected it
// does not block, because a check that cannot see is not entitled to veto a
// migration. An archive too corrupt to measure is likewise waved through — the
// extractor reports that far better than a disk estimator can.
func CheckExtractDiskSpace(tarballPath, dest string) error {
	fi, err := os.Stat(tarballPath)
	if err != nil {
		return nil
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(dest, &st); err != nil {
		return nil
	}
	free := st.Bavail * uint64(st.Bsize)

	measured, merr := MeasureUncompressedSize(tarballPath)
	if merr != nil {
		// Could not read the index. Fall back to the old heuristic rather than
		// dropping the check entirely, but only to catch the grossly-too-small
		// case — and say which number this is, so nobody debugs the wrong one.
		needed := uint64(fi.Size()) * fallbackRatio
		if free < needed {
			return fmt.Errorf(
				"insufficient disk space to extract %s: could not measure contents (%v), estimating ~%d MiB from compressed size, only %d MiB free on %s (free up space or move staging)",
				filepath.Base(tarballPath), merr, needed>>20, free>>20, dest)
		}
		return nil
	}

	needed := measured + extractMargin(measured)
	if free >= needed {
		return nil
	}
	return fmt.Errorf(
		"insufficient disk space to extract %s: uncompressed contents measure %d MiB, need %d MiB including margin, only %d MiB free on %s (free up space or move staging)",
		filepath.Base(tarballPath), measured>>20, needed>>20, free>>20, dest)
}

// extractTotalCap is the cumulative write ceiling for one extraction
// (JAB-241). Measure when possible (same pass the JAB-41 preflight
// uses); the measured total ×1.5 + margin becomes the ceiling, so lying
// headers cannot stream past the measurement. Unmeasurable archives get
// the absolute backstop instead. Enforcement counts WRITTEN bytes in
// the extract loop — headers are only ever an upper-bound hint.
func extractTotalCap(tarPath string) uint64 {
	if measured, merr := MeasureUncompressedSize(tarPath); merr == nil {
		return measured*lyingHeaderNum/lyingHeaderDen + extractMargin(measured)
	}
	return maxExtractTotalBytes
}

// extractMargin is the working headroom for a measured tree.
func extractMargin(measured uint64) uint64 {
	m := measured / extractMarginFraction
	if m < extractMarginFloor {
		m = extractMarginFloor
	}
	return m
}
