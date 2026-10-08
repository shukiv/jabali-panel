package main

import (
	"strings"
	"testing"
)

// GH #1956: a catalog entry with a release track only moves within it. The
// bumper reports a newer major instead of pinning it, because the panel's
// Update would take every existing install there.

func TestClassifyReadsTheTrack(t *testing.T) {
	e := classify("odoo", "x", "version: \"20.0\"\ntrack: \"20\"\nimage_channel: odoo:20.0@"+testDigest+"\n")
	if e.Skip != "" || e.Track != "20" {
		t.Fatalf("classify: %+v", e)
	}
	held := classify("odoo", "x", "version: \"20.0\"\nimage_channel: odoo:20.0@"+testDigest+"\nheld_tracks:\n  - track: \"19\"\n")
	if held.Track != "" {
		t.Fatalf("an indented track is a held track's, not the entry's: %+v", held)
	}
}

func TestTrackedEntryHoldsANewMajor(t *testing.T) {
	newDigest := "sha256:" + strings.Repeat("ef", 32)
	f := newFakeRegistry(t, false,
		[]string{"19.0", "20.0", "20.1", "21.0"},
		map[string]string{"20.1": newDigest, "21.0": "sha256:" + strings.Repeat("21", 32)}, 0)
	dir := t.TempDir()
	appDir := dir + "/acmeapp"
	yaml := "version: \"20.0\"\ntrack: \"20\"\nimage_channel: " + f.host() + "/acme/app:20.0@" + testDigest + "\n"
	if err := createApp(appDir, yaml); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	changed, hadErr, err := run(dir, false, testClient(), &out)
	if err != nil || hadErr || changed != 1 {
		t.Fatalf("changed=%d hadErr=%v err=%v\n%s", changed, hadErr, err, out.String())
	}
	got, _ := readFile(appDir + "/app.yaml")
	want := "version: \"20.1\"\ntrack: \"20\"\nimage_channel: " + f.host() + "/acme/app:20.1@" + newDigest + "\n"
	if got != want {
		t.Fatalf("rewritten app.yaml:\n%s\nwant (20.1, not 21.0):\n%s", got, want)
	}
	if !strings.Contains(out.String(), "⏸ new major `21.0` held") || strings.Contains(out.String(), "MAJOR bump") {
		t.Fatalf("summary should hold 21.0, not bump to it:\n%s", out.String())
	}
}

func TestTrackedEntryUpToDateStillReportsTheHeldMajor(t *testing.T) {
	f := newFakeRegistry(t, false, []string{"20.0", "21.0"},
		map[string]string{"20.0": testDigest, "21.0": "sha256:" + strings.Repeat("21", 32)}, 0)
	dir := t.TempDir()
	yaml := "version: \"20.0\"\ntrack: \"20\"\nimage_channel: " + f.host() + "/acme/app:20.0@" + testDigest + "\n"
	if err := createApp(dir+"/acmeapp", yaml); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	changed, _, err := run(dir, false, testClient(), &out)
	if err != nil || changed != 0 {
		t.Fatalf("changed=%d err=%v\n%s", changed, err, out.String())
	}
	if got, _ := readFile(dir + "/acmeapp/app.yaml"); got != yaml {
		t.Fatalf("app.yaml changed:\n%s", got)
	}
	if !strings.Contains(out.String(), "✅ up to date — ⏸ new major `21.0` held") {
		t.Fatalf("summary:\n%s", out.String())
	}
}

// Only the top-level version and image_channel lines are rewritten; a held
// track's lines stay byte-identical, even where they read the same.
func TestRewriteAppYamlLeavesHeldTracksAlone(t *testing.T) {
	raw := "held_tracks:\n  - track: \"19\"\n    version: \"20.0\"\n    image_channel: odoo:20.0@" + testDigest + "\n" +
		"version: \"20.0\"\ntrack: \"20\"\nimage_channel: odoo:20.0@" + testDigest + "\n"
	e := classify("odoo", "x", raw)
	newDigest := "sha256:" + strings.Repeat("ab", 32)
	out, err := rewriteAppYaml(raw, e, "20.1", "20.1", newDigest)
	if err != nil {
		t.Fatal(err)
	}
	want := "held_tracks:\n  - track: \"19\"\n    version: \"20.0\"\n    image_channel: odoo:20.0@" + testDigest + "\n" +
		"version: \"20.1\"\ntrack: \"20\"\nimage_channel: odoo:20.1@" + newDigest + "\n"
	if out != want {
		t.Fatalf("rewrite:\n%s\nwant:\n%s", out, want)
	}
}
