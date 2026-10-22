package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

// newReleaseCmd groups release-channel management (GH #445).
func newReleaseCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "release",
		Short: "Release-channel management (stable/development)",
	}
	c.AddCommand(newReleasePromoteCmd())
	return c
}

// newReleasePromoteCmd force-moves the movable `stable` tag to a reviewed
// commit and pushes it. Stable-channel hosts pick it up on their next
// `jabali update`. Run where there's push access to origin (maintainer/CI),
// not necessarily on a panel host.
func newReleasePromoteCmd() *cobra.Command {
	var allowSourceBuild bool
	c := &cobra.Command{
		Use:   "promote [<commit-ish>]",
		Short: "Promote a reviewed build to the stable channel (move the `stable` tag)",
		Long: `Force-moves the ` + "`stable`" + ` git tag to <commit-ish> (default: the
current origin/main HEAD) and pushes it. Stable-channel hosts then update to
that reviewed build on their next ` + "`jabali update`" + `.

Run on a box with push access to origin (a maintainer machine or CI) — the
panel host is a read-only deployment target. The release workflow builds a
release-<sha> tarball for every main commit, and release-hotfix.yml for every
commit pushed to a hotfix/ branch; stable hosts download the promoted commit's
tarball. Promote refuses a commit whose tarball is not published yet: every
stable host would build it from source instead, which takes minutes on each.
--allow-source-build promotes it anyway.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoDir := os.Getenv("JABALI_REPO_DIR")
			if repoDir == "" {
				repoDir = defaultRepoDir
			}
			git := func(args ...string) error {
				full := append([]string{"-C", repoDir}, args...)
				c := exec.Command("git", full...)
				c.Stdout, c.Stderr = os.Stdout, os.Stderr
				return c.Run()
			}
			target := "origin/main"
			if len(args) == 1 {
				target = args[0]
			}
			// Refresh main so the default target is the real current HEAD.
			_ = git("fetch", "origin", "main")
			fullOut, err := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", target+"^{commit}").Output()
			if err != nil {
				return fmt.Errorf("resolve %q: %w (is it a valid commit?)", target, err)
			}
			fullSHA := strings.TrimSpace(string(fullOut))
			shaOut, err := exec.Command("git", "-C", repoDir, "rev-parse", "--short", fullSHA).Output()
			if err != nil {
				return fmt.Errorf("resolve %q: %w (is it a valid commit?)", target, err)
			}
			sha := strings.TrimSpace(string(shaOut))
			if allowSourceBuild {
				fmt.Printf("  --allow-source-build: not checking for a release tarball; stable hosts without one build %s from source\n", sha)
			} else {
				ctx := cmd.Context()
				if ctx == nil {
					ctx = context.Background()
				}
				webBase := os.Getenv("JABALI_RELEASE_WEB_BASE")
				if webBase == "" {
					webBase = defaultReleaseWebBase
				}
				tag, err := promoteReleaseCheck(ctx, webBase, fullSHA, urlExists)
				if err != nil {
					return err
				}
				fmt.Printf("  release %s is published for %s\n", tag, sha)
			}
			if err := git("tag", "-f", "stable", fullSHA); err != nil {
				return fmt.Errorf("move stable tag: %w", err)
			}
			if err := git("push", "-f", "origin", "refs/tags/stable"); err != nil {
				return fmt.Errorf("push stable tag (need push access to origin): %w", err)
			}
			fmt.Printf("\n✓ Promoted stable -> %s (%s). Stable-channel hosts will update to it on next `jabali update`.\n", sha, target)
			return nil
		},
	}
	c.Flags().BoolVar(&allowSourceBuild, "allow-source-build", false,
		"promote even when no release tarball is published for the commit (stable hosts then build it from source)")
	return c
}

// promoteReleaseCheck returns the tag of the release published for fullSHA,
// found the way a stable host finds it on `jabali update`
// (byCommitDownloadURLs), or an error when there is none yet.
func promoteReleaseCheck(ctx context.Context, webBase, fullSHA string, head func(context.Context, string) bool) (string, error) {
	if _, _, tag, ok := byCommitDownloadURLs(ctx, webBase, fullSHA, head); ok {
		return tag, nil
	}
	short := fullSHA
	if len(short) > 9 {
		short = short[:9]
	}
	return "", fmt.Errorf("no release tarball is published for %s yet, so every stable host would build it from source, "+
		"which takes minutes on each. Wait for its release build to finish (release.yml for a main commit, "+
		"release-hotfix.yml for a hotfix/ branch) and run this again, or pass --allow-source-build", short)
}
