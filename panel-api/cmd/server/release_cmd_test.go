package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// promoteRepo is a checkout with one commit, pushed to a local bare origin.
// It returns the checkout, the origin and the commit's full SHA.
func promoteRepo(t *testing.T) (work, origin, sha string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	work, origin = filepath.Join(root, "work"), filepath.Join(root, "origin.git")
	run := func(dir string, args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	for _, d := range []string{work, origin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	run(origin, "init", "-q", "--bare", "-b", "main")
	run(work, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "f"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "add", "f")
	run(work, "commit", "-q", "-m", "fix")
	run(work, "remote", "add", "origin", origin)
	run(work, "push", "-q", "origin", "main")
	return work, origin, run(work, "rev-parse", "HEAD")
}

// releaseServer answers the probes a stable host makes for a release: 200 for
// the sidecar of release-<sha[:9]> when published, 404 for everything else.
func releaseServer(t *testing.T, sha string, published bool) *httptest.Server {
	t.Helper()
	want := "/releases/download/release-" + sha[:9] + "/jabali-release-" + sha[:9] + ".tar.gz.sha256"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if published && r.URL.Path == want {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func originStable(origin string) string {
	out, err := exec.Command("git", "-C", origin, "rev-parse", "-q", "--verify", "refs/tags/stable^{commit}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func runPromote(t *testing.T, args ...string) error {
	t.Helper()
	c := newReleasePromoteCmd()
	c.SetArgs(args)
	c.SetOut(io.Discard)
	c.SetErr(io.Discard)
	c.SilenceUsage = true
	return c.Execute()
}

// A commit with no published release tarball would send every stable host to
// a source build, so promote refuses it and leaves `stable` where it was.
func TestReleasePromote_RefusesCommitWithoutRelease(t *testing.T) {
	work, origin, sha := promoteRepo(t)
	t.Setenv("JABALI_REPO_DIR", work)
	t.Setenv("JABALI_RELEASE_WEB_BASE", releaseServer(t, sha, false).URL)

	err := runPromote(t, sha)
	if err == nil {
		t.Fatal("promote succeeded with no release published")
	}
	if !strings.Contains(err.Error(), "--allow-source-build") {
		t.Errorf("error does not name the override: %v", err)
	}
	if got := originStable(origin); got != "" {
		t.Errorf("stable was pushed to %s although promote refused", got)
	}
}

func TestReleasePromote_PromotesPublishedRelease(t *testing.T) {
	work, origin, sha := promoteRepo(t)
	t.Setenv("JABALI_REPO_DIR", work)
	t.Setenv("JABALI_RELEASE_WEB_BASE", releaseServer(t, sha, true).URL)

	if err := runPromote(t, sha); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if got := originStable(origin); got != sha {
		t.Errorf("origin stable = %q, want %s", got, sha)
	}
}

func TestReleasePromote_AllowSourceBuild(t *testing.T) {
	work, origin, sha := promoteRepo(t)
	t.Setenv("JABALI_REPO_DIR", work)
	t.Setenv("JABALI_RELEASE_WEB_BASE", releaseServer(t, sha, false).URL)

	if err := runPromote(t, "--allow-source-build", sha); err != nil {
		t.Fatalf("promote --allow-source-build: %v", err)
	}
	if got := originStable(origin); got != sha {
		t.Errorf("origin stable = %q, want %s", got, sha)
	}
}

func TestPromoteReleaseCheck(t *testing.T) {
	t.Parallel()
	const full = "7085d502f2a5cfe2a7e07bd1958ddeac4675b880"
	found := func(_ context.Context, url string) bool { return strings.Contains(url, "release-7085d502f2/") }
	if tag, err := promoteReleaseCheck(context.Background(), "https://example.invalid", full, found); err != nil || tag != "release-7085d502f2" {
		t.Errorf("published release: tag=%q err=%v, want release-7085d502f2", tag, err)
	}
	none := func(context.Context, string) bool { return false }
	_, err := promoteReleaseCheck(context.Background(), "https://example.invalid", full, none)
	if err == nil || !strings.Contains(err.Error(), "7085d502f") {
		t.Errorf("no release: err=%v, want an error naming the commit", err)
	}
}
