package commands

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// JAB-67: bound the full-domain purge walk. Without per-tenant cache
// partitioning (a documented follow-up), a full purge must scan the shared
// cache to host-match entries — but a tenant spamming full purges could
// otherwise pin the root agent doing I/O across every OTHER tenant's cache
// files. Cap both the wall-clock and the file count so the walk always returns
// promptly; anything beyond the cap is left for nginx's own inactive-eviction.
const cachePurgeWalkDeadline = 10 * time.Second

// cachePurgeMaxFiles is a var (not const) so tests can lower the cap without
// fabricating 250k files.
var cachePurgeMaxFiles = 250000

// fastcgiCacheRoot is the shared keyzone storage written by
// install.sh's install_nginx_fastcgi_cache (ADR-0108). Kept in lockstep
// with /etc/nginx/conf.d/jabali-fastcgi-cache.conf's fastcgi_cache_path.
// fastcgiCacheRoot is a var (not const) so tests can point the purge at a temp dir.
var fastcgiCacheRoot = "/var/cache/nginx/jabali"

// purgeVhostDir is where rendered vhosts live; a var so tests can stub it.
var purgeVhostDir = "/etc/nginx/sites-enabled"

// domainUsesQueryAllowlist reports whether the domain's rendered vhost carries
// the GH #610 query-allowlist cache key (the $jabali_qkey marker). Such a
// domain stores EXTRA cache entries per allowlisted query combination
// ("...path?page=2&"), which the hash-based fast purge below cannot enumerate.
func domainUsesQueryAllowlist(domain string) bool {
	b, err := os.ReadFile(filepath.Join(purgeVhostDir, domain+".conf"))
	if err != nil {
		return false
	}
	return bytes.Contains(b, []byte("$jabali_qkey"))
}

type nginxCachePurgeParams struct {
	Domain string `json:"domain"`
	// Paths (GH #619): optional targeted purge. When empty, the whole domain
	// is purged (v1 behaviour). When set, the hash fast path removes each
	// path's exact entries; domains with a cache_query_allowlist additionally
	// get a bounded walk that also evicts the path's query-key variants and
	// honours prefix matching ("<path>/..."). Callers wanting guaranteed
	// prefix semantics on a non-allowlist domain purge the whole domain.
	Paths []string `json:"paths,omitempty"`
}

// nginxCachePurgeHandler clears one domain's entries from the shared
// FastCGI keyzone (ADR-0108 v1: host-key grep-unlink — the cache_key is
// "$scheme$request_method$host$request_uri", so every cached file's
// stored `KEY: …` line contains the host). No nginx reload needed:
// nginx re-MISSes deleted entries transparently. Full per-domain cache
// partitioning is a documented follow-up.
func nginxCachePurgeHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p nginxCachePurgeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	// Reuse the strict domain validator (same one domain.create uses).
	// The domain is embedded in the KEY-match below; sanitising it here
	// makes the byte match safe and rejects traversal/glob input.
	if !domainRegex.MatchString(p.Domain) {
		return nil, csInvalidArg(fmt.Sprintf("invalid domain %q", p.Domain))
	}
	// Validate/normalize targeted paths: must be absolute, no traversal. A bad
	// path can't evict another host (host is matched exactly below), but reject
	// junk early so a caller mistake is obvious.
	var paths []string
	for _, raw := range p.Paths {
		pp := strings.TrimSpace(raw)
		if pp == "" {
			continue
		}
		if !strings.HasPrefix(pp, "/") || strings.Contains(pp, "..") {
			return nil, csInvalidArg(fmt.Sprintf("invalid path %q (must be absolute, no ..)", raw))
		}
		paths = append(paths, pp)
	}

	root := fastcgiCacheRoot
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		// Cache dir absent ⇒ nothing cached for anyone yet. Not an
		// error: a purge with no cache is a successful no-op.
		return map[string]any{"ok": true, "purged": 0, "domain": p.Domain}, nil
	}

	// GH #631: a targeted (exact-URL) purge doesn't need the full-tree walk —
	// the cache key is deterministic ($scheme$request_method$host$uri), so
	// compute each entry's file path directly (md5 of the key + levels=1:2) and
	// unlink it. Falls through to the host-match walk only for a full purge.
	if len(paths) > 0 {
		purged := 0
		for _, up := range paths {
			for _, scheme := range []string{"https", "http"} {
				for _, method := range []string{"GET", "HEAD"} {
					sum := md5.Sum([]byte(scheme + method + p.Domain + up))
					h := hex.EncodeToString(sum[:])
					fp := filepath.Join(root, h[31:32], h[29:31], h)
					if err := os.Remove(fp); err == nil {
						purged++
					}
				}
			}
		}
		// GH #610 interaction: a domain with a cache_query_allowlist keys its
		// allowlisted query combinations SEPARATELY ("...path?page=2&"), and
		// those keys cannot be enumerated from the path alone — the hash fast
		// path above only removed the clean-URL entries, leaving every query
		// variant stale after a content edit. For those domains, additionally
		// run the bounded KEY-line walk: uriMatchesAny strips the query, so
		// the variants match their purge path.
		if domainUsesQueryAllowlist(p.Domain) {
			wp, truncated, werr := walkPurgeHostPaths(ctx, root, p.Domain, paths)
			if werr != nil {
				return nil, csInternal("walk cache dir", werr)
			}
			purged += wp
			return map[string]any{"ok": true, "purged": purged, "domain": p.Domain, "truncated": truncated}, nil
		}
		return map[string]any{"ok": true, "purged": purged, "domain": p.Domain}, nil
	}

	purged, truncated, werr := walkPurgeHostPaths(ctx, root, p.Domain, nil)
	if werr != nil {
		return nil, csInternal("walk cache dir", werr)
	}
	return map[string]any{"ok": true, "purged": purged, "domain": p.Domain, "truncated": truncated}, nil
}

