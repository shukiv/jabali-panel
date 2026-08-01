package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Release resolution that does not touch api.github.com.
//
// The API path costs two unauthenticated calls per update (GET /branches/main
// and a GET /releases scan). Unauthenticated GitHub API is 60 requests/hour
// PER IP, shared by every host behind the same egress — so on a NAT'd network,
// a CI runner, or a cloud provider's shared outbound address it runs out. When
// it does, fetchLatestMainSHA gets a 403, installFromRelease reports "no
// release available", and the update silently does a full source build instead:
//
//	{"message":"API rate limit exceeded for 181.79.81.248. ..."}
//	"limit": 60, "remaining": 0
//
// On a small host that is not merely slower — it is an update that cannot
// finish, because the frontend build exceeds the heap ceiling there. Observed
// on a 2 GB box whose release tarball had been published four minutes before
// the update ran: the artifact existed, the lookup just could not see it.
//
// bootstrap.sh already solved exactly this (reference: installer release
// resolution, 2026-07-21) by resolving through
// github.com/<owner>/<repo>/releases/latest/download/<asset>. That path is
// served by github.com and its release CDN, NOT api.github.com, so it does not
// consume the API quota. release.yml publishes fixed-name jabali-release.tar.gz
// and .sha256 assets alongside the sha-named pair precisely so that URL
// resolves. `jabali update` never got the same treatment.
//
// Correctness is not weakened by dropping the API. The old flow asked GitHub
// for main's HEAD and required the release to match it; but this step runs
// AFTER "git fetch + reset to release channel", so the local checkout already
// IS the channel tip — a more trustworthy answer than a network round-trip,
// and one that cannot be rate-limited. The tarball's MANIFEST still has to
// agree with it, so a publisher bug is caught exactly as before.

// defaultReleaseWebBase is the non-API origin used for release downloads.
// Overridable alongside JABALI_RELEASE_API_BASE for forks and mirrors.
const defaultReleaseWebBase = "https://github.com/shukiv/jabali-panel"

// latestDownloadURLs returns the fixed-name asset URLs GitHub redirects to the
// newest release. No API call, no quota.
func latestDownloadURLs(webBase string) (tarURL, sumURL string) {
	base := strings.TrimSuffix(webBase, "/") + "/releases/latest/download/"
	return base + "jabali-release.tar.gz", base + "jabali-release.tar.gz.sha256"
}

// localHeadSHA reads the commit the working tree is actually on.
//
// Used instead of GET /branches/main. The update resets the repo to the
// release channel earlier in the run, so this is the same value the API would
// return — except it is free, offline, and immune to rate limiting.
func localHeadSHA(ctx context.Context, repoDir string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", repoDir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD in %s: %w", repoDir, err)
	}
	sha := strings.TrimSpace(string(out))
	if len(sha) < releaseShortSHALen {
		return "", fmt.Errorf("git rev-parse returned an implausible HEAD %q", sha)
	}
	return sha, nil
}
