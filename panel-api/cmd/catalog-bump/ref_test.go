package main

import (
	"reflect"
	"testing"
)

const testDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestParseImageRef(t *testing.T) {
	cases := []struct {
		in   string
		want imageRef
	}{
		{"ghost:6-alpine@" + testDigest,
			imageRef{"registry-1.docker.io", "library/ghost", "6-alpine", testDigest}},
		{"gitea/gitea:1.26@" + testDigest,
			imageRef{"registry-1.docker.io", "gitea/gitea", "1.26", testDigest}},
		{"ghcr.io/shukiv/jabali-sounder:0.5.22@" + testDigest,
			imageRef{"ghcr.io", "shukiv/jabali-sounder", "0.5.22", testDigest}},
		{"lscr.io/linuxserver/dokuwiki:version-2025-05-14b@" + testDigest,
			imageRef{"lscr.io", "linuxserver/dokuwiki", "version-2025-05-14b", testDigest}},
		{"codeberg.org/forgejo/forgejo:15.0.3@" + testDigest,
			imageRef{"codeberg.org", "forgejo/forgejo", "15.0.3", testDigest}},
		{"docker.io/searxng/searxng:2026.6.20-fd42d4fda@" + testDigest,
			imageRef{"registry-1.docker.io", "searxng/searxng", "2026.6.20-fd42d4fda", testDigest}},
	}
	for _, c := range cases {
		got, err := parseImageRef(c.in)
		if err != nil {
			t.Errorf("parseImageRef(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseImageRef(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestParseImageRefRejects(t *testing.T) {
	for _, in := range []string{
		"ghost:6-alpine",                          // no digest (violates GH #458 pin policy)
		"ghost@" + testDigest,                     // no tag
		"ghost:latest@sha256:short",               // malformed digest
		"nginx:1.25@sha256:ZZZ" + testDigest[10:], // non-hex digest
	} {
		if _, err := parseImageRef(in); err == nil {
			t.Errorf("parseImageRef(%q) should fail", in)
		}
	}
}

func TestParseTag(t *testing.T) {
	cases := []struct {
		in     string
		ok     bool
		prefix string
		core   []int
		suffix string
	}{
		{"0.5.22", true, "", []int{0, 5, 22}, ""},
		{"v8.2.1-trixie", true, "v", []int{8, 2, 1}, "-trixie"},
		{"6-alpine", true, "", []int{6}, "-alpine"},
		{"2", true, "", []int{2}, ""},
		{"18.0", true, "", []int{18, 0}, ""},
		{"2026.6.20-fd42d4fda", true, "", []int{2026, 6, 20}, "-fd42d4fda"},
		{"5-apache", true, "", []int{5}, "-apache"},
		{"latest", false, "", nil, ""},
		{"stable", false, "", nil, ""},
		{"version-2025-05-14b", false, "", nil, ""},
	}
	for _, c := range cases {
		got, ok := parseTag(c.in)
		if ok != c.ok {
			t.Errorf("parseTag(%q) ok=%v, want %v", c.in, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if got.Prefix != c.prefix || !reflect.DeepEqual(got.Core, c.core) || got.Suffix != c.suffix {
			t.Errorf("parseTag(%q) = %+v, want {%q %v %q}", c.in, got, c.prefix, c.core, c.suffix)
		}
	}
}

func TestPickBestTag(t *testing.T) {
	cases := []struct {
		name string
		cur  string
		tags []string
		want string // "" = no bump
	}{
		{"patch bump", "0.5.17",
			[]string{"0.5.16", "0.5.17", "0.5.21", "0.5.22", "latest"}, "0.5.22"},
		{"prefix preserved", "v2.11.0",
			[]string{"2.12.0", "v2.12.0", "v2.11.0"}, "v2.12.0"},
		{"suffix exact", "v8.2.1-trixie",
			[]string{"v8.3.0", "v8.3.0-bookworm", "v8.3.0-trixie"}, "v8.3.0-trixie"},
		{"prerelease excluded by suffix", "1.36.0",
			[]string{"1.37.0-rc1", "1.36.0"}, ""},
		{"major track only", "2",
			[]string{"2", "2.1.3", "3", "3.0.1", "10-beta"}, "3"},
		{"minor track keeps count", "1.26",
			[]string{"1.26.3", "1.27", "1.26"}, "1.27"},
		{"distro suffix track", "33-apache",
			[]string{"34", "34-apache", "34-fpm"}, "34-apache"},
		{"hash suffix wildcard", "2026.6.20-fd42d4fda",
			[]string{"2026.7.4-ab12cd34e", "2026.7.5-notahash!", "2026.6.20-fd42d4fda"}, "2026.7.4-ab12cd34e"},
		{"nothing newer", "9.4.0", []string{"9.4.0", "9.3.0"}, ""},
		{"numeric compare not lexical", "0.9.9", []string{"0.10.0"}, "0.10.0"},
	}
	for _, c := range cases {
		cur, ok := parseTag(c.cur)
		if !ok {
			t.Fatalf("%s: bad current tag %q", c.name, c.cur)
		}
		got, _, found := pickBestTag(cur, c.tags)
		if !found {
			got = ""
		}
		if got != c.want {
			t.Errorf("%s: pickBestTag = %q, want %q", c.name, got, c.want)
		}
	}
}
