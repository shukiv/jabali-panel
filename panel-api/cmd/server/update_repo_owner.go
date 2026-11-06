package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Layout of the panel checkout.
//
// The checkout is root-owned, .git included. Root's git runs with hooks,
// fsmonitor and pager off and without system or global config. Go and npm run
// as root with their caches outside the tree; npm runs with --ignore-scripts.
// install.sh, the install/ payloads and the root-run binaries all come from
// this tree.

// canonicalRepoURL is the source of truth for the checkout. It matches
// install.sh's REPO_URL default. A re-clone uses this constant, not the origin
// URL recorded in an existing .git.
const canonicalRepoURL = "https://github.com/shukiv/jabali-panel.git"

// goBuildHome holds the Go build and module caches for root builds. It sits
// outside the checkout so that nothing the service user writes feeds a root
// build cache.
const goBuildHome = "/var/cache/jabali-build"

// artifactMarkerDir holds the sha markers that let `jabali update` skip npm and
// vite work. Root-only.
const artifactMarkerDir = "/var/lib/jabali-build/markers"

// gitRootCmd builds a git invocation that root can run against a checkout
// without running hooks. The -c flags turn off hooks, fsmonitor and pager.
// GIT_CONFIG_NOSYSTEM and GIT_CONFIG_GLOBAL drop system and global config.
func gitRootCmd(dir string, args ...string) *exec.Cmd {
	var full []string
	if dir != "" {
		full = append(full, "-C", dir)
	}
	full = append(full,
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=false",
		"-c", "core.pager=cat",
	)
	full = append(full, args...)
	c := exec.Command("git", full...)
	c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	return c
}

// gitOwnerWrong reports whether a .git owner is not root. Only a root-owned
// .git is accepted.
func gitOwnerWrong(owner string) bool { return owner != "root" }

// recloneArgs are the git arguments that re-create the checkout at dst from
// the canonical URL. Both the update path and repair use them.
func recloneArgs(dst string) []string {
	return []string{"clone", "--branch", "main", canonicalRepoURL, dst}
}

// gitRun runs a root git command with its output streamed to the terminal.
func gitRun(dir string, args ...string) error {
	c := gitRootCmd(dir, args...)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// gitOut runs a root git command and returns its combined output, so the
// caller can classify what git said.
func gitOut(dir string, args ...string) (string, error) {
	out, err := gitRootCmd(dir, args...).CombinedOutput()
	return string(out), err
}

// ownedByRoot reports whether path is owned by uid 0. It does not follow
// symlinks.
func ownedByRoot(path string) (bool, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("stat %s: no unix ownership info", path)
	}
	return st.Uid == 0, nil
}

// enforceRepoOwnership puts the checkout in the root-owned layout described
// above. It is idempotent and runs before and after each git sync.
//
// A checkout whose .git is not root-owned is moved aside and re-cloned from
// canonicalRepoURL. The moved tree is kept. migrated reports that the re-clone
// happened, so the caller forces a full rebuild.
//
// runCmd and reclone are injected so the layout can be tested without root.
func enforceRepoOwnership(
	repoDir string,
	runCmd func(name string, args ...string) error,
	reclone func(dst string) error,
) (migrated bool, err error) {
	if _, statErr := os.Lstat(filepath.Join(repoDir, ".git")); os.IsNotExist(statErr) {
		return false, nil // fresh host: install.sh clones it
	}
	rootOwned, err := ownedByRoot(filepath.Join(repoDir, ".git"))
	if err != nil {
		return false, err
	}
	if !rootOwned {
		quarantine := repoDir + ".quarantine-" + time.Now().UTC().Format("20060102T150405Z")
		if err := os.Rename(repoDir, quarantine); err != nil {
			return false, fmt.Errorf("move jabali-owned checkout aside: %w", err)
		}
		fmt.Printf("  checkout at %s was not root-owned — kept at %s, re-cloning\n", repoDir, quarantine)
		if err := reclone(repoDir); err != nil {
			return false, fmt.Errorf("re-clone %s (old tree kept at %s): %w", canonicalRepoURL, quarantine, err)
		}
		migrated = true
	}
	entries, err := os.ReadDir(repoDir)
	if err != nil {
		return migrated, err
	}
	for _, e := range entries {
		if err := runCmd("chown", "-R", "root:root", filepath.Join(repoDir, e.Name())); err != nil {
			return migrated, err
		}
	}
	if err := runCmd("chown", "root:root", repoDir); err != nil {
		return migrated, err
	}
	// git checkouts keep the process umask; make the tree readable by the
	// service user, which serves the embedded assets.
	if err := runCmd("chmod", "-R", "a+rX", repoDir); err != nil {
		return migrated, err
	}
	return migrated, nil
}

// rootNpm runs a shell command as root in dir with npm's cache outside the
// checkout and a clean environment. Callers pass npm commands that include
// --ignore-scripts.
func rootNpm(dir, script string) error {
	_ = os.MkdirAll(goBuildHome+"/home", 0o755)
	_ = os.MkdirAll(goBuildHome+"/npm", 0o755)
	c := exec.Command("env", "-i",
		"HOME="+goBuildHome+"/home",
		"npm_config_cache="+goBuildHome+"/npm",
		"PATH=/usr/bin:/bin",
		"bash", "-c", script)
	c.Dir = dir
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("npm in %s: %w", dir, err)
	}
	return nil
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

// mustGlob returns the paths matching pattern, or nil when none match or the
// pattern is malformed.
func mustGlob(pattern string) []string {
	m, _ := filepath.Glob(pattern)
	return m
}
