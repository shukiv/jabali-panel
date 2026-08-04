package sslorigin

import (
	"os"
	"path/filepath"
	"testing"
)

// realSelfSignedVhost is copied from a live destination box — the shape that
// produces the 526 (JAB-224).
const realSelfSignedVhost = `server {
    listen 443 ssl;
    server_name 4lion.com www.4lion.com;
    ssl_certificate /etc/ssl/jabali-selfsigned/4lion.com/fullchain.pem;
    ssl_certificate_key /etc/ssl/jabali-selfsigned/4lion.com/privkey.pem;
    root /home/x/domains/4lion.com/public_html;
}`

const realLEVhost = `server {
    listen 443 ssl;
    server_name good.com;
    ssl_certificate /etc/letsencrypt/live/good.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/good.com/privkey.pem;
}`

// neverSelfSigned stands in for "the cert file said it is CA-issued".
func neverSelfSigned(string) (bool, bool) { return false, true }

// unreadable stands in for "could not open the cert".
func unreadable(string) (bool, bool) { return false, false }

func TestClassifyBody_SelfSignedIsUnsafe(t *testing.T) {
	o := ClassifyBody("4lion.com", realSelfSignedVhost, neverSelfSigned)

	if o.Kind != KindSelfSigned {
		t.Fatalf("kind = %q, want %q", o.Kind, KindSelfSigned)
	}
	if o.CertPath != "/etc/ssl/jabali-selfsigned/4lion.com/fullchain.pem" {
		t.Errorf("cert path = %q", o.CertPath)
	}
	if o.Kind.SafeForStrictTLS() {
		t.Error("self-signed must not be reported as strict-TLS safe")
	}
}

func TestClassifyBody_LetsEncryptIsSafe(t *testing.T) {
	o := ClassifyBody("good.com", realLEVhost, neverSelfSigned)
	if o.Kind != KindLetsEncrypt || !o.Kind.SafeForStrictTLS() {
		t.Fatalf("kind = %q safe = %v", o.Kind, o.Kind.SafeForStrictTLS())
	}
}

// ssl_certificate_key must never be mistaken for ssl_certificate — matching the
// key path would classify by the wrong directory and, worse, read a PRIVATE KEY.
func TestClassifyBody_DoesNotMatchCertificateKey(t *testing.T) {
	body := `server {
    ssl_certificate_key /etc/letsencrypt/live/x.com/privkey.pem;
    ssl_certificate /etc/ssl/jabali-selfsigned/x.com/fullchain.pem;
}`
	o := ClassifyBody("x.com", body, neverSelfSigned)
	if o.CertPath != "/etc/ssl/jabali-selfsigned/x.com/fullchain.pem" {
		t.Fatalf("matched the wrong directive: %q", o.CertPath)
	}
}

// A commented-out directive is not configuration.
func TestClassifyBody_IgnoresCommentedDirective(t *testing.T) {
	body := `server {
    # ssl_certificate /etc/letsencrypt/live/x.com/fullchain.pem;
    ssl_certificate /etc/ssl/jabali-selfsigned/x.com/fullchain.pem;
}`
	o := ClassifyBody("x.com", body, neverSelfSigned)
	if o.Kind != KindSelfSigned {
		t.Fatalf("commented line won: kind=%q path=%q", o.Kind, o.CertPath)
	}
}

func TestClassifyBody_NoTLSIsNone(t *testing.T) {
	o := ClassifyBody("plain.com", "server { listen 80; }", neverSelfSigned)
	if o.Kind != KindNone || o.Kind.SafeForStrictTLS() {
		t.Fatalf("kind = %q", o.Kind)
	}
}

// A custom path holding a self-issued cert 526s exactly like the placeholder.
func TestClassifyBody_CustomButSelfIssuedIsUnsafe(t *testing.T) {
	body := `server { ssl_certificate /etc/ssl/mine/x.pem; }`
	o := ClassifyBody("x.com", body, func(string) (bool, bool) { return true, true })

	if o.Kind != KindSelfSigned {
		t.Fatalf("kind = %q, want self-signed", o.Kind)
	}
	if o.Kind.SafeForStrictTLS() {
		t.Error("self-issued custom cert must not be strict-TLS safe")
	}
}

// A custom cert we cannot read is assumed valid — refusing to read an
// operator's cert is not evidence against it.
func TestClassifyBody_CustomUnreadableAssumedValid(t *testing.T) {
	body := `server { ssl_certificate /etc/ssl/mine/x.pem; }`
	o := ClassifyBody("x.com", body, unreadable)

	if o.Kind != KindCustom || !o.Kind.SafeForStrictTLS() {
		t.Fatalf("kind = %q", o.Kind)
	}
	if o.Detail == "" {
		t.Error("unreadable custom cert should say so")
	}
}

// "I could not look" must never read as "it is fine".
func TestClassify_MissingVhostIsUnknownNotSafe(t *testing.T) {
	o := Classify("ghost.com", t.TempDir())

	if o.Kind != KindUnknown {
		t.Fatalf("kind = %q, want unknown", o.Kind)
	}
	if o.Kind.SafeForStrictTLS() {
		t.Error("unknown must not be reported as strict-TLS safe")
	}
}

func TestClassify_ReadsVhostFromDisk(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "4lion.com.conf"), []byte(realSelfSignedVhost), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if o := Classify("4lion.com", dir); o.Kind != KindSelfSigned {
		t.Fatalf("kind = %q", o.Kind)
	}
}

// Some boxes name the enabled site without .conf.
func TestClassify_FallsBackToUnsuffixedFilename(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "good.com"), []byte(realLEVhost), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if o := Classify("good.com", dir); o.Kind != KindLetsEncrypt {
		t.Fatalf("kind = %q", o.Kind)
	}
}
