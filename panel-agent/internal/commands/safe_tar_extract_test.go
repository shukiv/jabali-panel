package commands

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GH #1408 — the untrusted-tar extractor is the security contract of the
// upload-restore feature. These prove it accepts a well-formed backup tree and
// rejects every classic root-extraction escape.

type tentry struct {
	typ   byte
	name  string
	body  string
	link  string
	mode  int64
	major int64
	minor int64
}

func buildTar(t *testing.T, entries []tentry) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		hdr := &tar.Header{
			Typeflag: e.typ,
			Name:     e.name,
			Linkname: e.link,
			Mode:     mode,
			Size:     int64(len(e.body)),
			Devmajor: e.major,
			Devminor: e.minor,
		}
		if e.typ == tar.TypeDir && !strings.HasSuffix(hdr.Name, "/") {
			hdr.Name += "/"
		}
		if e.typ != tar.TypeReg {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %q: %v", e.name, err)
		}
		if e.typ == tar.TypeReg && e.body != "" {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatalf("write body %q: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

func TestExtractTarStream_HappyPath(t *testing.T) {
	root := t.TempDir()
	buf := buildTar(t, []tentry{
		{typ: tar.TypeDir, name: "home"},
		{typ: tar.TypeReg, name: "home/a.txt", body: "hello"},
		{typ: tar.TypeDir, name: "db"},
		{typ: tar.TypeReg, name: "db/site.sql", body: "CREATE TABLE t();"},
		{typ: tar.TypeReg, name: "manifest.json", body: "{}"},
		{typ: tar.TypeSymlink, name: "home/link", link: "a.txt"}, // in-tree, allowed
	})
	n, err := extractTarStream(buf, root)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if n != int64(len("hello")+len("CREATE TABLE t();")+len("{}")) {
		t.Fatalf("bytes written = %d, unexpected", n)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "home", "a.txt")); string(b) != "hello" {
		t.Fatalf("home/a.txt content wrong")
	}
	if fi, err := os.Lstat(filepath.Join(root, "home", "link")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("home/link should be a symlink: %v", err)
	}
}

func TestExtractTarStream_RejectsEscapes(t *testing.T) {
	cases := []struct {
		name    string
		entries []tentry
		wantSub string
	}{
		{"absolute path", []tentry{{typ: tar.TypeReg, name: "/etc/passwd", body: "x"}}, "absolute"},
		{"dotdot traversal", []tentry{{typ: tar.TypeReg, name: "../../etc/cron.d/x", body: "x"}}, ".."},
		{"hardlink", []tentry{{typ: tar.TypeLink, name: "shadow", link: "/etc/shadow"}}, "hardlink"},
		{"char device", []tentry{{typ: tar.TypeChar, name: "dev", major: 1, minor: 3}}, "special"},
		{"fifo", []tentry{{typ: tar.TypeFifo, name: "pipe"}}, "special"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			_, err := extractTarStream(buildTar(t, tc.entries), root)
			if err == nil {
				t.Fatalf("expected rejection, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q missing %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// A real home carries absolute + out-of-tree symlinks (php shims, etc.). They
// are inert during extraction (never followed), so they must be preserved
// verbatim, exactly as the trusted restic restore does — NOT rejected.
func TestExtractTarStream_PreservesSymlinksVerbatim(t *testing.T) {
	root := t.TempDir()
	buf := buildTar(t, []tentry{
		{typ: tar.TypeDir, name: "home"},
		{typ: tar.TypeSymlink, name: "home/php", link: "/usr/bin/php8.5"}, // absolute
		{typ: tar.TypeSymlink, name: "home/up", link: "../../../etc"},     // out-of-tree
	})
	if _, err := extractTarStream(buf, root); err != nil {
		t.Fatalf("real-home symlinks must extract, got %v", err)
	}
	if tgt, _ := os.Readlink(filepath.Join(root, "home", "php")); tgt != "/usr/bin/php8.5" {
		t.Fatalf("absolute symlink target = %q, want /usr/bin/php8.5", tgt)
	}
	if tgt, _ := os.Readlink(filepath.Join(root, "home", "up")); tgt != "../../../etc" {
		t.Fatalf("out-of-tree symlink target = %q, want ../../../etc", tgt)
	}
}

// A symlink component must never be traversed by a later entry: "s" -> "home"
// (in-tree, allowed to create), then a regular file "s/evil" must be refused
// rather than written through the symlink.
func TestExtractTarStream_NoWriteThroughSymlinkParent(t *testing.T) {
	root := t.TempDir()
	buf := buildTar(t, []tentry{
		{typ: tar.TypeDir, name: "home"},
		{typ: tar.TypeSymlink, name: "s", link: "home"},
		{typ: tar.TypeReg, name: "s/evil", body: "x"},
	})
	_, err := extractTarStream(buf, root)
	if err == nil || !strings.Contains(err.Error(), "symlink component") {
		t.Fatalf("expected refusal to descend through symlink parent, got %v", err)
	}
}

// The decompression-bomb budget aborts a file that would exceed the remaining
// allowance rather than writing it.
func TestWriteRegularNoFollow_BudgetGuard(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "big")
	_, err := writeRegularNoFollow(dest, strings.NewReader(strings.Repeat("A", 100)), 0o644, 10)
	if err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("expected budget-exceeded, got %v", err)
	}
}

func TestSafeRelPath(t *testing.T) {
	ok := map[string]string{
		"home/a.txt": "home/a.txt",
		"./db/x.sql": "db/x.sql",
		"manifest.json": "manifest.json",
		".":          "",
		"":           "",
	}
	for in, want := range ok {
		got, err := safeRelPath(in)
		if err != nil || got != want {
			t.Fatalf("safeRelPath(%q) = %q,%v want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"/abs", "../up", "a/../../b", `\abs`} {
		if _, err := safeRelPath(bad); err == nil {
			t.Fatalf("safeRelPath(%q) should error", bad)
		}
	}
}
