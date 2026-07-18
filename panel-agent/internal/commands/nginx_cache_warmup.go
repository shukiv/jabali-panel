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

// sitemapPaths fetches the site's sitemap and returns up to max same-host URL
// paths. Tries the WP core sitemap first, then a generic sitemap.xml. One level
// of sitemap-index expansion (WP core's wp-sitemap.xml is an index).
func sitemapPaths(ctx context.Context, host string, max int) []string {
	seen := map[string]bool{}
	var paths []string
	add := func(u string) {
		p := toSameHostPath(host, u)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		paths = append(paths, p)
	}
	for _, entry := range []string{"/wp-sitemap.xml", "/sitemap.xml", "/sitemap_index.xml"} {
		body := warmupFetchBody(ctx, host, entry)
		locs := locRe.FindAllStringSubmatch(body, -1)
		if len(locs) == 0 {
			continue
		}
		// If these are sub-sitemaps (contain "sitemap" in the path), expand one
		// level; otherwise treat them as page URLs.
		for _, m := range locs {
			u := m[1]
			if strings.Contains(strings.ToLower(u), "sitemap") && strings.HasSuffix(strings.ToLower(u), ".xml") {
				sub := warmupFetchBody(ctx, host, toSameHostPath(host, u))
				for _, sm := range locRe.FindAllStringSubmatch(sub, -1) {
					add(sm[1])
					if len(paths) >= max {
						return paths
					}
				}
			} else {
				add(u)
			}
			if len(paths) >= max {
				return paths
			}
		}
		if len(paths) > 0 {
			break
		}
	}
	return paths
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
	var attempted, warmed, failed int
	var firstError string
	note := func(path string, code int) {
		attempted++
		if code >= 200 && code < 400 {
			warmed++
			return
		}
		failed++
		if firstError == "" {
			firstError = fmt.Sprintf("%s -> %d", path, code)
		}
	}
	// Homepage first, then bounded sitemap URLs.
	note("/", warmupFetch(ctx, host, "/"))
	for _, path := range sitemapPaths(ctx, host, max-1) {
		if ctx.Err() != nil {
			break
		}
		note(path, warmupFetch(ctx, host, path))
	}
	return map[string]any{
		"ok":          true,
		"host":        host,
		"requested":   requested,
		"clamped_to":  clampedTo,
		"attempted":   attempted,
		"warmed":      warmed,
		"skipped":     0, // freshness detection lands in a later phase.
		"failed":      failed,
		"first_error": firstError,
		"duration_ms": time.Since(start).Milliseconds(),
	}, nil
}

func init() {
	Default.Register("nginx.cache_warmup", nginxCacheWarmupHandler)
}
