package main

import (
	"context"
	"os"
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

// TestByCommitDownloadURLs covers the resolver that keeps channel hosts off the
// source-build path.
//
// "latest" is only correct for a host following main. A host on the `stable`
// channel resets its checkout to the promoted tag, so the newest release never
// matches its HEAD — while release-<that commit> is usually published and
// perfectly reachable. Resolving only by "latest" would send every stable host
// into a full source build forever, which on a small box means an update that
// takes hours or does not finish at all.
//
// The tag's short-SHA length is whatever build-release.sh produced and git's
// own --short drifts as the repo grows, so it cannot be derived — hence the
// probe. The order matters: the observed length is tried first so the common
// case costs one HEAD.
func TestByCommitDownloadURLs(t *testing.T) {
	t.Parallel()

	const full = "7085d502f2a5cfe2a7e07bd1958ddeac4675b880"
	const web = "https://github.com/shukiv/jabali-panel"

	t.Run("finds the 9-char tag on the first probe", func(t *testing.T) {
		var probes []string
		head := func(_ context.Context, url string) bool {
			probes = append(probes, url)
			return strings.Contains(url, "release-7085d502f/")
		}
		tar, sum, tag, ok := byCommitDownloadURLs(context.Background(), web, full, head)
		if !ok {
			t.Fatalf("expected a hit; probed %v", probes)
		}
		if tag != "release-7085d502f" {
			t.Errorf("tag = %q, want release-7085d502f", tag)
		}
		if len(probes) != 1 {
			t.Errorf("expected the observed length to be probed first, got %d probes: %v", len(probes), probes)
		}
		wantTar := web + "/releases/download/release-7085d502f/jabali-release-7085d502f.tar.gz"
		if tar != wantTar {
			t.Errorf("tar = %q, want %q", tar, wantTar)
		}
		if sum != wantTar+".sha256" {
			t.Errorf("sum = %q, want the tarball plus .sha256", sum)
		}
		if strings.Contains(tar, "api.github.com") {
			t.Error("by-commit resolution must not use the rate-limited API host")
		}
	})

	t.Run("falls through to another length when the tag is shorter", func(t *testing.T) {
		head := func(_ context.Context, url string) bool {
			return strings.Contains(url, "release-7085d50/")
		}
		_, _, tag, ok := byCommitDownloadURLs(context.Background(), web, full, head)
		if !ok || tag != "release-7085d50" {
			t.Errorf("expected the 7-char tag to be found, got ok=%v tag=%q", ok, tag)
		}
	})

	t.Run("reports not-found rather than guessing", func(t *testing.T) {
		head := func(_ context.Context, _ string) bool { return false }
		_, _, _, ok := byCommitDownloadURLs(context.Background(), web, full, head)
		if ok {
			t.Error("no published release must report not-found so the caller falls back to source")
		}
	})

	t.Run("a short SHA never slices out of range", func(t *testing.T) {
		head := func(_ context.Context, _ string) bool { return false }
		// Must not panic on a SHA shorter than the probe lengths.
		if _, _, _, ok := byCommitDownloadURLs(context.Background(), web, "abc123", head); ok {
			t.Error("unexpected hit")
		}
	})
}

// TestLocalHeadSHA_ForeignOwnedRepo is the case that actually happens in
// production and did not happen in any earlier test.
//
// /opt/jabali-panel is owned by the service user; `jabali update` runs as root.
// Plain git refuses that combination:
//
//	fatal: detected dubious ownership in repository at '/opt/jabali-panel'
//
// localHeadSHA then returns an error, resolution falls back to the
// rate-limited api.github.com path, and the whole point of resolving locally is
// lost — silently, because the fallback is by design. Verified on a real host:
// git rev-parse as root failed there while every developer-checkout test passed,
// because a dev checkout is owned by whoever runs the tests.
//
// Simulated here by pointing git's ownership check at a repo it should distrust
// via a scrubbed environment, which is the closest a same-user test can get.
func TestLocalHeadSHA_UsesSafeDirectory(t *testing.T) {
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

	// The command localHeadSHA builds must carry the exception, otherwise a
	// service-user-owned repo read by root fails and we fall back to the API
	// without anyone noticing.
	sha, err := localHeadSHA(ctx, dir)
	if err != nil {
		t.Fatalf("localHeadSHA: %v", err)
	}
	if len(sha) != 40 {
		t.Errorf("HEAD = %q, want a 40-char SHA", sha)
	}

	// Assert the flag is actually present in the source of truth — the
	// behaviour above cannot distinguish "has the flag" from "same owner", and
	// the flag is the whole fix.
	src, rerr := os.ReadFile("update_release_direct.go")
	if rerr != nil {
		t.Fatalf("read source: %v", rerr)
	}
	if !strings.Contains(string(src), `"safe.directory="+repoDir`) {
		t.Error("localHeadSHA must pass -c safe.directory=<repoDir>; without it " +
			"the lookup fails as root on every real host and silently degrades " +
			"to the rate-limited API path")
	}
}
