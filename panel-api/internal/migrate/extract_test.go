package migrate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GH #647/#648: ExtractTarGz must contain an UNTRUSTED source tarball — no
// path-traversal write, no symlink write-through — while extracting legit files.
func TestExtractTarGz_Containment(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	writeFile := func(name, body string) {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	writeFile("wp-config.php", "<?php define('DB_NAME','x');")
	writeFile("wp-content/themes/t/style.css", "body{}")
	writeFile("../escape.txt", "PWNED")       // traversal
	writeFile("a/../../escape2.txt", "PWNED") // traversal via ..
	writeFile("/abs.txt", "PWNED")            // absolute
	_ = tw.WriteHeader(&tar.Header{Name: "evil-link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
	_ = tw.Close()
	_ = gz.Close()

	dir := t.TempDir()
	src := filepath.Join(dir, "in.tar.gz")
	if err := os.WriteFile(src, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "out")
	if err := ExtractTarGz(src, dest); err != nil {
		t.Fatalf("ExtractTarGz: %v", err)
	}

	// Legit files landed under dest.
	if b, err := os.ReadFile(filepath.Join(dest, "wp-config.php")); err != nil || string(b) == "" {
		t.Errorf("wp-config.php not extracted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "wp-content/themes/t/style.css")); err != nil {
		t.Errorf("nested file not extracted: %v", err)
	}
	// Nothing escaped dest.
	for _, escaped := range []string{
		filepath.Join(dir, "escape.txt"),
		filepath.Join(dir, "escape2.txt"),
		filepath.Join(dir, "abs.txt"),
		"/abs.txt",
	} {
		if _, err := os.Stat(escaped); err == nil {
			t.Errorf("path traversal wrote %s", escaped)
		}
	}
	// The symlink was NOT created (no write-through).
	if _, err := os.Lstat(filepath.Join(dest, "evil-link")); err == nil {
		t.Error("symlink entry was created (write-through risk)")
	}
}

// Env-gated: extract a REAL WordPress tarball and confirm the tree lands.
// Skipped in CI. JABALI_EXTRACT_REAL=<tarball path>. GH #647.
func TestExtractTarGz_RealTarball(t *testing.T) {
	src := os.Getenv("JABALI_EXTRACT_REAL")
	if src == "" {
		t.Skip("set JABALI_EXTRACT_REAL=<tarball> to run")
	}
	dest := t.TempDir()
	if err := ExtractTarGz(src, dest); err != nil {
		t.Fatalf("ExtractTarGz(real): %v", err)
	}
	for _, want := range []string{"wp-config.php", "wp-includes/version.php", "wp-load.php"} {
		if _, err := os.Stat(filepath.Join(dest, want)); err != nil {
			t.Errorf("real WP tree missing %s: %v", want, err)
		}
	}
	// count extracted files
	n := 0
	filepath.Walk(dest, func(_ string, fi os.FileInfo, _ error) error {
		if fi != nil && fi.Mode().IsRegular() {
			n++
		}
		return nil
	})
	t.Logf("extracted %d regular files under dest", n)
	if n < 100 {
		t.Errorf("suspiciously few files: %d", n)
	}
}

// JAB-41: CheckExtractDiskSpace is best-effort — a missing tarball or unstattable
// dest must not block; a small tarball on a normal fs passes.
func TestCheckExtractDiskSpace(t *testing.T) {
	if err := CheckExtractDiskSpace("/nonexistent/tarball.tar.gz", "/tmp"); err != nil {
		t.Errorf("missing tarball should not block (best-effort), got %v", err)
	}
	dir := t.TempDir()
	f := dir + "/small.tar.gz"
	if err := os.WriteFile(f, make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckExtractDiskSpace(f, dir); err != nil {
		t.Errorf("1KiB tarball should pass on a normal fs, got %v", err)
	}
}

// buildManyEntriesTarGz writes a tar.gz with n tiny regular files.
func buildManyEntriesTarGz(t *testing.T, path string, n int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("f%d.txt", i)
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// JAB-241: an archive with more entries than the cap must abort with the
// inode-exhaustion guard, not extract to completion.
func TestExtractTarGz_EntryCountCap(t *testing.T) {
	orig := maxExtractEntries
	maxExtractEntries = 10
	defer func() { maxExtractEntries = orig }()

	dir := t.TempDir()
	tarball := filepath.Join(dir, "bomb.tar.gz")
	buildManyEntriesTarGz(t, tarball, 25)
	err := ExtractTarGz(tarball, filepath.Join(dir, "out"))
	if err == nil {
		t.Fatal("25-entry archive extracted past a 10-entry cap")
	}
	if !strings.Contains(err.Error(), "entries") {
		t.Fatalf("wrong error: %v", err)
	}
}

// JAB-241: a measurable archive's write ceiling is measured*1.5+margin;
// an unmeasurable one falls back to the absolute backstop.
func TestExtractTotalCap(t *testing.T) {
	dir := t.TempDir()
	tarball := filepath.Join(dir, "ok.tar.gz")
	buildManyEntriesTarGz(t, tarball, 5) // measures 5 bytes
	capBytes := extractTotalCap(tarball)
	want := uint64(5)*lyingHeaderNum/lyingHeaderDen + extractMargin(5)
	if capBytes != want {
		t.Fatalf("measured cap = %d, want %d", capBytes, want)
	}
	// Unmeasurable: not a tarball at all.
	garbage := filepath.Join(dir, "garbage.bin")
	if err := os.WriteFile(garbage, []byte("not a tar"), 0o640); err != nil {
		t.Fatal(err)
	}
	if got := extractTotalCap(garbage); got != maxExtractTotalBytes {
		t.Fatalf("unmeasurable cap = %d, want backstop %d", got, maxExtractTotalBytes)
	}
}

// JAB-241: cumulative-write enforcement — with the ceiling injected low,
// an archive whose real bytes exceed it must abort mid-extract. (The
// production ceiling comes from extractTotalCap, tested above; the loop
// counts WRITTEN bytes, so it also holds when measure and reality diverge.)
func TestExtractTarGz_TotalWriteCap(t *testing.T) {
	dir := t.TempDir()
	tarball := filepath.Join(dir, "plain.tar")
	f, err := os.Create(tarball)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("g%d.txt", i)
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 2, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("xy")); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	err = extractTarGzWithCap(tarball, filepath.Join(dir, "out"), 3)
	if err == nil {
		t.Fatal("6 written bytes passed a 3-byte total cap")
	}
	if !strings.Contains(err.Error(), "budget") {
		t.Fatalf("wrong error: %v", err)
	}
}
