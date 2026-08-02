package commands

// JAB-201 — cache health/advisor blind spots. Three additions to the
// probe data so "green while ineffective" can't happen silently:
//
//  1. When the MISS→HIT double-fetch does NOT verify, classify WHY from
//     the response headers instead of leaving the advisor guessing —
//     the production case was a maintenance-mode plugin calling WP's
//     nocache_headers() (Expires: 1984 signature) on every response,
//     which the fail-closed gate (GH #637) correctly refuses to store.
//  2. Tally the domain's jcache log ($upstream_cache_status, one token
//     per line) so a cache_enabled site with 0% HIT over the log window
//     is a visible red flag, not a silent no-op.
//  3. Assert the purge-spool path from the tenant's perspective: the
//     pool's open_basedir must include /run/jabali-wp-purge (the
//     JAB-199 regression) and the spool dir must exist world-writable.

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// classifyCacheBlock inspects the raw response headers of the SECOND
// (should-be-HIT) probe fetch and names the strongest reason the page
// cache refused to store. Empty string = no known blocker found.
func classifyCacheBlock(rawHeaders string) (cause, evidence string) {
	for _, line := range strings.Split(rawHeaders, "\n") {
		line = strings.TrimRight(line, "\r")
		l := strings.ToLower(line)
		switch {
		case strings.HasPrefix(l, "expires:") && strings.Contains(l, "1984"):
			// WP nocache_headers() emits "Expires: Wed, 11 Jan 1984 …" —
			// the signature of maintenance/coming-soon plugins and
			// wp_die pages. The strongest signal; return immediately.
			return "wp_nocache_headers", strings.TrimSpace(line)
		case strings.HasPrefix(l, "cache-control:") &&
			(strings.Contains(l, "no-cache") || strings.Contains(l, "no-store") || strings.Contains(l, "private")):
			if cause == "" {
				cause, evidence = "upstream_cache_control", strings.TrimSpace(line)
			}
		case strings.HasPrefix(l, "vary:") && strings.TrimSpace(l) != "vary: accept-encoding":
			if cause == "" {
				cause, evidence = "vary_header", strings.TrimSpace(line)
			}
		case strings.HasPrefix(l, "set-cookie:"):
			if cause == "" {
				cause, evidence = "response_sets_cookie", strings.TrimSpace(line)
			}
		}
	}
	return cause, evidence
}

// tallyJcacheLog counts $upstream_cache_status tokens (log_format
// jcache emits exactly one per request line).
func tallyJcacheLog(r io.Reader) (hits, misses, bypass, other int) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		switch strings.TrimSpace(sc.Text()) {
		case "HIT":
			hits++
		case "MISS":
			misses++
		case "BYPASS":
			bypass++
		case "", "-":
			// non-cache location or unset — not a cache decision
		default:
			other++ // EXPIRED, STALE, UPDATING, REVALIDATED
		}
	}
	return
}

// jcacheLogTailBytes bounds how much of the log one probe reads. The
// file rotates daily; half a megabyte of one-token lines is thousands
// of requests — plenty for a ratio.
const jcacheLogTailBytes = 512 * 1024

// tallyJcacheLogFile reads the bounded tail of the domain's jcache log.
// ok=false when the log doesn't exist (cache never enabled / no traffic
// yet) — callers must not read zeros as "0% HIT".
func tallyJcacheLogFile(host string) (hits, misses, bypass, other int, ok bool) {
	f, err := os.Open("/var/log/nginx/jcache-" + host + ".log")
	if err != nil {
		return 0, 0, 0, 0, false
	}
	defer f.Close()
	if st, serr := f.Stat(); serr == nil && st.Size() > jcacheLogTailBytes {
		if _, serr := f.Seek(st.Size()-jcacheLogTailBytes, io.SeekStart); serr == nil {
			// Drop the (likely partial) first line after the seek.
			br := bufio.NewReader(f)
			_, _ = br.ReadString('\n')
			hits, misses, bypass, other = tallyJcacheLog(br)
			return hits, misses, bypass, other, true
		}
	}
	hits, misses, bypass, other = tallyJcacheLog(f)
	return hits, misses, bypass, other, true
}

// probeSpool checks the WP→nginx purge path from the tenant's
// perspective: the spool dir must exist and be world-writable (tmpfiles
// creates it 1777), and the user's FPM pool open_basedir must include
// it — the exact line whose absence made every content-edit purge a
// silent no-op fleet-wide (JAB-199).
func probeSpool(osUser string) (ok bool, detail string) {
	const spool = "/run/jabali-wp-purge"
	st, err := os.Stat(spool)
	if err != nil {
		return false, "purge spool " + spool + " missing — WP content-edit purges are no-ops (re-run install.sh)"
	}
	if st.Mode().Perm()&0o002 == 0 {
		return false, "purge spool " + spool + " not world-writable — tenant PHP cannot drop purge requests"
	}
	matches, _ := filepath.Glob("/etc/php/*/fpm/pool.d/jabali-" + osUser + ".conf")
	if len(matches) == 0 {
		// No pool conf (static site, custom pool name) — nothing to assert.
		return true, ""
	}
	for _, conf := range matches {
		b, rerr := os.ReadFile(conf)
		if rerr != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "php_admin_value[open_basedir]") {
				if !strings.Contains(line, spool) {
					return false, "pool open_basedir in " + filepath.Base(conf) + " lacks " + spool +
						" — the JAB-199 regression: WP purges of the page cache silently fail (run jabali update to reapply pools)"
				}
			}
		}
	}
	return true, ""
}
