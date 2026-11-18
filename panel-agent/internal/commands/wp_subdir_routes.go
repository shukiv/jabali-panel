// Subfolder WordPress routing.
//
// A WordPress installed in a subfolder of a domain (a catalog install with a
// subdirectory, an install found by `jabali app scan`, or one a migration
// brought in) serves its pretty permalinks (/<sub>/<slug>/) through its own
// index.php. The vhost's default `location /` falls back to the ROOT's
// /index.php, so without a per-folder fallback every permalink under the
// folder reaches the site root's WordPress (or nothing) and 404s.
//
// The panel sends the domain's subfolder WordPress paths as wp_subdirs and the
// template renders one plain prefix location per folder holding only try_files:
//
//	location /<sub>/ { try_files $uri $uri/ /<sub>/index.php?$query_string; }
//
// No `^~` and no nested PHP block, on purpose: regex locations still win, so
// .php keeps going to the domain's own `location ~ \.php$` (existence guard,
// PHP pins, page cache) and the template's other regex locations still apply.
//
// A folder that already has a prefix location declared for it or for any
// parent of it (Rule Builder rules, raw directives, folder privacy, an app's
// rewrite snippet) gets no route: a second location for the same path fails
// nginx -t, and a more specific location under a password-protected parent
// would win nginx's longest-prefix match and skip the parent's auth_basic.

package commands

import (
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// wpSubdirSegmentRE bounds one folder segment. The path is rendered verbatim
// into the vhost (config-injection trust boundary), so only characters that
// need no quoting in an nginx location are allowed, and a segment must start
// with a letter or digit ("." and ".." can never pass).
var wpSubdirSegmentRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

const wpSubdirMaxDepth = 4

// sanitizeWPSubdir re-validates a panel-supplied install folder and returns it
// as "/a" or "/a/b". Anything else (empty, root, traversal, spaces, braces,
// semicolons, quotes, too deep) is refused.
func sanitizeWPSubdir(p string) (string, bool) {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" {
		return "", false
	}
	segs := strings.Split(p, "/")
	if len(segs) > wpSubdirMaxDepth {
		return "", false
	}
	for _, s := range segs {
		if !wpSubdirSegmentRE.MatchString(s) {
			return "", false
		}
	}
	return "/" + strings.Join(segs, "/"), true
}

// prefixLocationRE captures the path of every prefix (`location /x`) and
// preferential-prefix (`location ^~ /x`) location, quoted or not. Exact (`=`)
// and regex (`~`, `~*`) locations are not prefix matches and cannot collide.
var prefixLocationRE = regexp.MustCompile(`(?m)^\s*location\s+(?:\^~\s+)?["']?(/[^\s{;"']*)["']?\s*\{`)

// declaredPrefixLocations lists the prefix-location paths declared in blocks.
func declaredPrefixLocations(blocks ...string) []string {
	var out []string
	for _, b := range blocks {
		for _, m := range prefixLocationRE.FindAllStringSubmatch(b, -1) {
			out = append(out, m[1])
		}
	}
	return out
}

// appSnippetBlocks returns the per-install snippets the vhost includes from
// /etc/nginx/jabali/<domain>/ (Drupal/Joomla/ITFlow rewrites and the like), so
// their locations count as declared. Unreadable files are skipped.
func appSnippetBlocks(domain string) []string {
	files, _ := filepath.Glob(filepath.Join(nginxJabaliDir, domain, "*.conf"))
	out := make([]string, 0, len(files))
	for _, f := range files {
		if b, err := os.ReadFile(f); err == nil {
			out = append(out, string(b))
		}
	}
	return out
}

// conflictingLocation returns the declared prefix location that already
// covers sub's folder (the same path or a parent of it), or "".
func conflictingLocation(sub string, declared []string) string {
	folder := sub + "/"
	for _, d := range declared {
		if d == "/" {
			// A root location replaces the template's own `location /`
			// (RootOverridden), and the routes render only without it.
			continue
		}
		if strings.HasPrefix(folder, d) {
			return d
		}
	}
	return ""
}

// wpSubdirRoutes returns the subfolder WordPress paths the vhost routes:
// sanitized, deduplicated, sorted (stable vhost bytes), minus every folder a
// declared prefix location already covers.
func wpSubdirRoutes(domain string, subdirs []string, directiveBlocks ...string) []string {
	if len(subdirs) == 0 {
		return nil
	}
	blocks := append(append([]string{}, directiveBlocks...), appSnippetBlocks(domain)...)
	declared := declaredPrefixLocations(blocks...)
	seen := map[string]bool{}
	var out []string
	for _, raw := range subdirs {
		p, ok := sanitizeWPSubdir(raw)
		if !ok {
			log.Printf("domain.create %s: ignoring invalid wp_subdirs entry %q", domain, raw)
			continue
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		if c := conflictingLocation(p, declared); c != "" {
			log.Printf("domain.create %s: no WordPress route for %s/: location %s already covers it", domain, p, c)
			continue
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
