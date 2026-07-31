package plesk

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"testing"

	"golang.org/x/crypto/ssh"
)

// GH #429: a private key pasted from a browser textarea often arrives CRLF, and
// ssh.ParsePrivateKey fails on that even though the key is valid — the reporter's
// "publickey rejected" class. parsePEMSigner must normalize CRLF and still parse.
func TestParsePEMSigner_ToleratesCRLF(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	lf := pem.EncodeToMemory(block)

	// LF (baseline) must parse.
	if _, err := parsePEMSigner(lf); err != nil {
		t.Fatalf("LF key should parse: %v", err)
	}
	// CRLF (textarea paste) must ALSO parse after normalization.
	crlf := bytes.ReplaceAll(lf, []byte("\n"), []byte("\r\n"))
	if _, err := parsePEMSigner(crlf); err != nil {
		t.Fatalf("CRLF key should parse after normalization: %v", err)
	}
}

func TestParsePEMSigner_RejectsGarbageClearly(t *testing.T) {
	if _, err := parsePEMSigner([]byte("not a key")); err == nil {
		t.Fatal("garbage should be rejected")
	}
}
