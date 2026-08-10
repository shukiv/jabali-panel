// Command catalog-bump checks every Docker-apps catalog entry against its
// upstream registry and rewrites app.yaml with the newest matching tag +
// digest (JAB-234). It is run by .github/workflows/catalog-bump.yml; humans
// review and merge the resulting PR.
package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// imageRef is a parsed image_channel value: [host/]repo:tag@sha256:digest.
type imageRef struct {
	Host   string // registry host, e.g. ghcr.io / registry-1.docker.io
	Repo   string // repository path, e.g. shukiv/jabali-sounder or library/ghost
	Tag    string
	Digest string // sha256:<64 hex>
}

var digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// parseImageRef splits an image_channel reference. Catalog policy (GH #458)
// requires the digest, so a ref without @sha256:… is an error here too.
func parseImageRef(s string) (imageRef, error) {
	var r imageRef
	name, digest, ok := strings.Cut(s, "@")
	if !ok || !digestRe.MatchString(digest) {
		return r, fmt.Errorf("ref %q is not digest-pinned", s)
	}
	r.Digest = digest

	// The tag separator is the last ':' after the last '/'.
	slash := strings.LastIndex(name, "/")
	colon := strings.LastIndex(name, ":")
	if colon <= slash {
		return r, fmt.Errorf("ref %q has no tag", s)
	}
	r.Tag = name[colon+1:]
	path := name[:colon]

	// A first segment with a dot or port is a registry host; otherwise the
	// ref lives on Docker Hub (official images get the library/ namespace).
	first, rest, hasSlash := strings.Cut(path, "/")
	if hasSlash && (strings.ContainsAny(first, ".:") || first == "localhost") {
		r.Host = first
		r.Repo = rest
		// docker.io is the UI hostname, not the registry API endpoint.
		if r.Host == "docker.io" || r.Host == "index.docker.io" {
			r.Host = "registry-1.docker.io"
		}
	} else {
		r.Host = "registry-1.docker.io"
		if hasSlash {
			r.Repo = path
		} else {
			r.Repo = "library/" + path
		}
	}
	if r.Repo == "" {
		return r, fmt.Errorf("ref %q has no repository", s)
	}
	return r, nil
}

// tagParts decomposes a tag into prefix ("v" or ""), numeric core components,
// and a literal suffix, e.g. v8.2.1-trixie → "v", [8 2 1], "-trixie".
type tagParts struct {
	Prefix string
	Core   []int
	Suffix string
}

var tagRe = regexp.MustCompile(`^(v?)(\d+(?:\.\d+)*)(.*)$`)

// hexSuffixRe marks build-hash suffixes (searxng's 2026.6.20-fd42d4fda):
// these change every release, so candidates match the pattern, not the text.
var hexSuffixRe = regexp.MustCompile(`^-[0-9a-f]{7,}$`)

// parseTag returns ok=false for unversioned schemes (latest, stable,
// version-2025-05-14b) — those entries are reported for manual handling.
func parseTag(tag string) (tagParts, bool) {
	m := tagRe.FindStringSubmatch(tag)
	if m == nil {
		return tagParts{}, false
	}
	var core []int
	for _, c := range strings.Split(m[2], ".") {
		n, err := strconv.Atoi(c)
		if err != nil {
			return tagParts{}, false
		}
		core = append(core, n)
	}
	return tagParts{Prefix: m[1], Core: core, Suffix: m[3]}, true
}

// coreString renders the numeric core (8.2.1) — the value the catalog's
// version: field carries.
func (p tagParts) coreString() string {
	parts := make([]string, len(p.Core))
	for i, n := range p.Core {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ".")
}

// compareCore returns -1/0/1 for a<b, a==b, a>b (equal component counts).
func compareCore(a, b []int) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// pickBestTag selects the highest tag that preserves the current tag's
// pattern: same prefix, same component count (so a major-only track like
// uptime-kuma's "2" only moves to "3", never to "2.1.3"), and the same
// literal suffix (which is also what keeps -rc/-beta prereleases out).
// Hash-style suffixes match by pattern instead. Returns ok=false when no
// newer matching tag exists.
func pickBestTag(cur tagParts, tags []string) (string, tagParts, bool) {
	best := cur
	bestTag := ""
	hexMode := hexSuffixRe.MatchString(cur.Suffix)
	for _, t := range tags {
		p, ok := parseTag(t)
		if !ok || p.Prefix != cur.Prefix || len(p.Core) != len(cur.Core) {
			continue
		}
		if hexMode {
			if !hexSuffixRe.MatchString(p.Suffix) {
				continue
			}
		} else if p.Suffix != cur.Suffix {
			continue
		}
		if compareCore(p.Core, best.Core) > 0 {
			best = p
			bestTag = t
		}
	}
	if bestTag == "" {
		return "", tagParts{}, false
	}
	return bestTag, best, true
}
