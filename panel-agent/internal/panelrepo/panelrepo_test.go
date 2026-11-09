package panelrepo

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func withOwner(t *testing.T, uid uint32) {
	t.Helper()
	prev := OwnerUID
	OwnerUID = func(string) (uint32, error) { return uid, nil }
	t.Cleanup(func() { OwnerUID = prev })
}

// GH #2467: on the root-owned checkout #2399 made, git runs as root, without
// system or global config and with hooks, fsmonitor and pager off.
func TestGitArgv_RootOwnedRunsAsRoot(t *testing.T) {
	withOwner(t, 0)
	got, err := GitArgv("/opt/jabali-panel", "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"env", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"git", "-C", "/opt/jabali-panel",
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=false",
		"-c", "core.pager=cat",
		"rev-parse", "HEAD",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv = %q\nwant  %q", got, want)
	}
}

// A checkout from before #2399 is the service user's: git runs as that user.
func TestGitArgv_ServiceUserOwnedRunsAsServiceUser(t *testing.T) {
	withOwner(t, 1000)
	got, err := GitArgv("/opt/jabali-panel", "status", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"sudo", "-u", "jabali", "-H", "git", "-C", "/opt/jabali-panel", "status", "--porcelain"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv = %q\nwant  %q", got, want)
	}
}

func TestGitArgv_NoCheckoutIsAnError(t *testing.T) {
	if _, err := GitArgv(t.TempDir(), "rev-parse", "HEAD"); err == nil {
		t.Error("GitArgv on a dir with no .git returned no error")
	}
}

// OwnerUID reads the owner of dir's .git.
func TestOwnerUID_ReadsGitDirOwner(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	uid, err := OwnerUID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if uid != uint32(os.Getuid()) {
		t.Errorf("OwnerUID = %d, want %d", uid, os.Getuid())
	}
}
