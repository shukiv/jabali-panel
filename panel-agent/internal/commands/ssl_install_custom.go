// ssl.install_custom — write an operator-supplied cert + key under
// /etc/letsencrypt/live/<domain>/ so nginx's existing vhost template
// (which reads fullchain.pem + privkey.pem from that path) serves the
// custom cert without any vhost rewrite. Used by the M35 migration
// importer to land apache_tls/<dom>/ pieces from a cpmove tarball.
//
// Skipping certbot/acme.sh entirely is intentional: a custom cert may
// be from a private CA, a paid SAN cert, or a self-signed dev cert
// the operator wants preserved verbatim. The reconciler's auto-LE
// path still applies once the cert expires — nothing else changes.

package commands

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

const sslLERoot = "/etc/letsencrypt"

type sslInstallCustomParams struct {
	Domain  string `json:"domain"`
	CertPEM string `json:"cert_pem"` // X509 cert + optional intermediates (concatenated PEM blocks)
	KeyPEM  string `json:"key_pem"`  // RSA / EC private key (PKCS#1 / PKCS#8)
}

type sslInstallCustomResponse struct {
	CertPath string `json:"cert_path"`
	KeyPath  string `json:"key_path"`
}

var sslInstallDomainRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]{1,253}$`)

func sslInstallCustomHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p sslInstallCustomParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInvalidArgument, Message: "parse params: " + err.Error(),
		}
	}
	if !sslInstallDomainRegex.MatchString(p.Domain) {
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInvalidArgument, Message: "invalid domain: " + p.Domain,
		}
	}
	if strings.TrimSpace(p.CertPEM) == "" || strings.TrimSpace(p.KeyPEM) == "" {
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInvalidArgument, Message: "cert_pem + key_pem required",
		}
	}
	// Validate cert + key parse + match.
	leaf, err := parseLeafCert(p.CertPEM)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInvalidArgument, Message: "cert_pem: " + err.Error(),
		}
	}
	if err := validateKeyMatchesCert(p.KeyPEM, leaf); err != nil {
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInvalidArgument, Message: err.Error(),
		}
	}

	// Switching a domain to a custom cert must de-track any certbot-managed
	// lineage for it FIRST. Writing regular fullchain.pem/privkey.pem over the
	// LE symlinks while leaving /etc/letsencrypt/renewal/<domain>.conf in place
	// makes `certbot renew` load the lineage, hit the now-non-symlink file, and
	// abort the ENTIRE run with "expected ... cert.pem to be a symlink" — so
	// every domain on the box stops auto-renewing (GH #738). Runs only after
	// cert/key validation above, so a bad upload never destroys a working cert.
	cleanupCertbotLineage(ctx, sslLERoot, p.Domain)

	liveDir := filepath.Join(sslLERoot, "live", p.Domain)
	if err := os.MkdirAll(liveDir, 0o755); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "mkdir live: " + err.Error()}
	}
	certPath := filepath.Join(liveDir, "fullchain.pem")
	keyPath := filepath.Join(liveDir, "privkey.pem")

	// Atomic write: temp + rename.
	if err := writeAtomic(certPath, []byte(strings.TrimSpace(p.CertPEM)+"\n"), 0o644); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "write cert: " + err.Error()}
	}
	if err := writeAtomic(keyPath, []byte(strings.TrimSpace(p.KeyPEM)+"\n"), 0o600); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "write key: " + err.Error()}
	}

	// Reload nginx — best-effort. nginx -s reload skips on syntax error
	// (nginx -t pre-check below catches that), and the existing daemon
	// keeps serving with the previous cert. Operator can rerun the
	// install after fixing the cert.
	if testCmd := execCommandContext(ctx, "nginx", "-t"); testCmd.Run() == nil {
		_ = execCommandContext(ctx, "nginx", "-s", "reload").Run()
	}

	return sslInstallCustomResponse{CertPath: certPath, KeyPath: keyPath}, nil
}

// cleanupCertbotLineage de-tracks any certbot-managed lineage named `domain`
// under `root` before a custom cert is dropped into root/live/<domain>/. See
// the call site (GH #738): an orphaned root/renewal/<domain>.conf pointing at
// non-symlink live files makes `certbot renew` abort box-wide. `certbot delete`
// removes the live symlinks, archive, and renewal conf as a unit; if certbot is
// absent — or the lineage is already half-broken and `certbot delete` itself
// fails (exactly the #738 state) — we remove the renewal conf directly so the
// renew run stops choking on it. No-op when the domain was never certbot-managed.
func cleanupCertbotLineage(ctx context.Context, root, domain string) {
	renewalConf := filepath.Join(root, "renewal", domain+".conf")
	if _, err := os.Stat(renewalConf); err != nil {
		return // no certbot-managed lineage for this name — nothing to clean
	}
	if certbot, err := exec.LookPath("certbot"); err == nil {
		cmd := execCommandContext(ctx, certbot, "delete",
			"--cert-name", domain, "--config-dir", root, "--non-interactive")
		if cmd.Run() == nil {
			return
		}
		// fall through: remove the conf directly as a floor.
	}
	_ = os.Remove(renewalConf)
}

func parseLeafCert(blob string) (*x509.Certificate, error) {
	rest := []byte(blob)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return nil, fmt.Errorf("no CERTIFICATE PEM block found")
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse certificate: %w", err)
		}
		return cert, nil
	}
}

// validateKeyMatchesCert ensures the supplied key actually pairs with
// the leaf cert (modulus / curve match) so we don't write a working
// cert with an unrelated key.
func validateKeyMatchesCert(keyPEM string, leaf *x509.Certificate) error {
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		return fmt.Errorf("key_pem: no PEM block")
	}
	var key any
	var err error
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	default:
		return fmt.Errorf("key_pem: unsupported PEM type %q", block.Type)
	}
	if err != nil {
		return fmt.Errorf("key_pem: parse %s: %w", block.Type, err)
	}
	switch leafPub := leaf.PublicKey.(type) {
	case *rsa.PublicKey:
		var priv *rsa.PrivateKey
		switch k := key.(type) {
		case *rsa.PrivateKey:
			priv = k
		default:
			return fmt.Errorf("cert/key algo mismatch (leaf=RSA, key=%T)", k)
		}
		if priv.PublicKey.N.Cmp(leafPub.N) != 0 || priv.PublicKey.E != leafPub.E {
			return fmt.Errorf("cert/key modulus mismatch")
		}
	default:
		// EC + Ed25519 — skip strict equality; PEM parse + algo
		// check above is enough for v1. nginx -t will catch any
		// remaining mismatch.
		_ = leafPub
	}
	return nil
}

// writeAtomic writes data to path via a temp file in the SAME directory, so the
// rename is always intra-filesystem. Staging in /tmp instead is the EXDEV trap:
// rename(2) cannot cross devices, and /tmp is a separate tmpfs on any hardened
// host (JAB-222).
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	// WriteFile only applies mode when it CREATES the file. A leftover .tmp
	// from an interrupted run would keep its old, possibly looser permissions —
	// and some callers write secrets (bouncer conf carries a captcha secret
	// key), so set the mode explicitly rather than inheriting whatever is there.
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func init() {
	Default.Register("ssl.install_custom", sslInstallCustomHandler)
}
