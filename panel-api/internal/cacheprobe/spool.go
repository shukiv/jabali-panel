package cacheprobe

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// spool.go — JAB-201 check 4 / JAB-199 regression guard.
//
// The jabali-cache WordPress plugin drops purge requests into
// /run/jabali-wp-purge for the agent's spool watcher. That directory was outside
// the pool's open_basedir, so the plugin's is_dir() guard tripped the
// restriction on every wp-admin request, concluded "not a Jabali host", and
// every content-edit purge of the nginx micro-cache was a silent no-op —
// fleet-wide, for as long as it took anyone to notice content staying stale for
// the full cache TTL after an edit.
//
// The pool template now lists the spool, but a box only picks that up when its
// pools are re-rendered. Until then the fix exists in git and not on the host,
// which is the deploy-drift shape that keeps producing incidents. So the check
// reads the pool file ON DISK rather than trusting the template: the question is
// what this host's PHP is actually jailed to, not what the repo says it should
// be.

// PurgeSpoolDir is where the plugin writes purge requests.
const PurgeSpoolDir = "/run/jabali-wp-purge"

// openBasedirAllows reports whether an open_basedir value permits path.
//
// open_basedir is a colon-separated prefix list. A path is allowed when it
// equals an entry or sits beneath one — prefix matching on the string alone
// would let "/run/jabali-wp-purge-evil" satisfy "/run/jabali-wp-purge", so the
// boundary is checked explicitly.
func openBasedirAllows(openBasedir, path string) bool {
	want := filepath.Clean(path)
	for _, entry := range strings.Split(openBasedir, ":") {
		e := filepath.Clean(strings.TrimSpace(entry))
		if e == "" || e == "." {
			continue
		}
		if e == want || strings.HasPrefix(want, e+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// parseOpenBasedir pulls the open_basedir value out of a rendered FPM pool file.
// Returns "" when the directive is absent — which means PHP is NOT jailed, a
// different and more serious problem that this check does not own.
func parseOpenBasedir(poolBody string) string {
	for _, line := range strings.Split(poolBody, "\n") {
		l := strings.TrimSpace(line)
		if l == "" || strings.HasPrefix(l, ";") {
			continue
		}
		if !strings.Contains(l, "open_basedir") {
			continue
		}
		if i := strings.Index(l, "="); i > 0 {
			return strings.TrimSpace(l[i+1:])
		}
	}
	return ""
}

// CheckPurgeSpool reports whether WordPress purges can actually reach the spool
// on this host, reading the user's rendered pool files.
//
// Returns ("", nil) when it cannot tell — no pool files found, or no
// open_basedir directive. Same contract as the cache outcome probe: a check that
// cannot see must not manufacture a failure.
func CheckPurgeSpool(poolGlobs []string, osUser string) (problem string, checked bool) {
	var bodies []string
	for _, g := range poolGlobs {
		matches, err := filepath.Glob(g)
		if err != nil {
			continue
		}
		for _, m := range matches {
			if osUser != "" && !strings.Contains(filepath.Base(m), osUser) {
				continue
			}
			if b, rErr := os.ReadFile(m); rErr == nil {
				bodies = append(bodies, string(b))
			}
		}
	}
	if len(bodies) == 0 {
		return "", false
	}

	for _, body := range bodies {
		ob := parseOpenBasedir(body)
		if ob == "" {
			continue // not jailed by this pool — not this check's business
		}
		if !openBasedirAllows(ob, PurgeSpoolDir) {
			return fmt.Sprintf(
				"the PHP pool's open_basedir does not include %s, so the jabali-cache plugin's "+
					"is_dir() check trips the restriction, decides this is not a Jabali host, and "+
					"every content-edit purge is silently dropped — pages stay stale for the full "+
					"cache TTL after an edit. This host's pools predate the fix; re-render them "+
					"(jabali php pool apply, or any update that reapplies pool templates)",
				PurgeSpoolDir), true
		}
	}

	// open_basedir is fine — does the directory actually exist and take writes?
	st, err := os.Stat(PurgeSpoolDir)
	if err != nil {
		return fmt.Sprintf("%s does not exist (%v) — the agent's tmpfiles rule has not run, "+
			"so purges have nowhere to land", PurgeSpoolDir, err), true
	}
	if !st.IsDir() {
		return PurgeSpoolDir + " exists but is not a directory", true
	}
	// The plugin writes as the tenant, so the sticky world-writable mode the
	// tmpfiles rule sets (1777) is what makes that possible.
	if st.Mode().Perm()&0o002 == 0 {
		return fmt.Sprintf("%s is not world-writable (mode %v) — the tenant's PHP-FPM worker "+
			"cannot drop purge requests there", PurgeSpoolDir, st.Mode().Perm()), true
	}
	return "", true
}
