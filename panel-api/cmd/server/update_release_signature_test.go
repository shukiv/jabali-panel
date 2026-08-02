package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/blake2b"
)

// signFor produces a minisign-format signature. Signing exists only in tests —
// production code must not be able to make one, or the offline-key property is
// fiction (ADR-0162).
func signFor(t *testing.T, artifact []byte) (pubLine, sigFile string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatalf("keyid: %v", err)
	}
	pubLine = base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), id[:]...), pub...))
	h := blake2b.Sum512(artifact)
	sig := ed25519.Sign(priv, h[:])
	sigLine := base64.StdEncoding.EncodeToString(append(append([]byte("ED"), id[:]...), sig...))
	global := ed25519.Sign(priv, append(append([]byte{}, sig...), []byte("t")...))
	sigFile = fmt.Sprintf("untrusted comment: x\n%s\ntrusted comment: t\n%s\n",
		sigLine, base64.StdEncoding.EncodeToString(global))
	return pubLine, sigFile
}

// serveSig hands out the signature at any path, or 404s when body is empty —
// standing in for a release that has not been signed.
func serveSig(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if body == "" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeTarball(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "jabali-release.tar.gz")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

// THE test for this change: with a key embedded, a release that has no
// signature must NOT install.
//
// The issue was explicit that "a signature check that silently skips when the
// signature is absent provides no defense" — and it is right, because an
// attacker who substitutes the tarball also controls whether a .minisig is
// there. Absent must never mean fine.
func TestMissingSignatureIsFatalWhenKeyIsEmbedded(t *testing.T) {
	tar := writeTarball(t, "trojaned tarball")
	pubLine, _ := signFor(t, []byte("genuine tarball"))
	srv := serveSig(t, "") // release carries no .minisig

	err := verifyReleaseSignatureWith(context.Background(), pubLine, tar, srv.URL+"/x.minisig", func(string, ...any) {})
	if err == nil {
		t.Fatal("a missing signature must abort the update, not be treated as unsigned-and-fine")
	}
	if !strings.Contains(err.Error(), "signature could not be fetched") {
		t.Errorf("error should say the signature was unavailable, got: %v", err)
	}
}

func TestGenuineSignatureInstalls(t *testing.T) {
	const body = "genuine tarball"
	tar := writeTarball(t, body)
	pubLine, sig := signFor(t, []byte(body))
	srv := serveSig(t, sig)

	var logged []string
	if err := verifyReleaseSignatureWith(context.Background(), pubLine, tar, srv.URL+"/x.minisig",
		func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }); err != nil {
		t.Fatalf("genuine signature rejected: %v", err)
	}
	if len(logged) == 0 || !strings.Contains(strings.Join(logged, " "), "signature verified") {
		t.Error("a verified signature should be visible in the update output")
	}
}

// The scenario the whole ADR is about: attacker replaces BOTH the tarball and
// its checksum. The checksum check passes; the signature must not.
func TestTamperedTarballWithValidChecksumFails(t *testing.T) {
	pubLine, sig := signFor(t, []byte("genuine tarball"))
	tar := writeTarball(t, "trojaned tarball") // checksum sidecar would match this
	srv := serveSig(t, sig)

	err := verifyReleaseSignatureWith(context.Background(), pubLine, tar, srv.URL+"/x.minisig", func(string, ...any) {})
	if err == nil {
		t.Fatal("a tarball that does not match its signature must be refused")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Errorf("error should name the mismatch, got: %v", err)
	}
}

// A build whose embedded key is corrupt must refuse to install, not quietly
// degrade to unverified — that would turn a typo into a silent loss of the
// control.
func TestUnparsableEmbeddedKeyRefusesToInstall(t *testing.T) {
	const body = "tarball"
	tar := writeTarball(t, body)
	_, sig := signFor(t, []byte(body))
	srv := serveSig(t, sig)

	err := verifyReleaseSignatureWith(context.Background(), "!!!not-a-key!!!", tar, srv.URL+"/x.minisig", func(string, ...any) {})
	if err == nil {
		t.Fatal("an unusable embedded key must abort, not fall through to unverified")
	}
	if !strings.Contains(err.Error(), "unusable") {
		t.Errorf("error should name the bad key, got: %v", err)
	}
}

// With no key compiled in, this build predates enforcement: the checksum is the
// only control and the update proceeds. That state is reachable only at build
// time, never by an attacker omitting a file.
func TestNoEmbeddedKeyMeansNoEnforcement(t *testing.T) {
	tar := writeTarball(t, "anything")
	srv := serveSig(t, "")
	if err := verifyReleaseSignatureWith(context.Background(), "", tar, srv.URL+"/x.minisig", func(string, ...any) {}); err != nil {
		t.Fatalf("a build with no embedded key must not require signatures: %v", err)
	}
}

// The .minisig URL is derived by appending, not by replacing the extension —
// getting this wrong would 404 on every release and brick updates fleet-wide.
func TestReleaseSignatureURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://x/jabali-release.tar.gz":         "https://x/jabali-release.tar.gz.minisig",
		"https://x/jabali-release-1a2b3c4.tar.gz": "https://x/jabali-release-1a2b3c4.tar.gz.minisig",
	} {
		if got := releaseSignatureURL(in); got != want {
			t.Errorf("releaseSignatureURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// Guards the shipped default. If a key is ever committed, this test is the
// reminder to also confirm a signed release exists — otherwise every box hard-
// fails its next update.
func TestShippedBuildSignatureState(t *testing.T) {
	if releaseSignatureRequired() {
		t.Log("NOTE: this build enforces release signatures. Every release must " +
			"carry a .minisig made by the matching key, or updates will refuse to install.")
	}
}
