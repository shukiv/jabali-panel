package commands

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// composer_run.go — run Composer for a tenant on the PHP the domain is
// served by.
//
// /usr/local/bin/composer is a sh dispatcher (install.sh, GH #1332 item 13)
// that execs the phar the tenant's channel picks, and every phar starts with
// `#!/usr/bin/env php`. Under the agent's pinned PATH that is the host's
// default PHP, not the domain's, so vendor/ was resolved for the wrong PHP
// and its platform check could refuse the site. The agent picks the phar the
// way the dispatcher does and runs it with the domain's PHP binary.

// composerBinDir is where install.sh puts Composer; tests point it at a
// temporary directory.
var composerBinDir = "/usr/local/bin"

// composerPharFor returns the Composer phar osUser's channel selects, the
// same one the dispatcher would exec for them: composer-lts when the channel
// is "lts" and it is installed, else composer-latest. A host provisioned
// before the dispatcher has the phar itself at composer.
func composerPharFor(osUser string) (string, error) {
	if !osUserSafeRE.MatchString(osUser) {
		return "", fmt.Errorf("invalid os user %q", osUser)
	}
	if b, err := os.ReadFile(filepath.Join(composerChoiceRoot(), osUser)); err == nil && strings.TrimSpace(string(b)) == "lts" {
		if p := filepath.Join(composerBinDir, "composer-lts"); isExecutableRegular(p) {
			return p, nil
		}
	}
	if p := filepath.Join(composerBinDir, "composer-latest"); isExecutableRegular(p) {
		return p, nil
	}
	if p := filepath.Join(composerBinDir, "composer"); isExecutableRegular(p) && startsAsPHPScript(p) {
		return p, nil
	}
	return "", fmt.Errorf("Composer is not installed on this server (no Composer phar in %s)", composerBinDir)
}

// composerHomeFor is the COMPOSER_HOME for osUser's panel-run Composer:
// ~/.composer, outside every site's web root. Composer's cache is its cache/
// subdirectory (composerEnv).
func composerHomeFor(osUser string) string {
	return filepath.Join(tenantHomeDir(osUser), ".composer")
}

// composerEnv is the environment for a panel-run Composer, as `env`
// arguments: home and cache in ~/.composer, never prompting.
func composerEnv(osUser string) []string {
	home := composerHomeFor(osUser)
	return []string{
		"COMPOSER_HOME=" + home,
		"COMPOSER_CACHE_DIR=" + filepath.Join(home, "cache"),
		"COMPOSER_NO_INTERACTION=1",
	}
}

// ensureComposerHome makes osUser's ~/.composer a directory they own.
// Tenant homes are root-owned 0751, so the tenant cannot create it and
// Composer would run without a cache.
//
// The agent runs as root and some tenants own their home, so nothing here
// follows a link (as in ensureUserLogsDir): only the last component is
// created, an existing entry must be a real directory (Lstat), and the owner
// is set with Lchown.
func ensureComposerHome(osUser string) error {
	if !osUserSafeRE.MatchString(osUser) {
		return fmt.Errorf("invalid os user %q", osUser)
	}
	u, err := user.Lookup(osUser)
	if err != nil {
		return fmt.Errorf("look up %s: %w", osUser, err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return fmt.Errorf("uid of %s: %w", osUser, err)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return fmt.Errorf("gid of %s: %w", osUser, err)
	}
	dir := composerHomeFor(osUser)
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if err := os.Lchown(dir, uid, gid); err != nil {
		return fmt.Errorf("chown %s: %w", dir, err)
	}
	return nil
}

// isExecutableRegular reports whether p is a regular file with an execute bit.
func isExecutableRegular(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// startsAsPHPScript reports whether p's first line runs it with PHP, as a
// Composer phar's `#!/usr/bin/env php` does; the dispatcher starts with
// `#!/bin/sh`.
func startsAsPHPScript(p string) bool {
	f, err := os.Open(p) //nolint:gosec // p is under composerBinDir
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 128)
	n, _ := io.ReadFull(f, head)
	line, _, _ := strings.Cut(string(head[:n]), "\n")
	return strings.HasPrefix(line, "<?php") || (strings.HasPrefix(line, "#!") && strings.Contains(line, "php"))
}
