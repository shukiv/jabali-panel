package commands

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// PHP Defense rules for panel-started Composer runs (GH #2276, ADR-0176).
//
// Composer runs git, unzip and composer.json scripts through proc_open, which
// 00-base.rules bans for every PHP, so in enforce mode a run stops at the first
// download. A run loads its own copy of active.rules without the exec-family
// bans and the phpinfo ban (composerPHPDefenseLifted), built like a GH #1701
// pool copy. Every other rule stays.
//
// The copy also logs to PHP's error log instead of syslog. Runs execute in the
// SSH sandbox, where /run is a fresh tmpfs and /dev/log leads nowhere, so a
// syslog line would be lost. On stderr it lands in the run's log, where
// composerRunBlockedRE finds it.
//
// Both files sit under /etc/jabali/snuffleupagus, the one /etc/jabali
// directory the sandbox binds. A run selects the ini with
// PHP_INI_SCAN_DIR=:<dir>, which every PHP process Composer starts inherits.

// Paths, vars so tests can point them at a temp dir.
var (
	composerPHPDefenseRulesPath = "/etc/jabali/snuffleupagus/composer.rules"
	composerPHPDefenseIniDir    = "/etc/jabali/snuffleupagus/composer.d"
)

const composerPHPDefenseIniName = "90-jabali-composer.ini"

// phpDefenseLogMediaRE matches the log_media directive 00-base.rules sets.
var phpDefenseLogMediaRE = regexp.MustCompile(`^sp\.log_media\("[a-z]+"\);\s*$`)

// composerPHPDefenseLifted reports whether a Composer run's copy lifts the
// ban on f: the exec family, which Composer runs git, unzip and scripts
// through, and phpinfo, which Symfony Process calls before starting any
// process to learn whether PHP was built with sigchild support (seen on a
// test server: every run stopped there, before its first download).
func composerPHPDefenseLifted(f string) bool {
	return phpDefenseExecFunctions[f] || f == "phpinfo"
}

// renderComposerPHPDefenseRules is active.rules without the bans Composer
// trips over, logging to PHP's error log.
func renderComposerPHPDefenseRules(active []byte) []byte {
	var b bytes.Buffer
	b.WriteString("# Jabali PHP Defense rules for panel-started Composer runs -- RENDERED by the agent, do not edit.\n")
	b.WriteString("# A copy of active.rules without the exec-family and phpinfo bans, logging to\n")
	b.WriteString("# PHP's error log (ADR-0176). Rebuilt before every run.\n")
	for _, line := range strings.Split(strings.TrimSuffix(string(active), "\n"), "\n") {
		if m := phpDefenseDropLineRE.FindStringSubmatch(line); m != nil && composerPHPDefenseLifted(m[1]) {
			b.WriteString("# allowed for Composer runs: " + line + "\n")
			continue
		}
		if phpDefenseLogMediaRE.MatchString(line) {
			b.WriteString("sp.log_media(\"php\");\n")
			continue
		}
		b.WriteString(line + "\n")
	}
	return b.Bytes()
}

// ensureComposerPHPDefense writes the Composer copy of the current
// active.rules and the ini that selects it. It returns the scan dir a run
// adds with PHP_INI_SCAN_DIR, or "" when PHP Defense is not installed and
// there is nothing to select.
func ensureComposerPHPDefense() (string, error) {
	iniPath := filepath.Join(composerPHPDefenseIniDir, composerPHPDefenseIniName)
	active, err := os.ReadFile(phpDefenseActiveRulesPath)
	if os.IsNotExist(err) {
		for _, p := range []string{iniPath, composerPHPDefenseRulesPath} {
			if _, rerr := removePHPDefenseFile(p); rerr != nil {
				return "", fmt.Errorf("remove Composer PHP Defense rules: %w", rerr)
			}
		}
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read PHP Defense rules: %w", err)
	}
	if _, err := writePHPDefenseFile(composerPHPDefenseRulesPath, renderComposerPHPDefenseRules(active)); err != nil {
		return "", fmt.Errorf("write Composer PHP Defense rules: %w", err)
	}
	ini := "; Jabali PHP Defense for panel-started Composer runs (ADR-0176).\n" +
		"; RENDERED by the agent, do not edit.\n" +
		"sp.configuration_file=" + composerPHPDefenseRulesPath + "\n"
	if _, err := writePHPDefenseFile(iniPath, []byte(ini)); err != nil {
		return "", fmt.Errorf("write Composer PHP Defense ini: %w", err)
	}
	return composerPHPDefenseIniDir, nil
}
