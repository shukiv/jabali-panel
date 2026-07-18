package commands

// nginx_cache_warmup.go — GH #615. After cache is enabled (or purged), the
// FastCGI microcache is cold: the first visitor to each URL pays the full PHP
// regen. Pre-warm it by fetching the homepage + a bounded slice of the site's
// sitemap THROUGH THE LOCAL nginx (curl --resolve pins the host to this box, so
// the real vhost + FPM pool run and each fetch stores a cache entry). Bounded
// and best-effort — warmup never blocks or fails the enable/purge it follows.

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type cacheWarmupParams struct {
	Host    string `json:"host"`
	MaxURLs int    `json:"max_urls"`
}

const (
	cacheWarmupDefaultMax = 20
	cacheWarmupHardMax    = 50
	cacheWarmupPerURL     = 12 * time.Second
	// JAB-95 Phase 4: pace requests so warmup can't spike PHP-FPM, and stop
	// after too many consecutive failures so a struggling site isn't hammered.
	cacheWarmupPaceDelay     = 150 * time.Millisecond
	cacheWarmupMaxConsecFail = 5
)

// locRe pulls URLs out of a sitemap (both urlset <loc> and sitemapindex <loc>).
var locRe = regexp.MustCompile(`(?i)<loc>\s*([^<\s]+)\s*</loc>`)

// warmupFetch requests one absolute path through the local nginx, priming the
// microcache. Returns the HTTP status (0 on connection failure).
var warmupFetch = func(ctx context.Context, host, path string) int {
	if path == "" {
		path = "/"
	}
	args := []string{
		"-s", "-o", "/dev/null", "-w", "%{http_code}",
		"-k", "--max-time", "12", // GH #639: no -L (don't follow redirects off the pinned hop)
		"-A", "jabali-cache-warmup/1.0",
		"--resolve", host + ":443:" + probeTargetIP,
		"--resolve", host + ":80:" + probeTargetIP,
		"https://" + host + path,
	}
	fctx, cancel := context.WithTimeout(ctx, cacheWarmupPerURL)
	defer cancel()
	out, _ := exec.CommandContext(fctx, "curl", args...).Output()
	code := 0
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &code)
	return code
}

// warmupFetchBody fetches a URL's body (for the sitemap), bounded.
var warmupFetchBody = func(ctx context.Context, host, path string) string {
	args := []string{
		"-s", "-k", "--max-time", "12", // GH #639: no -L (don't follow redirects off the pinned hop)
		"-A", "jabali-cache-warmup/1.0",
		"--resolve", host + ":443:" + probeTargetIP,
		"--resolve", host + ":80:" + probeTargetIP,
		"https://" + host + path,
	}
	fctx, cancel := context.WithTimeout(ctx, cacheWarmupPerURL)
	defer cancel()
	out, _ := exec.CommandContext(fctx, "curl", args...).Output()
	return string(out)
}

// sitemapEntry is one URL discovered in a sitemap, with the optional priority
// and lastmod hints SEO plugins (Yoast/RankMath) emit. WP core sitemaps omit
// both, so they default to priority 0.5 / empty lastmod and ranking falls back
// to the URL value tier.
type sitemapEntry struct {
	path     string
	priority float64
	lastmod  string
}

var (
	urlBlockRe  = regexp.MustCompile(`(?is)<url>(.*?)</url>`)
	priorityRe  = regexp.MustCompile(`(?i)<priority>\s*([0-9.]+)\s*</priority>`)
	lastmodRe   = regexp.MustCompile(`(?i)<lastmod>\s*([^<\s]+)\s*</lastmod>`)
	dateArchive = regexp.MustCompile(`^/(19|20)\d\d/`)
)

// sitemapMaxEntries bounds how many URLs we parse+rank. Parsing is cheap (just
// the XML), and the caller only warms `max-1` of the ranked result, so a
// generous ceiling costs little while giving ranking enough to work with.
const sitemapMaxEntries = 2000

// parseUrlset extracts <url> entries (loc + optional priority/lastmod) from a
// urlset sitemap body, resolved to same-host paths.
func parseUrlset(host, body string) []sitemapEntry {
	var out []sitemapEntry
	for _, m := range urlBlockRe.FindAllStringSubmatch(body, -1) {
		block := m[1]
		loc := locRe.FindStringSubmatch(block)
		if loc == nil {
			continue
		}
		path := toSameHostPath(host, loc[1])
		if path == "" {
			continue
		}
		e := sitemapEntry{path: path, priority: 0.5}
		if pr := priorityRe.FindStringSubmatch(block); pr != nil {
			if f, err := strconv.ParseFloat(pr[1], 64); err == nil {
				e.priority = f
			}
		}
		if lm := lastmodRe.FindStringSubmatch(block); lm != nil {
			e.lastmod = lm[1]
		}
		out = append(out, e)
	}
	return out
}

// sitemapEntries fetches the site's sitemap and returns the same-host URL
// entries. Tries the WP core sitemap first, then generic sitemap.xml. One level
// of sitemap-index expansion (WP core's wp-sitemap.xml is an index).
func sitemapEntries(ctx context.Context, host string) []sitemapEntry {
	seen := map[string]bool{}
	var entries []sitemapEntry
	addUrlset := func(body string) {
		for _, e := range parseUrlset(host, body) {
			if seen[e.path] {
				continue
			}
			seen[e.path] = true
			entries = append(entries, e)
		}
	}
	for _, entry := range []string{"/wp-sitemap.xml", "/sitemap.xml", "/sitemap_index.xml"} {
		body := warmupFetchBody(ctx, host, entry)
		locs := locRe.FindAllStringSubmatch(body, -1)
		if len(locs) == 0 {
			continue
		}
		// Sub-<loc> that are .xml sitemaps → index; expand one level. Otherwise
		// this body is itself a urlset.
		isIndex := false
		for _, m := range locs {
			u := strings.ToLower(m[1])
			if strings.Contains(u, "sitemap") && strings.HasSuffix(u, ".xml") {
				isIndex = true
				addUrlset(warmupFetchBody(ctx, host, toSameHostPath(host, m[1])))
				if len(entries) >= sitemapMaxEntries {
					return entries
				}
			}
		}
		if !isIndex {
			addUrlset(body)
		}
		if len(entries) > 0 {
			break
		}
	}
	return entries
}

