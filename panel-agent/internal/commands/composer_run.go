package commands

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
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

// composerHomeFor is the COMPOSER_HOME for osUser's panel-run Composer: the
// ~/.composer their own `composer` uses, outside every site's web root.
// Composer creates it on first use.
func composerHomeFor(osUser string) string {
	return filepath.Join(tenantHomeDir(osUser), ".composer")
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
