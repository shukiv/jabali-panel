package filesafe

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// Happy-path coverage for the openat2 primitives — the escape tests only prove
// the rejection path, so these prove the primitives actually DO the work
// correctly (a corrupt/empty copy would pass an escape test).

func funcScope(t *testing.T) (*Scope, string) {
	t.Helper()
	root := t.TempDir()
	docroot := filepath.Join(root, "home")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatal(err)
	}
	return NewScopeForTest("u", "u", docroot), docroot
}

func TestCopyTreeInScope_ReproducesTree(t *testing.T) {
	scope, docroot := funcScope(t)
	src := filepath.Join(docroot, "src")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("aaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("bbb"), 0o600); err != nil {
		t.Fatal(err)
	}
	// In-scope relative symlink — must be reproduced AS a symlink, not chased.
	if err := os.Symlink("a.txt", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(docroot, "dst")
	n, err := scope.CopyTreeInScope(context.Background(), src, dst, os.Getuid(), os.Getgid())
	if err != nil {
		t.Fatalf("CopyTreeInScope: %v", err)
	}
	if n != 6 { // "aaa" + "bbb"; symlink contributes 0
		t.Errorf("copied bytes = %d, want 6", n)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "a.txt")); string(b) != "aaa" {
		t.Errorf("dst/a.txt = %q, want aaa", string(b))
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "sub", "b.txt")); string(b) != "bbb" {
		t.Errorf("dst/sub/b.txt = %q, want bbb", string(b))
	}
	li, err := os.Lstat(filepath.Join(dst, "link"))
	if err != nil || li.Mode()&os.ModeSymlink == 0 {
		t.Errorf("dst/link should be a symlink, got mode=%v err=%v", li.Mode(), err)
	}
	if tgt, _ := os.Readlink(filepath.Join(dst, "link")); tgt != "a.txt" {
		t.Errorf("dst/link target = %q, want a.txt", tgt)
	}
	// Perm bits preserved on the regular files.
	if fi, _ := os.Lstat(filepath.Join(dst, "sub", "b.txt")); fi.Mode().Perm() != 0o600 {
		t.Errorf("dst/sub/b.txt perm = %o, want 600", fi.Mode().Perm())
	}
}

// GH #1392: CountTreeBytesInScope must sum exactly the regular-file bytes the
// byte-progress copy will write (symlinks + dirs contribute 0), and
// CopyTreeInScopeProgress must report that same total via onBytes.
func TestCountAndCopyProgress(t *testing.T) {
	scope, docroot := funcScope(t)
	src := filepath.Join(docroot, "src")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("aaaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("bbbbbb"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.txt", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}

	total, err := scope.CountTreeBytesInScope(context.Background(), src)
	if err != nil {
		t.Fatalf("CountTreeBytesInScope: %v", err)
	}
	if total != 10 { // 4 + 6; symlink counts 0
		t.Fatalf("count = %d, want 10", total)
	}

	var ticked int64
	dst := filepath.Join(docroot, "dst")
	n, err := scope.CopyTreeInScopeProgress(context.Background(), src, dst, os.Getuid(), os.Getgid(), func(d int64) { ticked += d })
	if err != nil {
		t.Fatalf("CopyTreeInScopeProgress: %v", err)
	}
	if n != 10 {
		t.Fatalf("copied bytes = %d, want 10", n)
	}
	if ticked != 10 {
		t.Fatalf("progress ticks summed to %d, want 10 (must equal bytes copied)", ticked)
	}
}