// warmValueTier ranks a path by how much it benefits from a warm cache. Higher
// warms first. Homepage/shop + WooCommerce product & category pages are hottest;
// tag/author/date/paged archives are low value and warm last.
func warmValueTier(path string) int {
	lp := strings.ToLower(path)
	switch {
	case lp == "/":
		return 4
	case strings.HasPrefix(lp, "/shop") ||
		strings.Contains(lp, "/product/") ||
		strings.Contains(lp, "/product-category/"):
		return 3
	case strings.Contains(lp, "/tag/") ||
		strings.Contains(lp, "/author/") ||
		strings.Contains(lp, "/page/") ||
		dateArchive.MatchString(lp):
		return 1
	default:
		return 2 // pages + posts
	}
}

// prioritizeWarmPaths orders sitemap entries: value tier first, then the
// sitemap priority hint, then most-recently-modified. Stable so equal entries
// keep sitemap order. Returns the ordered paths. (JAB-95 Phase 2)
func prioritizeWarmPaths(entries []sitemapEntry) []string {
	sort.SliceStable(entries, func(i, j int) bool {
		ti, tj := warmValueTier(entries[i].path), warmValueTier(entries[j].path)
		if ti != tj {
			return ti > tj
		}
		if entries[i].priority != entries[j].priority {
			return entries[i].priority > entries[j].priority
		}
		return entries[i].lastmod > entries[j].lastmod // ISO dates sort lexically; recent first
	})
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.path)
	}
	return out
}

// toSameHostPath returns the absolute path of u only if u is on host (defends
// against a sitemap pointing off-site); "" otherwise.
func toSameHostPath(host, u string) string {
	u = strings.TrimSpace(u)
	if strings.HasPrefix(u, "/") {
		return u
	}
	for _, pre := range []string{"https://", "http://"} {
		if strings.HasPrefix(u, pre) {
			rest := u[len(pre):]
			slash := strings.IndexByte(rest, '/')
			h := rest
			path := "/"
			if slash >= 0 {
				h = rest[:slash]
				path = rest[slash:]
			}
			if strings.EqualFold(h, host) {
				return path
			}
			return ""
		}
	}
	return ""
}

// effectiveWarmupMax resolves the URL budget: a non-positive request falls back
// to the default, anything over the hard cap is clamped to it. The caller reports
// this as clamped_to so the panel can show when the requested limit was reduced
// (JAB-95 Phase 1).
func effectiveWarmupMax(requested int) int {
	m := requested
	if m <= 0 {
		m = cacheWarmupDefaultMax
	}
	if m > cacheWarmupHardMax {
		m = cacheWarmupHardMax
	}
	return m
}

func nginxCacheWarmupHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p cacheWarmupParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	host := strings.TrimSpace(strings.ToLower(p.Host))
	if !probeDomainRe.MatchString(host) {
		return nil, csInvalidArg(fmt.Sprintf("invalid host %q", p.Host))
	}
	// JAB-95 Phase 1: report the effective limit instead of silently clamping,
	// and return rich stats so the panel/UI can show what actually happened.
	requested := p.MaxURLs
	max := effectiveWarmupMax(requested)
	clampedTo := max

	start := time.Now()
	var attempted, warmed, failed, consecFail int
	var firstError string
	note := func(path string, code int) {
		attempted++
		if code >= 200 && code < 400 {
			warmed++
			consecFail = 0
			return
		}
		failed++
		consecFail++
		if firstError == "" {
			firstError = fmt.Sprintf("%s -> %d", path, code)
		}
	}
	// JAB-95 Phase 2: homepage first, then sitemap URLs in priority order
	// (high-value pages before low-value archives) so a small budget warms the
	// pages most likely to be hit, not just the first sitemap entries.
	note("/", warmupFetch(ctx, host, "/"))
	warmed2 := 0
	circuitBroken := false
	for _, path := range prioritizeWarmPaths(sitemapEntries(ctx, host)) {
		if ctx.Err() != nil || warmed2 >= max-1 {
			break
		}
		if path == "/" {
			continue // homepage already warmed above
		}
		// JAB-95 Phase 4: stop early if the site keeps failing, so warmup
		// doesn't pound a struggling backend through the whole list.
		if consecFail >= cacheWarmupMaxConsecFail {
			circuitBroken = true
			firstError = fmt.Sprintf("circuit-broken after %d consecutive failures (first: %s)", consecFail, firstError)
			break
		}
		// Pace so warmup never spikes PHP-FPM.
		select {
		case <-time.After(cacheWarmupPaceDelay):
		case <-ctx.Done():
		}
		note(path, warmupFetch(ctx, host, path))
		warmed2++
	}
	return map[string]any{
		"ok":             true,
		"host":           host,
		"requested":      requested,
		"clamped_to":     clampedTo,
		"attempted":      attempted,
		"warmed":         warmed,
		"skipped":        0, // freshness detection lands in a later phase.
		"failed":         failed,
		"first_error":    firstError,
		"circuit_broken": circuitBroken,
		"duration_ms":    time.Since(start).Milliseconds(),
	}, nil
}

func init() {
	Default.Register("nginx.cache_warmup", nginxCacheWarmupHandler)
}
