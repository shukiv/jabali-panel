package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// bumpMode says how an entry's tag relates to its version field.
type bumpMode int

const (
	// modePinned: the tag core equals the version field (forgejo 15.0.3).
	// New release = a newer matching tag.
	modePinned bumpMode = iota
	// modeTrack: the tag is a rolling track (ghost 6-alpine, gitea 1.26)
	// and the version field carries the resolved concrete version
	// (6.44.1, 1.26.2). New release = the track tag's digest moving.
	modeTrack
)

// appEntry is the classified state of one catalog app.yaml.
type appEntry struct {
	Slug    string
	Path    string
	Raw     string
	Ref     imageRef
	TagP    tagParts
	Version string // version: field value
	Mode    bumpMode
	// Track is the entry's release track (GH #1956), "" when it has none.
	// A tracked entry only moves within its track; a newer major is
	// reported and left for a reviewed change.
	Track string

	// Skip is the human-readable reason this app cannot be auto-bumped
	// (unversioned tag scheme, version/tag mismatch, parse failure).
	Skip string
}

// The line patterns match top-level keys only, so a held track's indented
// version and image_channel (GH #1956) are never read or rewritten.
var (
	imageChannelLineRe = regexp.MustCompile(`(?m)^image_channel: (\S+)$`)
	versionLineRe      = regexp.MustCompile(`(?m)^version: "([^"]*)"$`)
	trackLineRe        = regexp.MustCompile(`(?m)^track: "([^"]*)"$`)
)

// classify decides, offline, whether an app.yaml is auto-bumpable. Every
// non-bumpable state carries a named reason — no silent skips.
func classify(slug, path, raw string) appEntry {
	e := appEntry{Slug: slug, Path: path, Raw: raw}

	im := imageChannelLineRe.FindStringSubmatch(raw)
	if im == nil {
		e.Skip = "no image_channel line"
		return e
	}
	ref, err := parseImageRef(im[1])
	if err != nil {
		e.Skip = err.Error()
		return e
	}
	e.Ref = ref

	vm := versionLineRe.FindStringSubmatch(raw)
	if vm == nil {
		e.Skip = "no version line"
		return e
	}
	e.Version = vm[1]
	if tm := trackLineRe.FindStringSubmatch(raw); tm != nil {
		e.Track = tm[1]
	}

	p, ok := parseTag(ref.Tag)
	if !ok {
		e.Skip = fmt.Sprintf("unversioned tag scheme %q — re-pin to a versioned tag to enable auto-bump", ref.Tag)
		return e
	}
	e.TagP = p

	// The panel offers updates by comparing the catalog version: field, so
	// the tag/version pair must be coherent: either the tag core IS the
	// version (pinned mode), or the tag is a track the version refines
	// (6-alpine carrying 6.44.1). Anything else needs a human.
	if e.Version == p.coreString() {
		e.Mode = modePinned
		return e
	}
	if vp, ok := parseTag(e.Version); ok && vp.Prefix == "" && vp.Suffix == "" &&
		len(vp.Core) > len(p.Core) && compareCore(vp.Core[:len(p.Core)], p.Core) == 0 {
		e.Mode = modeTrack
		return e
	}
	e.Skip = fmt.Sprintf("version field %q != tag core %q — needs manual review", e.Version, p.coreString())
	return e
}

// loadCatalog reads and classifies every app in the catalog dir, sorted.
func loadCatalog(dir string) ([]appEntry, error) {
	dirs, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read catalog %s: %w", dir, err)
	}
	var entries []appEntry
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		path := filepath.Join(dir, d.Name(), "app.yaml")
		raw, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		entries = append(entries, classify(d.Name(), path, string(raw)))
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Slug < entries[j].Slug })
	return entries, nil
}

// rewriteAppYaml returns raw with the top-level version: value and
// image_channel tag@digest replaced, leaving every other byte untouched.
func rewriteAppYaml(raw string, e appEntry, newTag, newCore, newDigest string) (string, error) {
	oldPin := ":" + e.Ref.Tag + "@" + e.Ref.Digest
	newPin := ":" + newTag + "@" + newDigest
	out, ok := replaceInLine(raw, imageChannelLineRe, oldPin, newPin)
	if !ok {
		return "", fmt.Errorf("pin %q not found", oldPin)
	}

	oldVer := `version: "` + e.Version + `"`
	newVer := `version: "` + newCore + `"`
	out, ok = replaceInLine(out, versionLineRe, oldVer, newVer)
	if !ok {
		return "", fmt.Errorf("version line %q not found", oldVer)
	}
	return out, nil
}

// replaceInLine replaces old with new inside the first line re matches.
func replaceInLine(raw string, re *regexp.Regexp, old, new string) (string, bool) {
	loc := re.FindStringIndex(raw)
	if loc == nil || !strings.Contains(raw[loc[0]:loc[1]], old) {
		return raw, false
	}
	return raw[:loc[0]] + strings.Replace(raw[loc[0]:loc[1]], old, new, 1) + raw[loc[1]:], true
}

// onTrack reports whether a version belongs to a track: "28.1.0" and "28.2"
// are on "28", "1.27.3" is on "1.27" but not on "1.2". Mirrors
// dockerapp.onTrack, which the panel uses to pick an install's image.
func onTrack(v, track string) bool {
	return track != "" && (v == track || strings.HasPrefix(v, track+"."))
}
