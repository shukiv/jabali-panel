// Package sslorigin answers one question: what certificate is nginx actually
// serving for a domain, and will Cloudflare accept it?
//
// JAB-224. A migrated domain lands on the destination behind jabali's
// self-signed placeholder, because Let's Encrypt HTTP-01 cannot succeed until
// the domain already resolves to this box — and nothing issues a certificate at
// the moment of cutover. A domain proxied through Cloudflare in Full (strict)
// therefore goes live pointing at a self-signed origin and Cloudflare answers
// every request with "Error 526 Invalid SSL certificate". The origin is healthy;
// the site is simply dark.
//
// This is invisible before cutover: the origin serves the site fine over its
// self-signed certificate, so any check that talks straight to the box passes.
// It only fails once real traffic arrives through Cloudflare, which is the worst
// possible moment to discover it. Measured on a live destination box: 70
// self-signed lineages against 21 real ones.
//
// So classification reads the VHOST — the file nginx actually loads — rather
// than the panel's ssl_certificates table. A domain that has never had a
// certificate issued has no row at all, and those are exactly the domains that
// will 526.
package sslorigin

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Kind is what sort of certificate a vhost points at.
type Kind string

const (
	KindLetsEncrypt Kind = "letsencrypt" // /etc/letsencrypt/live/<d>/ — Cloudflare-strict safe
	KindSelfSigned  Kind = "self-signed" // jabali placeholder — 526 behind Cloudflare strict
	KindCustom      Kind = "custom"      // operator-supplied path; trusted unless the cert says otherwise
	KindNone        Kind = "none"        // no ssl_certificate directive — plain HTTP only
	KindUnknown     Kind = "unknown"     // vhost missing or unreadable; refuse to guess
)

// SafeForStrictTLS reports whether an origin of this kind will satisfy a
// Cloudflare Full (strict) connection.
//
// KindUnknown is deliberately NOT safe: a readiness check that reports "fine"
// because it could not look is worse than one that says nothing.
func (k Kind) SafeForStrictTLS() bool {
	return k == KindLetsEncrypt || k == KindCustom
}

// jabaliSelfSignedDir is where the panel parks its placeholder lineages.
const jabaliSelfSignedDir = "/etc/ssl/jabali-selfsigned/"

// letsEncryptLiveDir is certbot's lineage directory.
const letsEncryptLiveDir = "/etc/letsencrypt/live/"

// sslCertificateRe matches nginx's `ssl_certificate <path>;`, and must NOT match
// `ssl_certificate_key`, `ssl_certificate_by_lua_block`, or a commented line.
// The negative lookahead Go's regexp lacks is expressed as "next char is space".
var sslCertificateRe = regexp.MustCompile(`(?m)^[^#\n]*\bssl_certificate[ \t]+([^;\s]+)[ \t]*;`)

// Origin is the verdict for one domain.
type Origin struct {
	Domain   string
	CertPath string
	Kind     Kind
	Detail   string // why, when the answer is not obvious from Kind alone
}

// certPathFromVhost extracts the ssl_certificate path from an nginx config body.
// Returns "" when the vhost serves no TLS.
func certPathFromVhost(body string) string {
	for _, m := range sslCertificateRe.FindAllStringSubmatch(body, -1) {
		if len(m) > 1 && m[1] != "" {
			return m[1]
		}
	}
	return ""
}

// kindForPath classifies purely by where the certificate lives. jabali owns both
// directory layouts, so the path is authoritative for its own certificates.
func kindForPath(path string) Kind {
	switch {
	case path == "":
		return KindNone
	case strings.HasPrefix(path, jabaliSelfSignedDir):
		return KindSelfSigned
	case strings.HasPrefix(path, letsEncryptLiveDir):
		return KindLetsEncrypt
	default:
		return KindCustom
	}
}

// isSelfSignedCert reports whether the leaf certificate is self-issued.
//
// Only consulted for KindCustom: an operator-supplied certificate can itself be
// self-signed, and that 526s exactly the same as jabali's placeholder. Reading
// the first PEM block is deliberate — fullchain.pem is leaf-first, and the
// issuer above it is a real CA whose subject would never match.
func isSelfSignedCert(path string) (bool, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, false
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return false, false
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false, false
	}
	return c.Issuer.String() == c.Subject.String(), true
}

// ClassifyBody is the pure half: given a vhost body, decide the origin kind.
// checkCert is injected so tests need no filesystem.
func ClassifyBody(domain, body string, checkCert func(string) (bool, bool)) Origin {
	path := certPathFromVhost(body)
	kind := kindForPath(path)
	o := Origin{Domain: domain, CertPath: path, Kind: kind}

	switch kind {
	case KindNone:
		o.Detail = "vhost serves no TLS"
	case KindSelfSigned:
		o.Detail = "jabali placeholder — Cloudflare Full (strict) will return 526"
	case KindCustom:
		// A custom path may still hold a self-signed certificate.
		if selfSigned, ok := checkCert(path); ok && selfSigned {
			o.Kind = KindSelfSigned
			o.Detail = "custom certificate is self-issued — Cloudflare Full (strict) will return 526"
		} else if !ok {
			o.Detail = "custom certificate could not be read; assuming it is valid"
		}
	}
	return o
}

// Classify reads the vhost for a domain from nginxDir and classifies it.
//
// Both <domain>.conf and <domain> are probed because the enabled-site filename
// convention has varied; a domain whose vhost cannot be found is reported as
// KindUnknown rather than assumed fine.
func Classify(domain, nginxDir string) Origin {
	for _, name := range []string{domain + ".conf", domain} {
		body, err := os.ReadFile(filepath.Join(nginxDir, name))
		if err != nil {
			continue
		}
		return ClassifyBody(domain, string(body), isSelfSignedCert)
	}
	return Origin{
		Domain: domain,
		Kind:   KindUnknown,
		Detail: "no vhost found in " + nginxDir,
	}
}