// walkPurgeHostPaths removes cached entries whose KEY line matches host and
// (when paths is non-empty) uriMatchesAny. nginx stores a
// `KEY: <scheme><method><host><uri>` line in the cache file header,
// concatenated with no delimiter. Match the HOST FIELD exactly — a raw
// substring match would evict another tenant whose host merely CONTAINS this
// domain ("a.com" ⊂ "sub.a.com") or whose URL path contains the string, a
// cross-tenant cache-eviction lever despite the owner-scoped REST (Gitea #418).
//
// Bounded (JAB-67): a dedicated deadline + a scanned-file cap so a purge can
// never block the agent proportional to total cross-tenant cache size.
func walkPurgeHostPaths(ctx context.Context, root, host string, paths []string) (int, bool, error) {
	needle := []byte("KEY: ")
	purged := 0

	walkCtx, cancel := context.WithTimeout(ctx, cachePurgeWalkDeadline)
	defer cancel()
	scanned := 0
	truncated := false

	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // skip unreadable entries, keep walking
		}
		scanned++
		if scanned > cachePurgeMaxFiles {
			truncated = true
			return filepath.SkipAll
		}
		select {
		case <-walkCtx.Done():
			return walkCtx.Err()
		default:
		}
		// Cache files are small headers + body; read a bounded prefix
		// to find the KEY line without slurping large bodies.
		f, oerr := os.Open(path)
		if oerr != nil {
			return nil
		}
		buf := make([]byte, 4096)
		n, _ := f.Read(buf)
		_ = f.Close()
		head := buf[:n]
		if i := bytes.Index(head, needle); i >= 0 {
			line := head[i:]
			if j := bytes.IndexByte(line, '\n'); j >= 0 {
				line = line[:j]
			}
			if keyLineHost(line) == host && uriMatchesAny(keyLineURI(line), paths) {
				if rmErr := os.Remove(path); rmErr == nil {
					purged++
				}
			}
		}
		return nil
	})
	if walkErr != nil && walkErr != context.Canceled && walkErr != context.DeadlineExceeded {
		return purged, truncated, walkErr
	}
	if walkErr == context.DeadlineExceeded || walkCtx.Err() == context.DeadlineExceeded {
		truncated = true
	}
	if truncated {
		slog.Warn("nginx cache purge walk hit a bound; some entries left for nginx inactive-eviction",
			"domain", host, "scanned", scanned, "purged", purged)
	}
	return purged, truncated, nil
}

// keyLineHost extracts the $host field from an nginx cache KEY line of the form
// "KEY: <scheme><method><host><uri>". The fields are concatenated with no
// separator, so we peel the known scheme + method prefixes and take everything
// up to the first "/" (the request_uri always begins with "/"). Returns "" if
// the line doesn't parse. Exact host comparison (not substring) is the #418 fix.
func keyLineHost(line []byte) string {
	v := strings.TrimSpace(strings.TrimPrefix(string(line), "KEY:"))
	for _, sch := range []string{"https", "http"} { // https first (longer)
		if strings.HasPrefix(v, sch) {
			v = v[len(sch):]
			break
		}
	}
	for _, m := range []string{"GET", "HEAD", "POST", "PUT", "DELETE", "OPTIONS", "PATCH", "PURGE", "TRACE", "CONNECT"} {
		if strings.HasPrefix(v, m) {
			v = v[len(m):]
			break
		}
	}
	if i := strings.IndexByte(v, '/'); i >= 0 {
		return v[:i]
	}
	return v
}

// keyLineURI extracts the request_uri (from the first "/" onward) from an nginx
// cache KEY line "KEY: <scheme><method><host><uri>". Mirror of keyLineHost.
func keyLineURI(line []byte) string {
	v := strings.TrimSpace(strings.TrimPrefix(string(line), "KEY:"))
	for _, sch := range []string{"https", "http"} {
		if strings.HasPrefix(v, sch) {
			v = v[len(sch):]
			break
		}
	}
	for _, m := range []string{"GET", "HEAD", "POST", "PUT", "DELETE", "OPTIONS", "PATCH", "PURGE", "TRACE", "CONNECT"} {
		if strings.HasPrefix(v, m) {
			v = v[len(m):]
			break
		}
	}
	if i := strings.IndexByte(v, '/'); i >= 0 {
		return v[i:]
	}
	return "/"
}

// uriMatchesAny reports whether a cached entry's request_uri should be purged
// for the given targeted paths (GH #619). Empty paths = whole-domain purge.
// A path matches its exact URL, or as a prefix ("/blog" purges "/blog/x"),
// and "/" matches everything.
func uriMatchesAny(uri string, paths []string) bool {
	if len(paths) == 0 {
		return true
	}
	if i := strings.IndexByte(uri, '?'); i >= 0 {
		uri = uri[:i]
	}
	for _, p := range paths {
		if p == "/" || uri == p {
			return true
		}
		if strings.HasPrefix(uri, strings.TrimRight(p, "/")+"/") {
			return true
		}
	}
	return false
}

func init() {
	Default.Register("nginx.cache.purge", nginxCachePurgeHandler)
}
