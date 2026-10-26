package filesafe

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// GH #2277 (ADR-0178): a Git-deployed domain's document root is
// domains/<domain>/current/<web dir>, where current is a relative link to
// releases/<id>. The agent's domain.create provisions a document root with
// MkdirInScope and opens each directory up to the home with OpenDirInScope,
// so both must follow a relative link that stays inside the home. Adding
// RESOLVE_NO_SYMLINKS would break every deployed site's vhost.
func TestScope_DocrootThroughADeploymentsCurrentLink(t *testing.T) {
	if _, err := unix.Openat2(unix.AT_FDCWD, ".", &unix.OpenHow{Flags: unix.O_RDONLY, Resolve: unix.RESOLVE_BENEATH}); err != nil && errors.Is(err, unix.ENOSYS) {
		t.Skip("openat2 not supported on this kernel")
	}
	home := t.TempDir()
	domain := filepath.Join(home, "domains", "example.com")
	release := filepath.Join(domain, "releases", "01JBX000000000000000000005")
	if err := os.MkdirAll(filepath.Join(release, "public"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("releases", "01JBX000000000000000000005"), filepath.Join(domain, "current")); err != nil {
		t.Fatal(err)
	}
	scope := NewScopeForTest("u1", "u1", home)

	for _, docroot := range []string{filepath.Join(domain, "current", "public"), filepath.Join(domain, "current")} {
		created, err := scope.MkdirInScope(docroot, true, 0o755)
		if err != nil {
			t.Fatalf("MkdirInScope(%s): %v", docroot, err)
		}
		if created {
			t.Errorf("MkdirInScope(%s) created a directory over the release's", docroot)
		}
	}

	// current opens as the release itself.
	f, err := scope.OpenDirInScope(filepath.Join(domain, "current"))
	if err != nil {
		t.Fatalf("OpenDirInScope(current): %v", err)
	}
	defer f.Close()
	got, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.Stat(release)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sys().(*syscall.Stat_t).Ino != want.Sys().(*syscall.Stat_t).Ino {
		t.Error("current did not open as the release")
	}
	if fi, err := os.Lstat(filepath.Join(domain, "current")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("current is no longer a link: %v %v", fi, err)
	}

	// A current pointing out of the home is still refused.
	if err := os.Remove(filepath.Join(domain, "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../../..", filepath.Join(domain, "current")); err != nil {
		t.Fatal(err)
	}
	if _, err := scope.OpenDirInScope(filepath.Join(domain, "current")); err == nil {
		t.Error("a current link out of the home was followed")
	}
}
