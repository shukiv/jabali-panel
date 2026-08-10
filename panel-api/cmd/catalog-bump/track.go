package main

import "sort"

// maxResolveHeads bounds the manifest HEADs spent resolving a track digest
// to a concrete version tag.
const maxResolveHeads = 15

// concreteCandidates returns tags that could be the concrete version behind
// a track tag: same prefix and suffix, more core components, and the track's
// components as their numeric prefix (track 6-alpine → 6.44.1-alpine).
// Sorted newest-first so the digest match is usually the first HEAD.
func concreteCandidates(track tagParts, tags []string) []string {
	type cand struct {
		tag  string
		core []int
	}
	var cands []cand
	for _, t := range tags {
		p, ok := parseTag(t)
		if !ok || p.Prefix != track.Prefix || p.Suffix != track.Suffix {
			continue
		}
		if len(p.Core) <= len(track.Core) {
			continue
		}
		if compareCore(p.Core[:len(track.Core)], track.Core) != 0 {
			continue
		}
		cands = append(cands, cand{t, p.Core})
	}
	sort.Slice(cands, func(i, j int) bool {
		a, b := cands[i].core, cands[j].core
		n := len(a)
		if len(b) < n {
			n = len(b)
		}
		if c := compareCore(a[:n], b[:n]); c != 0 {
			return c > 0
		}
		return len(a) > len(b)
	})
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.tag
	}
	return out
}

// resolveConcrete finds the concrete version tag whose manifest digest equals
// the track tag's digest. Returns "" when no candidate matches within the
// HEAD budget — the caller reports that instead of guessing.
func resolveConcrete(client *regClient, host, repo string, track tagParts, tags []string, trackDigest string) string {
	cands := concreteCandidates(track, tags)
	if len(cands) > maxResolveHeads {
		cands = cands[:maxResolveHeads]
	}
	for _, t := range cands {
		d, err := client.digest(host, repo, t)
		if err != nil {
			continue
		}
		if d == trackDigest {
			p, _ := parseTag(t)
			return p.coreString()
		}
	}
	return ""
}
