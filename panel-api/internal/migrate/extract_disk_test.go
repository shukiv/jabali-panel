package migrate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTarball builds a tarball whose entries have the given sizes. compress
// controls gzip; incompressible fills entries with varied bytes so the archive
// barely shrinks, reproducing the media-heavy account the check used to refuse.
func writeTarball(t *testing.T, sizes []int, compress, incompressible bool) string {
	t.Helper()
	var raw bytes.Buffer
	var w *tar.Writer
	var gz *gzip.Writer
	if compress {
		gz = gzip.NewWriter(&raw)
		w = tar.NewWriter(gz)
	} else {
		w = tar.NewWriter(&raw)
	}

	for i, n := range sizes {
		body := make([]byte, n)
		if incompressible {
			// Real random bytes. An arithmetic "pattern" is not enough — a
			// periodic fill compresses ~250:1, which silently turns this into a
			// test of the compressible case.
			if _, err := rand.Read(body); err != nil {
				t.Fatalf("rand: %v", err)
			}
		}
		if err := w.WriteHeader(&tar.Header{
			Name:     "file" + string(rune('a'+i)),
			Mode:     0o644,
			Size:     int64(n),
			Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatalf("header: %v", err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if gz != nil {
		if err := gz.Close(); err != nil {
			t.Fatalf("close gzip: %v", err)
		}
	}

	path := filepath.Join(t.TempDir(), "cpmove-test.tar.gz")
	if err := os.WriteFile(path, raw.Bytes(), 0o644); err != nil {
		t.Fatalf("write tarball: %v", err)
	}
	return path
}

func TestMeasureUncompressedSize_SumsEntriesNotArchiveSize(t *testing.T) {
	sizes := []int{1 << 20, 2 << 20, 3 << 20} // 6 MiB of content
	path := writeTarball(t, sizes, true, true)

	got, err := MeasureUncompressedSize(path)
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	if want := uint64(6 << 20); got != want {
		t.Fatalf("measured %d, want %d", got, want)
	}

	// And it must differ from the on-disk archive size, or the test proves nothing.
	fi, _ := os.Stat(path)
	if uint64(fi.Size()) == got {
		t.Skip("archive happened to equal content size; nothing to distinguish")
	}
}

// The regression: an incompressible archive whose contents fit comfortably must
// no longer be refused just because free space is below compressed×5.
func TestCheckExtractDiskSpace_IncompressibleArchiveIsNotRefused(t *testing.T) {
	// Contents ~6 MiB and barely compressible, so compressed ≈ 6 MiB. Under the
	// old rule this demanded ~30 MiB; the real need is ~6 MiB + margin.
	path := writeTarball(t, []int{2 << 20, 2 << 20, 2 << 20}, true, true)
	fi, _ := os.Stat(path)

	measured, err := MeasureUncompressedSize(path)
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	// Guard the premise: this archive really is close to incompressible.
	if uint64(fi.Size())*5 < measured {
		t.Fatalf("premise broken: compressed=%d measured=%d", fi.Size(), measured)
	}

	// /tmp on CI has far more than 6 MiB + 2 GiB floor... which is the point of
	// the next assertion: we only require that the check does not refuse when
	// space is plentiful.
	if err := CheckExtractDiskSpace(path, t.TempDir()); err != nil {
		t.Fatalf("feasible restore was refused: %v", err)
	}
}

// The other direction, and the more dangerous one. A real cpmove tarball
// measured 3.47 GiB compressed against 19.81 GiB of contents — a ratio of 5.71.
// Any fixed multiple of the compressed size therefore UNDER-estimates some real
// accounts, and an under-estimate is an accept followed by a half-extract, which
// is the failure this check exists to prevent. The verdict must come from the
// measured index, so a highly-compressible archive must report far more than
// compressed×fallbackRatio.
func TestMeasureUncompressedSize_CatchesRatioAboveFallback(t *testing.T) {
	// Zero-filled entries compress enormously — the compressible extreme.
	path := writeTarball(t, []int{16 << 20, 16 << 20}, true, false)
	fi, _ := os.Stat(path)

	measured, err := MeasureUncompressedSize(path)
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	if want := uint64(32 << 20); measured != want {
		t.Fatalf("measured %d, want %d", measured, want)
	}
	guess := uint64(fi.Size()) * fallbackRatio
	if guess >= measured {
		t.Fatalf("fixture is not compressible enough to exercise the case: guess=%d measured=%d", guess, measured)
	}
	// The whole point: the guess would have allowed a restore that needs far more.
	t.Logf("compressed=%d guess(x%d)=%d measured=%d — guess is %.1fx too small",
		fi.Size(), fallbackRatio, guess, measured, float64(measured)/float64(guess))
}

func TestExtractMargin_FloorAndFraction(t *testing.T) {
	// Small tree: the floor dominates.
	if got := extractMargin(1 << 20); got != extractMarginFloor {
		t.Errorf("small tree margin = %d, want floor %d", got, extractMarginFloor)
	}
	// Large tree: the fraction dominates.
	big := uint64(100) << 30 // 100 GiB
	if got, want := extractMargin(big), big/extractMarginFraction; got != want {
		t.Errorf("large tree margin = %d, want %d", got, want)
	}
	// The fraction must actually exceed the floor at that size, or the case above
	// is vacuous.
	if big/extractMarginFraction <= extractMarginFloor {
		t.Error("fraction never dominates the floor — margin constants are inconsistent")
	}
}

// Best-effort contract: a check that cannot see must not veto a migration.
func TestCheckExtractDiskSpace_UnreadableInputsDoNotBlock(t *testing.T) {
	if err := CheckExtractDiskSpace("/nonexistent/x.tar.gz", t.TempDir()); err != nil {
		t.Errorf("missing tarball must not block: %v", err)
	}
	real := writeTarball(t, []int{1 << 10}, true, false)
	if err := CheckExtractDiskSpace(real, "/nonexistent/dest"); err != nil {
		t.Errorf("unstattable dest must not block: %v", err)
	}
}

// A corrupt archive must not be vetoed here — extraction reports it far better.
func TestCheckExtractDiskSpace_CorruptArchiveDoesNotBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.tar.gz")
	// Valid gzip magic, garbage body.
	if err := os.WriteFile(path, []byte{0x1f, 0x8b, 0x08, 0x00, 0xde, 0xad, 0xbe, 0xef}, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := CheckExtractDiskSpace(path, t.TempDir()); err != nil {
		t.Errorf("corrupt archive must not block the pipeline here: %v", err)
	}
}

func TestMeasureUncompressedSize_UncompressedTarball(t *testing.T) {
	path := writeTarball(t, []int{4 << 20}, false, false)
	got, err := MeasureUncompressedSize(path)
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	if want := uint64(4 << 20); got != want {
		t.Fatalf("measured %d, want %d", got, want)
	}
}

// The refusal message must quote the measured number, so an operator can tell
// the difference between "your box is too small" and "the estimator is wrong" —
// which is exactly what cost two failed attempts in the field.
func TestCheckExtractDiskSpace_RefusalNamesMeasuredSize(t *testing.T) {
	// /proc has 0 blocks available, so any positive requirement fails there.
	path := writeTarball(t, []int{1 << 20}, true, true)
	err := CheckExtractDiskSpace(path, "/proc")
	if err == nil {
		t.Skip("/proc did not report zero free space on this platform")
	}
	for _, want := range []string{"uncompressed contents measure", "including margin", "free on"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal message missing %q: %v", want, err)
		}
	}
}
