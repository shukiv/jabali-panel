package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
)

// GH #2240: the development channel streams main. It installs each release as
// CI publishes it, without the release signature. Stable (and every channel
// that is not "development") stays signed.

func TestReleaseSignatureRequiredOn(t *testing.T) {
	if !releaseSignatureRequired() {
		t.Skip("this build embeds no release key")
	}
	for channel, want := range map[string]bool{
		"development": false,
		"stable":      true,
		"":            true,
		"Development": true,
		"dev":         true,
	} {
		if got := releaseSignatureRequiredOn(channel); got != want {
			t.Errorf("releaseSignatureRequiredOn(%q) = %v, want %v", channel, got, want)
		}
	}
}

// On the development channel the tarball is accepted on its sha256: no
// .minisig is fetched. Where the signature is required, a missing one stops
// the install.
func TestCheckReleaseSignature_FollowsTheChannel(t *testing.T) {
	if !releaseSignatureRequired() {
		t.Skip("this build embeds no release key")
	}
	var fetched atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	tarPath := filepath.Join(t.TempDir(), "jabali-release-abc1234.tar.gz")
	if err := os.WriteFile(tarPath, []byte("tarball"), 0o600); err != nil {
		t.Fatal(err)
	}
	sigURL := srv.URL + "/jabali-release-abc1234.tar.gz.minisig"
	quiet := func(string, ...any) {}

	if err := checkReleaseSignature(context.Background(), releaseSignatureRequiredOn("development"), tarPath, sigURL, quiet); err != nil {
		t.Fatalf("development: %v, want the tarball accepted", err)
	}
	if n := fetched.Load(); n != 0 {
		t.Fatalf("development fetched the signature %d time(s)", n)
	}

	err := checkReleaseSignature(context.Background(), releaseSignatureRequiredOn("stable"), tarPath, sigURL, quiet)
	if err == nil || !strings.Contains(err.Error(), "requires signed releases") {
		t.Fatalf("stable: err = %v, want the missing signature refused", err)
	}
}

// `jabali update` takes the rule from the channel it read, and applies the
// same rule to the commit it resets to and to the tarball it installs.
func TestUpdate_SignatureRuleComesFromTheChannel(t *testing.T) {
	src, err := os.ReadFile("update.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if !regexp.MustCompile(`requireSignature := releaseSignatureRequired\(\)`).MatchString(s) {
		t.Error("update.go no longer starts from the build's own rule (used when the channel cannot be read)")
	}
	if !regexp.MustCompile(`\} else \{\s*requireSignature = releaseSignatureRequiredOn\(channel\)`).MatchString(s) {
		t.Error("update.go no longer sets the signature rule from the channel it read")
	}
	if !strings.Contains(s, `if resetRef != "HEAD" && requireSignature {`) {
		t.Error("the git step no longer verifies the target commit under the channel's rule")
	}
	if !strings.Contains(s, "installFromRelease(ctx, repoDir, verifiedRelease, requireSignature,") {
		t.Error("the release step no longer installs under the channel's rule")
	}
	if n := strings.Count(s, "releaseSignatureRequired()"); n != 1 {
		t.Errorf("update.go calls releaseSignatureRequired() %d times; only the default may, the rest must follow the channel", n)
	}

	rel, err := os.ReadFile("update_release.go")
	if err != nil {
		t.Fatal(err)
	}
	r := string(rel)
	if strings.Contains(r, "releaseSignatureRequired()") || strings.Contains(r, "verifyReleaseSignature(") {
		t.Error("update_release.go checks signatures by the build's rule instead of the channel's")
	}
	if !strings.Contains(r, "checkReleaseSignature(ctx, requireSignature,") {
		t.Error("update_release.go no longer checks the tarball's signature under the channel's rule")
	}
}
