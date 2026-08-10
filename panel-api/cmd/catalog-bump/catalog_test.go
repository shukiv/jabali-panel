package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sounderYaml = `slug: jabali-sounder
popularity: 38
name: Jabali Sounder
version: "0.5.17"
icon: icon.svg

image_channel: ghcr.io/shukiv/jabali-sounder:0.5.17@` + testDigest + `
volume_owner: "10001:10001"
update_mode: manual
`

func TestClassifyBumpable(t *testing.T) {
	e := classify("jabali-sounder", "x", sounderYaml)
	if e.Skip != "" {
		t.Fatalf("unexpected skip: %s", e.Skip)
	}
	if e.Version != "0.5.17" || e.Ref.Tag != "0.5.17" || e.Ref.Host != "ghcr.io" || e.Mode != modePinned {
		t.Fatalf("bad classify: %+v", e)
	}
}

func TestClassifyTrackMode(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{"ghost 6-alpine / 6.44.1",
			"version: \"6.44.1\"\nimage_channel: ghost:6-alpine@" + testDigest + "\n"},
		{"gitea 1.26 / 1.26.2",
			"version: \"1.26.2\"\nimage_channel: gitea/gitea:1.26@" + testDigest + "\n"},
		{"uptime-kuma 2 / 2.4.0",
			"version: \"2.4.0\"\nimage_channel: louislam/uptime-kuma:2@" + testDigest + "\n"},
	}
	for _, c := range cases {
		e := classify("x", "x", c.yaml)
		if e.Skip != "" || e.Mode != modeTrack {
			t.Errorf("%s: skip=%q mode=%v, want track mode", c.name, e.Skip, e.Mode)
		}
	}
	// A version that does NOT refine the track (2.25.5 under tag 2.28.2)
	// stays a mismatch — that's the n8n data bug this tool's corpus caught.
	e := classify("x", "x", "version: \"2.25.5\"\nimage_channel: n8nio/n8n:2.28.2@"+testDigest+"\n")
	if e.Skip == "" || !strings.Contains(e.Skip, "needs manual review") {
		t.Errorf("non-refining version must skip, got %+v", e)
	}
}

func TestClassifySkips(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string // substring of the skip reason
	}{
		{"unversioned latest",
			"version: \"latest\"\nimage_channel: mediacms/mediacms:latest@" + testDigest + "\n",
			"unversioned tag scheme"},
		{"unversioned stable",
			"version: \"stable\"\nimage_channel: mondedie/flarum:stable@" + testDigest + "\n",
			"unversioned tag scheme"},
		{"version/tag mismatch",
			"version: \"9.9.9\"\nimage_channel: gitea/gitea:1.26@" + testDigest + "\n",
			"needs manual review"},
		{"unpinned image",
			"version: \"1.0\"\nimage_channel: gitea/gitea:1.26\n",
			"not digest-pinned"},
		{"missing version line",
			"image_channel: gitea/gitea:1.26@" + testDigest + "\n",
			"no version line"},
	}
	for _, c := range cases {
		e := classify("x", "x", c.yaml)
		if e.Skip == "" || !strings.Contains(e.Skip, c.want) {
			t.Errorf("%s: skip = %q, want containing %q", c.name, e.Skip, c.want)
		}
	}
}

func TestRewriteAppYaml(t *testing.T) {
	e := classify("jabali-sounder", "x", sounderYaml)
	newDigest := "sha256:" + strings.Repeat("ab", 32)
	out, err := rewriteAppYaml(sounderYaml, e, "0.5.22", "0.5.22", newDigest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `version: "0.5.22"`) {
		t.Error("version line not rewritten")
	}
	if !strings.Contains(out, "image_channel: ghcr.io/shukiv/jabali-sounder:0.5.22@"+newDigest) {
		t.Error("image_channel not rewritten")
	}
	// Everything except the two rewritten values must be byte-identical —
	// volume_owner's "10001:10001" especially must survive untouched.
	restore := strings.Replace(out, "0.5.22@"+newDigest, "0.5.17@"+testDigest, 1)
	restore = strings.Replace(restore, `version: "0.5.22"`, `version: "0.5.17"`, 1)
	if restore != sounderYaml {
		t.Error("rewrite touched bytes outside the two pinned values")
	}
}

// TestCatalogCorpus walks the real shipped catalog and asserts every app
// classifies into exactly one named bucket: auto-bumpable, or skipped with
// a stated reason from the known-permanent set. A new app with a tag scheme
// the bumper can't handle fails here instead of silently never updating.
func TestCatalogCorpus(t *testing.T) {
	root := filepath.Join("..", "..", "..", "install", "docker-apps")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("catalog dir not found: %v", err)
	}
	entries, err := loadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 40 {
		t.Fatalf("expected the full catalog, got %d apps", len(entries))
	}

	// Permanent, by-design skips. Re-pinning these to versioned tags would
	// bring them under auto-bump — until then they are listed, not silent.
	permanentSkips := map[string]string{
		"mediacms": "unversioned tag scheme", // :latest
		"flarum":   "unversioned tag scheme", // :stable
		"dokuwiki": "unversioned tag scheme", // version-YYYY-MM-DD
	}

	for _, e := range entries {
		if e.Skip == "" {
			continue
		}
		want, known := permanentSkips[e.Slug]
		if !known {
			t.Errorf("%s: unexpected skip %q — extend the bumper or add a justified permanent skip", e.Slug, e.Skip)
			continue
		}
		if !strings.Contains(e.Skip, want) {
			t.Errorf("%s: skip reason %q, want containing %q", e.Slug, e.Skip, want)
		}
	}
}
