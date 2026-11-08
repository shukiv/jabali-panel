package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func withSnippetPruneDirs(t *testing.T) (jabali, sites string) {
	t.Helper()
	prevJ, prevS := nginxJabaliDir, nginxSitesDir
	nginxJabaliDir = filepath.Join(t.TempDir(), "jabali")
	nginxSitesDir = filepath.Join(t.TempDir(), "sites-available")
	t.Cleanup(func() { nginxJabaliDir, nginxSitesDir = prevJ, prevS })
	for _, d := range []string{nginxJabaliDir, nginxSitesDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return nginxJabaliDir, nginxSitesDir
}

func touchSnippetPruneFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("# x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func runSnippetPrune(t *testing.T, keep ...string) (snippetDirsPruneResult, error) {
	t.Helper()
	raw, _ := json.Marshal(snippetDirsPruneParams{Keep: keep})
	out, err := snippetDirsPruneHandler(context.Background(), raw)
	if err != nil {
		return snippetDirsPruneResult{}, err
	}
	return out.(snippetDirsPruneResult), nil
}

// Only the dir of a name the panel does not have, with no vhost, goes. A name
// the panel has stays even without a vhost (its vhost may be failing to
// render, or its web teardown may have run); a name with a vhost stays even
// without a row (an orphan site is never removed on a guess); a dir that is
// not a domain name, a file and a symlink stay.
func TestSnippetDirsPrune_RemovesOnlyTheDirsOfNamesThatAreGone(t *testing.T) {
	jabali, sites := withSnippetPruneDirs(t)
	touchSnippetPruneFiles(t, sites, "live.test.conf", "orphan.test.conf")
	for _, d := range []string{"live.test", "novhost.test", "orphan.test", "gone.test", "Not_A_Domain"} {
		touchSnippetPruneFiles(t, filepath.Join(jabali, d), "wordpress-xmlrpc.conf", "php-pins.params")
	}
	if err := os.WriteFile(filepath.Join(jabali, "file.test"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	touchSnippetPruneFiles(t, elsewhere, "keep.conf")
	if err := os.Symlink(elsewhere, filepath.Join(jabali, "link.test")); err != nil {
		t.Fatal(err)
	}

	res, err := runSnippetPrune(t, "live.test", "novhost.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 1 || res.Removed[0] != "gone.test" || len(res.Failed) != 0 {
		t.Fatalf("removed %v failed %v, want only gone.test", res.Removed, res.Failed)
	}
	if _, err := os.Lstat(filepath.Join(jabali, "gone.test")); !os.IsNotExist(err) {
		t.Fatalf("gone.test left behind (err=%v)", err)
	}
	for _, kept := range []string{"live.test", "novhost.test", "orphan.test", "Not_A_Domain", "file.test", "link.test"} {
		if _, err := os.Lstat(filepath.Join(jabali, kept)); err != nil {
			t.Fatalf("%s was removed: %v", kept, err)
		}
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "keep.conf")); err != nil {
		t.Fatalf("the prune reached through a symlink: %v", err)
	}
}

// An empty list, or no sites-available to check against, would make every dir
// look gone: both are refused and remove nothing.
func TestSnippetDirsPrune_RefusesWhatWouldMakeEveryDirLookGone(t *testing.T) {
	jabali, sites := withSnippetPruneDirs(t)
	touchSnippetPruneFiles(t, filepath.Join(jabali, "gone.test"), "wordpress-xmlrpc.conf")

	var ae *agentwire.AgentError
	if _, err := runSnippetPrune(t); !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument {
		t.Fatalf("empty keep: want InvalidArgument, got %v", err)
	}
	if err := os.Remove(sites); err != nil {
		t.Fatal(err)
	}
	if _, err := runSnippetPrune(t, "live.test"); !errors.As(err, &ae) || ae.Code != agentwire.CodeFailedPrecondition {
		t.Fatalf("no sites-available: want FailedPrecondition, got %v", err)
	}
	if _, err := os.Lstat(filepath.Join(jabali, "gone.test")); err != nil {
		t.Fatalf("a refused prune removed a dir: %v", err)
	}
}

// The prune changes /etc/nginx/jabali, so it waits for every other nginx op.
func TestSnippetDirsPrune_WaitsForTheNginxLock(t *testing.T) {
	withSnippetPruneDirs(t)
	nginxOpMu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := runSnippetPrune(t, "live.test")
		done <- err
	}()
	select {
	case err := <-done:
		nginxOpMu.Unlock()
		t.Fatalf("the prune finished (err %v) while another nginx op held the lock", err)
	case <-time.After(100 * time.Millisecond):
	}
	nginxOpMu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
