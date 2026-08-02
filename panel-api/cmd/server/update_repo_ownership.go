package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// repoOwnershipReport is what a self-heal pass found and fixed.
type repoOwnershipReport struct {
	Scanned int
	Fixed   []string
}

// fixTrackedFileOwnership chowns TRACKED working-tree files that are not owned
// by the service user.
//
// Why this exists: every git command in the updater runs as the service user
// (`sudo -u jabali git …`), but the repo files are mode 0640/0660 — no world
// read. So one tracked file left owned by root is enough to break git commands
// that have to hash the working tree:
//
//	error: open("CLAUDE.md"): Permission denied
//	fatal: cannot hash CLAUDE.md
//
// Seen on a fresh install. It is survivable — `git reset --hard` then unlinks
// and recreates the file, because the parent directory IS writable by the
// service user — but until that point the update prints a bare git `fatal:`
// with no explanation, and any git operation that reads rather than replaces
// the file fails outright.
//
// The existing self-heal (both here and install.sh) chowned only `.git`. That
// covered the known case — a hand-run `git fetch` as root leaving root-owned
// FETCH_HEAD/objects — and missed the working tree entirely.
//
// SCOPE IS DELIBERATE: only files git tracks. install.sh's chown carries the
// note "so node_modules or other trees that may legitimately be group-owned
// differently don't get clobbered", and that reasoning is right — a blanket
// `chown -R` over the repo would take ownership of node_modules, build caches
// and anything else living there. Tracked files are exactly the set git needs
// to read and exactly the set `git reset --hard` will rewrite anyway.
func fixTrackedFileOwnership(ctx context.Context, repoDir, serviceUser string) (repoOwnershipReport, error) {
	var rep repoOwnershipReport

	u, err := user.Lookup(serviceUser)
	if err != nil {
		return rep, fmt.Errorf("look up service user %q: %w", serviceUser, err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return rep, fmt.Errorf("service user %q has a non-numeric uid %q", serviceUser, u.Uid)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return rep, fmt.Errorf("service user %q has a non-numeric gid %q", serviceUser, u.Gid)
	}

	// Run as root: enumerating the index must not itself trip over the
	// permissions we are here to repair.
	//
	// -c safe.directory is required, not defensive — same reason as
	// localHeadSHA (#841). The checkout is owned by the service user and this
	// runs as root, so git's dubious-ownership guard refuses the repo with
	// exit 128 and the repair silently does nothing. Verified on a box: without
	// it this step reported "list tracked files: exit status 128" and left the
	// root-owned file in place.
	cmd := exec.CommandContext(ctx, "git",
		"-c", "safe.directory="+repoDir,
		"-C", repoDir, "ls-files", "-z")
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		if stderr != "" {
			return rep, fmt.Errorf("list tracked files: %w (%s)", err, stderr)
		}
		return rep, fmt.Errorf("list tracked files: %w", err)
	}

	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" {
			continue
		}
		rep.Scanned++
		path := filepath.Join(repoDir, rel)
		// Lstat, not Stat: a tracked symlink must have the LINK's ownership
		// fixed, and we must never follow it to chown something outside the
		// repo.
		fi, err := os.Lstat(path)
		if err != nil {
			continue // deleted from the worktree; reset will restore it
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || (int(st.Uid) == uid && int(st.Gid) == gid) {
			continue
		}
		if err := os.Lchown(path, uid, gid); err != nil {
			return rep, fmt.Errorf("chown %s: %w", rel, err)
		}
		rep.Fixed = append(rep.Fixed, rel)
	}
	return rep, nil
}

// gitFailureLine reduces a command's combined output to the single most useful line
// for an inline note — git puts the real cause on its `fatal:` line and pads
// the rest with context the operator does not need mid-update.
func gitFailureLine(out string, err error) string {
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "fatal:") {
			return l
		}
	}
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	if err != nil {
		return err.Error()
	}
	return "unknown error"
}
