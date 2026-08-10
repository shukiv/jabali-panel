package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestConcreteCandidates(t *testing.T) {
	track, _ := parseTag("6-alpine")
	got := concreteCandidates(track, []string{
		"6-alpine", "6.44.0-alpine", "6.44.1-alpine", "6.44.1", "5.99.0-alpine",
		"7.0.0-alpine", "6.44.1-alpine.arm", "latest",
	})
	// Same suffix, refines core 6, newest first. 6.44.1 (no suffix),
	// 5.x and 7.x (different track), and 6-alpine itself are excluded.
	want := []string{"6.44.1-alpine", "6.44.0-alpine"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
}

// Track flow end-to-end: the track tag's digest moved upstream; the bumper
// refreshes the pin and resolves the version field from the concrete tag
// sharing the new digest.
func TestRunTrackDigestRefresh(t *testing.T) {
	newDigest := "sha256:" + strings.Repeat("aa", 32)
	f := newFakeRegistry(t, false,
		[]string{"2", "2.4.0", "2.4.1", "2.5.0-beta1"},
		map[string]string{
			"2":     newDigest, // track moved off testDigest
			"2.4.1": newDigest, // concrete twin
			"2.4.0": "sha256:" + strings.Repeat("bb", 32),
		}, 0)

	dir := t.TempDir()
	appDir := dir + "/kuma"
	yaml := "version: \"2.4.0\"\nimage_channel: " + f.host() + "/acme/app:2@" + testDigest + "\n"
	if err := createApp(appDir, yaml); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	changed, hadErr, err := run(dir, false, testClient(), &out)
	if err != nil {
		t.Fatal(err)
	}
	if hadErr || changed != 1 {
		t.Fatalf("changed=%d hadErr=%v\n%s", changed, hadErr, out.String())
	}
	got, err := readFile(appDir + "/app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := "version: \"2.4.1\"\nimage_channel: " + f.host() + "/acme/app:2@" + newDigest + "\n"
	if got != want {
		t.Fatalf("rewritten:\n%s\nwant:\n%s", got, want)
	}
	if !strings.Contains(out.String(), "digest refresh") {
		t.Fatalf("summary missing refresh row:\n%s", out.String())
	}
}

// When no concrete tag shares the new digest, the version field is left
// alone and the row is flagged — never guessed.
func TestRunTrackUnresolved(t *testing.T) {
	newDigest := "sha256:" + strings.Repeat("aa", 32)
	f := newFakeRegistry(t, false,
		[]string{"2", "2.4.0"},
		map[string]string{
			"2":     newDigest,
			"2.4.0": "sha256:" + strings.Repeat("bb", 32),
		}, 0)

	dir := t.TempDir()
	yaml := "version: \"2.4.0\"\nimage_channel: " + f.host() + "/acme/app:2@" + testDigest + "\n"
	if err := createApp(dir+"/kuma", yaml); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if _, _, err := run(dir, false, testClient(), &out); err != nil {
		t.Fatal(err)
	}
	got, _ := readFile(dir + "/kuma/app.yaml")
	if !strings.Contains(got, `version: "2.4.0"`) || !strings.Contains(got, newDigest) {
		t.Fatalf("want digest bumped + version untouched:\n%s", got)
	}
	if !strings.Contains(out.String(), "resolved version unknown") {
		t.Fatalf("summary missing unresolved flag:\n%s", out.String())
	}
}

// A newer track tag (2 → 3) is an advance: tag, digest, and — when
// resolvable — the version field all move, flagged as a major bump.
func TestRunTrackAdvanceMajor(t *testing.T) {
	d3 := "sha256:" + strings.Repeat("cc", 32)
	f := newFakeRegistry(t, false,
		[]string{"2", "3", "3.0.1", "2.4.0"},
		map[string]string{
			"3":     d3,
			"3.0.1": d3,
		}, 0)

	dir := t.TempDir()
	yaml := "version: \"2.4.0\"\nimage_channel: " + f.host() + "/acme/app:2@" + testDigest + "\n"
	if err := createApp(dir+"/kuma", yaml); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if _, _, err := run(dir, false, testClient(), &out); err != nil {
		t.Fatal(err)
	}
	got, _ := readFile(dir + "/kuma/app.yaml")
	if !strings.Contains(got, ":3@"+d3) || !strings.Contains(got, `version: "3.0.1"`) {
		t.Fatalf("want track advance to 3 with resolved version:\n%s", got)
	}
	if !strings.Contains(out.String(), "MAJOR") {
		t.Fatalf("summary missing major flag:\n%s", out.String())
	}
}