// A single regular file must still report its bytes through onBytes (the
// worst-case-for-file-count path: one big file).
func TestCopyProgress_SingleFile(t *testing.T) {
	scope, docroot := funcScope(t)
	src := filepath.Join(docroot, "one.bin")
	if err := os.WriteFile(src, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	if c, err := scope.CountTreeBytesInScope(context.Background(), src); err != nil || c != 10 {
		t.Fatalf("count = %d err=%v, want 10", c, err)
	}
	var ticked int64
	dst := filepath.Join(docroot, "one-copy.bin")
	if _, err := scope.CopyTreeInScopeProgress(context.Background(), src, dst, os.Getuid(), os.Getgid(), func(d int64) { ticked += d }); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if ticked != 10 {
		t.Fatalf("ticks = %d, want 10", ticked)
	}
}

// GH #1392: a copy whose destination already exists must fail with an error
// that errors.Is(err, fs.ErrExist) — the async copy's rollback relies on this
// to know the copy created NOTHING and must NOT delete the pre-existing dst.
func TestCopyTree_ExistingDst_IsErrExist(t *testing.T) {
	scope, docroot := funcScope(t)
	src := filepath.Join(docroot, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(docroot, "dst")
	if err := os.MkdirAll(dst, 0o755); err != nil { // dst already present
		t.Fatal(err)
	}
	_, err := scope.CopyTreeInScope(context.Background(), src, dst, os.Getuid(), os.Getgid())
	if err == nil {
		t.Fatal("copy into an existing dst must fail")
	}
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("err = %v, want errors.Is fs.ErrExist (the rollback guard depends on it)", err)
	}
}

func TestRemoveAllInScope_RemovesPopulatedTree(t *testing.T) {
	scope, docroot := funcScope(t)
	tree := filepath.Join(docroot, "tree")
	if err := os.MkdirAll(filepath.Join(tree, "sub", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"x.txt", "sub/y.txt", "sub/deep/z.txt"} {
		if err := os.WriteFile(filepath.Join(tree, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("x.txt", filepath.Join(tree, "lnk")); err != nil {
		t.Fatal(err)
	}
	if err := scope.RemoveAllInScope(context.Background(), tree); err != nil {
		t.Fatalf("RemoveAllInScope: %v", err)
	}
	if _, err := os.Lstat(tree); !os.IsNotExist(err) {
		t.Errorf("tree should be gone, lstat err = %v", err)
	}
	if _, err := os.Lstat(docroot); err != nil {
		t.Errorf("docroot must survive: %v", err)
	}
	// Idempotent: removing again is success.
	if err := scope.RemoveAllInScope(context.Background(), tree); err != nil {
		t.Errorf("second RemoveAllInScope should be nil, got %v", err)
	}
}

func TestMkdirInScope_ParentsAndIdempotent(t *testing.T) {
	scope, docroot := funcScope(t)
	target := filepath.Join(docroot, "p", "q", "r")
	created, err := scope.MkdirInScope(target, true, 0o755)
	if err != nil || !created {
		t.Fatalf("MkdirInScope parents: created=%v err=%v", created, err)
	}
	if fi, err := os.Lstat(target); err != nil || !fi.IsDir() {
		t.Errorf("target dir not created: err=%v", err)
	}
	// Idempotent: same path again → created=false, no error.
	created, err = scope.MkdirInScope(target, true, 0o755)
	if err != nil || created {
		t.Errorf("second mkdir: created=%v err=%v (want false,nil)", created, err)
	}
	// Non-parents on a missing parent must fail.
	if _, err := scope.MkdirInScope(filepath.Join(docroot, "no", "deep"), false, 0o755); err == nil {
		t.Errorf("non-parents mkdir with missing parent should fail")
	}
}

func TestLeafPrimitives_CreateRenameReadStatList(t *testing.T) {
	scope, docroot := funcScope(t)

	// CreateExcl + write.
	f, err := scope.CreateExclInScope(filepath.Join(docroot, "tmp.txt"), 0o644)
	if err != nil {
		t.Fatalf("CreateExclInScope: %v", err)
	}
	if _, err := f.WriteString("hello"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	// EXCL: second create must fail.
	if _, err := scope.CreateExclInScope(filepath.Join(docroot, "tmp.txt"), 0o644); err == nil {
		t.Errorf("CreateExclInScope on existing path should fail (EEXIST)")
	}

	// Rename within scope.
	if err := scope.RenameInScope(filepath.Join(docroot, "tmp.txt"), filepath.Join(docroot, "final.txt")); err != nil {
		t.Fatalf("RenameInScope: %v", err)
	}

	// Read it back.
	rf, err := scope.OpenInScope(filepath.Join(docroot, "final.txt"), os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenInScope: %v", err)
	}
	buf := make([]byte, 16)
	n, _ := rf.Read(buf)
	rf.Close()
	if string(buf[:n]) != "hello" {
		t.Errorf("read = %q, want hello", string(buf[:n]))
	}

	// Stat.
	st, err := scope.StatInScope(filepath.Join(docroot, "final.txt"))
	if err != nil || st.IsDir || st.Size != 5 {
		t.Errorf("StatInScope = %+v err=%v", st, err)
	}

	// Exists: missing path returns (nil,nil).
	if li, err := scope.ExistsInScope(filepath.Join(docroot, "nope.txt")); err != nil || li != nil {
		t.Errorf("ExistsInScope(missing) = %v,%v want nil,nil", li, err)
	}

	// List.
	entries, err := scope.ReadDirInScope(docroot)
	if err != nil {
		t.Fatalf("ReadDirInScope: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "final.txt" {
		t.Errorf("ReadDirInScope = %+v, want [final.txt]", entries)
	}

	// Chmod via fd, then confirm.
	if err := scope.ChmodInScope(filepath.Join(docroot, "final.txt"), 0o600); err != nil {
		t.Fatalf("ChmodInScope: %v", err)
	}
	if fi, _ := os.Lstat(filepath.Join(docroot, "final.txt")); fi.Mode().Perm() != 0o600 {
		t.Errorf("perm after chmod = %o, want 600", fi.Mode().Perm())
	}
}
