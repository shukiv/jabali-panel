package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// The git step and the release step must agree on what is trusted.
//
// `jabali update` resets the checkout to the channel's commit and then runs
// install.sh functions, unit files and templates from that checkout as root,
// long before it installs a single binary. Signing only the tarball would
// leave all of that unverified: whoever can push to the repository could
// change install.sh, and every host would run it on its next update.
//
// So on a build that carries the release key, the commit is verified BEFORE it
// becomes the working tree. The tarball's MANIFEST names the commit it was built
// from (full_sha), and the signature covers the MANIFEST along with everything
// else in the tarball. A signed release whose MANIFEST names the reset target
// therefore vouches for that commit, and the commit ID covers every tracked
// file in it. One key, one signature per release.

// releaseStageParent is where release tarballs are downloaded and extracted.
const releaseStageParent = "/var/lib/jabali-panel"

// stagedRelease is a release tarball that has been downloaded, checked against
// its sha256 and its signature, extracted, and confirmed by its MANIFEST to be
// the build of commit sha.
type stagedRelease struct {
	sha        string
	extractDir string
	root       string
}

// cleanup removes the staging directory. Safe on a nil receiver.
func (s *stagedRelease) cleanup() {
	if s != nil && s.root != "" {
		_ = os.RemoveAll(s.root)
	}
}

// stageSignedRelease fetches and verifies the release built from fullSHA.
//
// It returns (nil, nil) when that release cannot be installed yet and the host
// should stay on its current build: nothing is published for the commit, or it
// is published but its .minisig is not there yet (the window between CI
// publishing and the release being signed). Nothing is downloaded in either
// case.
//
// It returns an error when a release exists and fails a check: checksum,
// signature, or a MANIFEST that names a different commit. A signed tarball for
// the wrong commit is not a near miss. It is either a publishing mistake or an
// old signed release served under a new name, and both must stop the update.
func stageSignedRelease(ctx context.Context, webBase, pubKey, fullSHA, stageParent string,
	head func(context.Context, string) bool, log func(string, ...any)) (*stagedRelease, error) {
	tarURL, sumURL, tag, ok := byCommitDownloadURLs(ctx, webBase, fullSHA, head)
	if !ok {
		log("release: no release is published for %s yet", shortSHA7(fullSHA))
		return nil, nil
	}
	sigURL := releaseSignatureURL(tarURL)
	if !head(ctx, sigURL) {
		log("release: %s is published but not signed yet", tag)
		return nil, nil
	}

	root, err := os.MkdirTemp(stageParent, "release-stage-*")
	if err != nil {
		return nil, fmt.Errorf("release: mkdir staging: %w", err)
	}
	staged := &stagedRelease{sha: fullSHA, root: root}
	ok = false
	defer func() {
		if !ok {
			staged.cleanup()
		}
	}()

	tarPath := filepath.Join(root, filepath.Base(tarURL))
	if err := downloadTo(ctx, tarURL, tarPath, releaseDownloadTimeout); err != nil {
		return nil, fmt.Errorf("release: download %s: %w", tag, err)
	}
	sumPath := tarPath + ".sha256"
	if err := downloadTo(ctx, sumURL, sumPath, releaseAPITimeout); err != nil {
		return nil, fmt.Errorf("release: download %s checksum: %w", tag, err)
	}
	if err := verifyTarballChecksum(tarPath, sumPath); err != nil {
		return nil, fmt.Errorf("release: %s checksum verification failed: %w", tag, err)
	}
	if err := verifyReleaseSignatureWith(ctx, pubKey, tarPath, sigURL, log); err != nil {
		return nil, fmt.Errorf("release: %s: %w", tag, err)
	}

	staged.extractDir = filepath.Join(root, "extracted")
	if err := os.Mkdir(staged.extractDir, 0o755); err != nil {
		return nil, fmt.Errorf("release: mkdir extract: %w", err)
	}
	if err := extractTarball(ctx, tarPath, staged.extractDir); err != nil {
		return nil, fmt.Errorf("release: extract %s: %w", tag, err)
	}
	manifest, err := parseManifest(filepath.Join(staged.extractDir, "MANIFEST"))
	if err != nil {
		return nil, fmt.Errorf("release: parse %s MANIFEST: %w", tag, err)
	}
	if manifest["full_sha"] != fullSHA {
		return nil, fmt.Errorf("release: %s is signed but is the build of %q, not %s — refusing it",
			tag, manifest["full_sha"], fullSHA)
	}
	log("release: %s verified for %s (build_time=%s)", tag, shortSHA7(fullSHA), manifest["build_time"])
	ok = true
	return staged, nil
}

// verifyResetTarget decides whether the git step may reset the checkout to
// targetSHA.
//
//   - No key in this build, or the target is the commit already checked out:
//     nothing new would come from git, so there is nothing to verify
//     (nil, false, nil).
//   - A signed release for the target exists and verifies: reset to it, and
//     install that same tarball later (staged, false, nil).
//   - No signed release for the target yet: do not reset; stay on the
//     current build until one is published (nil, true, nil).
//   - The target is older than the build this host runs: stay
//     (nil, true, nil). A signature says a build is genuine, not that it is
//     current, so an old signed release must not be usable to move a host
//     back onto code with fixed holes. behind reports whether the target is an
//     ancestor of the checked-out commit.
//   - Anything else: stop the update (nil, false, err).
func verifyResetTarget(ctx context.Context, pubKey, webBase, stageParent, targetSHA, currentSHA string,
	behind func(targetSHA string) (bool, error), head func(context.Context, string) bool,
	log func(string, ...any)) (staged *stagedRelease, stay bool, err error) {
	if pubKey == "" || targetSHA == currentSHA {
		return nil, false, nil
	}
	older, err := behind(targetSHA)
	if err != nil {
		return nil, false, fmt.Errorf("compare %s with the current build: %w", shortSHA7(targetSHA), err)
	}
	if older {
		log("release: %s is older than the build this server runs (%s) — not moving backwards",
			shortSHA7(targetSHA), shortSHA7(currentSHA))
		return nil, true, nil
	}
	staged, err = stageSignedRelease(ctx, webBase, pubKey, targetSHA, stageParent, head, log)
	if err != nil {
		return nil, false, err
	}
	if staged == nil {
		return nil, true, nil
	}
	return staged, false, nil
}
