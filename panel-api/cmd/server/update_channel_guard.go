package main

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Moving backwards on a release channel.
//
// `jabali update` resets the checkout to its channel's target first, and most
// of the update then runs from that tree: install.sh's provision_new_software,
// the systemd units and shims, the AppArmor profiles, the static assets, the
// ssh and sudoers helpers. The JAB-210 schema guard runs much later, right
// before the binary swap. So a box that had followed main (the development
// channel) and was switched to the stable channel while `stable` was behind
// it would, on every nightly run, reset the tree to the older `stable`,
// reinstall those older files, and only then stop at the schema guard. Its
// newer binaries kept running against older helper files, night after night,
// until `stable` caught up.
//
// updateResetTarget runs before the reset, so nothing has changed yet when it
// says no.

// gitOutFunc runs git in the panel checkout (as the service user in
// production) and returns its output.
type gitOutFunc func(args ...string) (string, error)

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

func gitSHA(gitOut gitOutFunc, args ...string) (string, error) {
	out, err := gitOut(args...)
	sha := strings.TrimSpace(out)
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, sha)
	}
	if !shaRe.MatchString(sha) {
		return "", fmt.Errorf("git %s: unexpected output %q", strings.Join(args, " "), sha)
	}
	return sha, nil
}

// refBehindHead reports whether ref is a strict ancestor of HEAD: resetting
// to it would move the checkout backwards. A ref that has diverged from HEAD
// is not behind it.
func refBehindHead(gitOut gitOutFunc, ref string) (bool, error) {
	refSHA, err := gitSHA(gitOut, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return false, err
	}
	headSHA, err := gitSHA(gitOut, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return false, err
	}
	if refSHA == headSHA {
		return false, nil
	}
	base, err := gitSHA(gitOut, "merge-base", refSHA, headSHA)
	if err != nil {
		return false, err
	}
	return base == refSHA, nil
}

// refMaxMigration returns the newest migration version in ref's tree, read
// with `git ls-tree` so the answer comes before the checkout moves.
func refMaxMigration(gitOut gitOutFunc, ref string) (uint, error) {
	const dir = "panel-api/internal/db/migrations/"
	out, err := gitOut("ls-tree", "--name-only", ref, dir)
	if err != nil {
		return 0, fmt.Errorf("git ls-tree %s %s: %v: %s", ref, dir, err, strings.TrimSpace(out))
	}
	var max uint
	for _, line := range strings.Split(out, "\n") {
		m := migrationFileRe.FindStringSubmatch(path.Base(strings.TrimSpace(line)))
		if m == nil {
			continue
		}
		if n, convErr := strconv.ParseUint(m[1], 10, 64); convErr == nil && uint(n) > max {
			max = uint(n)
		}
	}
	if max == 0 {
		return 0, fmt.Errorf("no migrations found under %s in %s", dir, ref)
	}
	return max, nil
}

// updateResetTarget decides what the update resets the checkout to, given the
// channel's target resetRef. liveSchema is the database's schema version (0
// when there is no database to ask, as on a dev checkout).
//
//   - On the stable channel, when `stable` is behind the build this server
//     already runs, it stays on the current build until a newer stable
//     release is promoted, the same as when no stable release exists yet.
//     note says so.
//   - On any channel, a target whose migrations stop short of the live schema
//     is refused before the reset, with nothing changed.
func updateResetTarget(gitOut gitOutFunc, resetRef string, followStable bool, liveSchema uint) (ref, note string, err error) {
	if resetRef == "HEAD" {
		return resetRef, "", nil
	}
	if followStable {
		behind, bErr := refBehindHead(gitOut, resetRef)
		if bErr != nil {
			return "", "", fmt.Errorf("compare the stable release with the current build: %w", bErr)
		}
		if behind {
			return "HEAD", "release channel: stable, but this server already runs a newer build than the `stable` release — staying on the current build until a newer stable release is promoted.", nil
		}
	}
	if liveSchema == 0 {
		return resetRef, "", nil
	}
	candidate, mErr := refMaxMigration(gitOut, resetRef)
	if mErr != nil {
		return "", "", fmt.Errorf("schema downgrade check: %w", mErr)
	}
	if candidate < liveSchema {
		return "", "", fmt.Errorf(
			"refusing to update: %s only knows migrations up to %d, but this server's database "+
				"is already at schema version %d. Moving to it would install older code than the "+
				"schema it has to serve. Nothing has been changed; the server stays on its current build",
			resetRef, candidate, liveSchema)
	}
	return resetRef, "", nil
}
