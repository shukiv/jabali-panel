package main

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// TestLatestDownloadURLs pins the exact shape that keeps release resolution off
// api.github.com.
//
// The distinction is the whole point: api.github.com enforces 60 unauthenticated
// requests per hour PER IP, and github.com/.../releases/latest/download/ does
// not. When the quota ran out, the update silently fell back to a full source
// build — on a 2 GB host, one that cannot finish. A well-meaning "tidy-up" that
// routes these through the API would restore that failure invisibly, since
// everything still works until an IP happens to be over quota.
func TestLatestDownloadURLs(t *testing.T) {
	t.Parallel()

	tarURL, sumURL := latestDownloadURLs("https://github.com/shukiv/jabali-panel")

	if strings.Contains(tarURL, "api.github.com") || strings.Contains(sumURL, "api.github.com") {
		t.Errorf("release URLs must not use the API host (60 req/hour/IP):\n  %s\n  %s", tarURL, sumURL)
	}
	const wantTar = "https://github.com/shukiv/jabali-panel/releases/latest/download/jabali-release.tar.gz"
	if tarURL != wantTar {
		t.Errorf("tar URL = %q, want %q", tarURL, wantTar)
	}
	if sumURL != wantTar+".sha256" {
		t.Errorf("sum URL = %q, want the tarball URL plus .sha256", sumURL)
	}
}

// TestLatestDownloadURLs_TrailingSlash covers an override supplied with a
// trailing slash, which would otherwise produce a double slash in the path.
func TestLatestDownloadURLs_TrailingSlash(t *testing.T) {
	t.Parallel()

	tarURL, _ := latestDownloadURLs("https://example.test/org/repo/")
	if strings.Contains(tarURL, "repo//releases") {
		t.Errorf("double slash in %q", tarURL)
	}
	if !strings.HasPrefix(tarURL, "https://example.test/org/repo/releases/latest/download/") {
		t.Errorf("unexpected URL for an overridden base: %q", tarURL)
	}
}

// TestLocalHeadSHA reads the commit from a real throwaway repo, because the
// point of this helper is replacing a network lookup with the local truth — a
// stubbed test would not show that `git rev-parse` behaves as assumed.
func TestLocalHeadSHA(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	ctx := context.Background()

	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "seed"},
	} {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	got, err := localHeadSHA(ctx, dir)
	if err != nil {
		t.Fatalf("localHeadSHA: %v", err)
	}
	if len(got) != 40 {
		t.Errorf("HEAD = %q, want a 40-char SHA", got)
	}
	if strings.ContainsAny(got, " \n\t") {
		t.Errorf("HEAD %q still has whitespace — it is used to build a URL and compared to MANIFEST", got)
	}
}

// TestLocalHeadSHA_NotARepo must fail rather than return something empty: the
// caller uses the error to fall back to the API path, and an empty SHA would
// instead be compared against the MANIFEST and mismatch confusingly.
func TestLocalHeadSHA_NotARepo(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if sha, err := localHeadSHA(context.Background(), t.TempDir()); err == nil {
		t.Errorf("expected an error outside a git repo, got sha=%q", sha)
	}
}
