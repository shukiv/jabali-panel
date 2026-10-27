package commands

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// GH #2303: restoring one domain's files writes into ~/domains/<domain>
// only, reached without following a link, and with delete_extra the folder
// ends up holding exactly what the backup holds.

func domainRestoreFixture(t *testing.T) (src, home string) {
	t.Helper()
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not installed")
	}
	// TestMain stubs every command; these tests run the real rsync, on
	// temporary folders only, and nothing else.
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name != "rsync" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		return exec.CommandContext(ctx, name, args...)
	}
	t.Cleanup(func() { execCommandContext = prev })
	src = filepath.Join(t.TempDir(), "staged") + "/"
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	drWrite(t, filepath.Join(src, "index.php"), "backup")
	drWrite(t, filepath.Join(src, "sub", "page.html"), "backup")
	home = t.TempDir()
	return src, home
}

func drWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func drRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func TestRestoreDomainFiles_AddsAndKeeps(t *testing.T) {
	src, home := domainRestoreFixture(t)
	dir := filepath.Join(home, "domains", "example.com")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	drWrite(t, filepath.Join(dir, "index.php"), "live")
	drWrite(t, filepath.Join(dir, "added.txt"), "added since")

	if err := restoreDomainFiles(context.Background(), src, home, "example.com", os.Getuid(), os.Getgid(), false); err != nil {
		t.Fatal(err)
	}
	if got := drRead(t, filepath.Join(dir, "index.php")); got != "backup" {
		t.Errorf("index.php = %q, want the backup's", got)
	}
	if got := drRead(t, filepath.Join(dir, "sub", "page.html")); got != "backup" {
		t.Errorf("sub/page.html = %q, want the backup's", got)
	}
	if got := drRead(t, filepath.Join(dir, "added.txt")); got != "added since" {
		t.Error("a file added since the backup was removed without delete_extra")
	}
}

func TestRestoreDomainFiles_DeleteExtra(t *testing.T) {
	src, home := domainRestoreFixture(t)
	dir := filepath.Join(home, "domains", "example.com")
	other := filepath.Join(home, "domains", "other.com")
	for _, d := range []string{dir, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	drWrite(t, filepath.Join(dir, "added.txt"), "added since")
	drWrite(t, filepath.Join(other, "keep.txt"), "other domain")

	if err := restoreDomainFiles(context.Background(), src, home, "example.com", os.Getuid(), os.Getgid(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "added.txt")); !os.IsNotExist(err) {
		t.Error("delete_extra kept a file added since the backup")
	}
	if got := drRead(t, filepath.Join(dir, "index.php")); got != "backup" {
		t.Errorf("index.php = %q, want the backup's", got)
	}
	if got := drRead(t, filepath.Join(other, "keep.txt")); got != "other domain" {
		t.Error("delete_extra reached another domain's folder")
	}
}

func TestRestoreDomainFiles_CreatesMissingFolder(t *testing.T) {
	src, home := domainRestoreFixture(t)
	if err := restoreDomainFiles(context.Background(), src, home, "example.com", os.Getuid(), os.Getgid(), false); err != nil {
		t.Fatal(err)
	}
	if got := drRead(t, filepath.Join(home, "domains", "example.com", "index.php")); got != "backup" {
		t.Errorf("index.php = %q, want the backup's", got)
	}
}

// A link where the domain's folder should be is refused, and nothing is
// written behind it.
func TestRestoreDomainFiles_RefusesALink(t *testing.T) {
	src, home := domainRestoreFixture(t)
	elsewhere := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "domains"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(home, "domains", "example.com")); err != nil {
		t.Fatal(err)
	}
	if err := restoreDomainFiles(context.Background(), src, home, "example.com", os.Getuid(), os.Getgid(), true); err == nil {
		t.Fatal("restored through a link")
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Errorf("files were written behind the link: %v", entries)
	}
}
