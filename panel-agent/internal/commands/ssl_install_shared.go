// ssl.install_shared / ssl.delete_shared — one cert/key pair per SHARED
// certificate (JAB-170), stored once under /etc/jabali/ssl/shared/<id>/ and
// served from MANY domains' vhosts by path (Phase 3 wires the vhost template).
// Unlike ssl.install_custom (per-domain, under /etc/letsencrypt/live/<domain>),
// the shared pair is NOT copied per domain — replacing it renews every attached
// domain in one action. The private key stays root-owned 0600 and never leaves
// the box.

package commands

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// sslSharedRoot is a var (not const) so tests can point it at a temp dir.
var sslSharedRoot = "/etc/jabali/ssl/shared"

// A shared-cert id is a ULID (26 Crockford base32 chars). Anchored so it can
// never escape sslSharedRoot as a path component.
var sslSharedIDRegex = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

type sslInstallSharedParams struct {
	ID      string `json:"id"`
	CertPEM string `json:"cert_pem"`
	KeyPEM  string `json:"key_pem"`
}

type sslInstallSharedResponse struct {
	CertPath  string   `json:"cert_path"`
	KeyPath   string   `json:"key_path"`
	SANs      []string `json:"sans"`
	ExpiresAt string   `json:"expires_at"` // RFC3339
}

func sslInstallSharedHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p sslInstallSharedParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "parse params: " + err.Error()}
	}
	if !sslSharedIDRegex.MatchString(p.ID) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid shared-cert id"}
	}
	if strings.TrimSpace(p.CertPEM) == "" || strings.TrimSpace(p.KeyPEM) == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "cert_pem + key_pem required"}
	}
	leaf, err := parseLeafCert(p.CertPEM)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "cert_pem: " + err.Error()}
	}
	if err := validateKeyMatchesCert(p.KeyPEM, leaf); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}

	dir := filepath.Join(sslSharedRoot, p.ID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "mkdir shared: " + err.Error()}
	}
	certPath := filepath.Join(dir, "fullchain.pem")
	keyPath := filepath.Join(dir, "privkey.pem")
	if err := writeAtomic(certPath, []byte(strings.TrimSpace(p.CertPEM)+"\n"), 0o644); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "write cert: " + err.Error()}
	}
	if err := writeAtomic(keyPath, []byte(strings.TrimSpace(p.KeyPEM)+"\n"), 0o600); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "write key: " + err.Error()}
	}

	return sslInstallSharedResponse{
		CertPath:  certPath,
		KeyPath:   keyPath,
		SANs:      certSANs(leaf),
		ExpiresAt: leaf.NotAfter.UTC().Format(time.RFC3339),
	}, nil
}

// certSANs returns the certificate's DNS SANs, lowercased + deduped. Falls back
// to the CN when a (legacy) cert carries no DNS SANs. This is the set the panel
// stores + matches domains against for wildcard-aware attach.
func certSANs(leaf *x509.Certificate) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(leaf.DNSNames))
	for _, n := range leaf.DNSNames {
		n = strings.ToLower(strings.TrimSpace(n))
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	if len(out) == 0 && strings.TrimSpace(leaf.Subject.CommonName) != "" {
		out = append(out, strings.ToLower(strings.TrimSpace(leaf.Subject.CommonName)))
	}
	return out
}

// ssl.delete_shared removes a shared cert's on-disk pair. Idempotent: a missing
// dir is success.
func sslDeleteSharedHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "parse: " + err.Error()}
	}
	if !sslSharedIDRegex.MatchString(p.ID) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid shared-cert id"}
	}
	if err := os.RemoveAll(filepath.Join(sslSharedRoot, p.ID)); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "rm shared: " + err.Error()}
	}
	return map[string]any{"ok": true}, nil
}

func init() {
	Default.Register("ssl.install_shared", sslInstallSharedHandler)
	Default.Register("ssl.delete_shared", sslDeleteSharedHandler)
}
