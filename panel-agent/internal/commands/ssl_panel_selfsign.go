package commands

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/panelcert"
)

// ssl.panel.selfsign regenerates the panel's self-signed hostname cert at
// /etc/jabali/tls/panel.{crt,key} for the CURRENT panel FQDN, then reloads
// nginx and restarts jabali-panel so the new cert is actually served on
// :8443. It is the live equivalent of install.sh's provision_tls_cert
// self-sign step.
//
// Why it exists (JAB-389): changing the panel hostname/FQDN through the
// panel Settings updates the OS hostname and the panel_certificate DB rows,
// but nothing regenerates the openssl self-signed cert on disk or restarts
// the Go TLS listener that caches it — so :8443 keeps presenting the OLD
// hostname's cert (a name mismatch every browser rejects) until Let's
// Encrypt is enabled. The panel-cert reconciler dispatches this verb
// whenever the on-disk cert has drifted from server_settings.hostname while
// the panel is in self-signed mode.
//
// Invariants ported verbatim from install.sh provision_tls_cert:
//   - PRESERVE any unexpired cert whose issuer Organization is neither empty
//     nor panelSelfSignOrg ("Jabali Panel"): that is a deployed Let's Encrypt
//     or custom cert. The 2026-05-09 regression clobbered a live LE cert
//     with a self-signed one; the reconciler's drift check applies the same
//     guard (both use panelcert.Check), and this handler re-checks so a stale
//     dispatch can never destroy a live CA cert. An EXPIRED CA cert is
//     replaced (GH #2166): it is no live cert, and with Let's Encrypt off
//     nothing else would ever replace it.
//   - No-churn: when the existing self-signed cert already covers the FQDN
//     (CN + the hostname and mail.<hostname> SANs) and is unexpired, do
//     nothing — no regenerate, no nginx reload, no panel restart. Unless
//     the caller asks for a new key (new_key): then it is replaced too.
//   - SAN set identical to install.sh:
//     [hostname, mail.hostname, localhost, 127.0.0.1, <ip>].
type sslPanelSelfSignParams struct {
	Hostname string `json:"hostname"`
	IP       string `json:"ip,omitempty"`
	// NewKey replaces a current self-signed cert too, with a new key, for an
	// operator whose old key may have been copied (`jabali panel-cert
	// new-key`). A live CA cert is still kept: Let's Encrypt gives it a new
	// key at each renewal.
	NewKey bool `json:"new_key,omitempty"`
}

type sslPanelSelfSignResponse struct {
	Regenerated bool   `json:"regenerated"`
	Reason      string `json:"reason,omitempty"`
	CertPath    string `json:"cert_path"`
	ExpiresAt   string `json:"expires_at,omitempty"`
}

// Overridable in tests.
var (
	panelSelfSignCertPath = panelcert.DefaultCertPath
	panelSelfSignKeyPath  = "/etc/jabali/tls/panel.key"
	// panelSelfSignReloadFn applies the freshly written cert to the running
	// services. Seam so unit tests don't spawn systemctl.
	panelSelfSignReloadFn = defaultPanelSelfSignReload
	// panelHSTSSnippetPath is the include the panel vhost pulls the HSTS header
	// from (GH #1507). Overridable in tests.
	panelHSTSSnippetPath = "/etc/nginx/snippets/jabali-panel-hsts.conf"
)

// writePanelHSTSSnippetOff clears the HSTS header the panel :8443 vhost includes.
// GH #1507: Strict-Transport-Security over a self-signed cert hard-blocks the
// browser with no exception path, so once this verb (re)writes a self-signed cert
// HSTS must be off. jabali-panel-cert.sh writes it back ON when a CA cert deploys.
// Best-effort: a stale ON snippet only re-pins browsers that were already pinned
// during a prior CA-cert period.
func writePanelHSTSSnippetOff() {
	_ = os.MkdirAll(filepath.Dir(panelHSTSSnippetPath), 0o755)
	_ = os.WriteFile(panelHSTSSnippetPath,
		[]byte("# GH #1507: HSTS omitted — the panel is serving a self-signed cert. It hard-blocks\n"+
			"# the browser over an untrusted cert; enabled automatically once a CA cert deploys.\n"),
		0o644)
}

// panelSelfSignOrg is the Subject/Issuer Organization stamped onto our
// self-signed panel cert (matches install.sh's -subj "…/O=Jabali Panel").
// The preserve-guard keys off this exact string, so it MUST stay in sync
// with install.sh provision_tls_cert.
const panelSelfSignOrg = panelcert.SelfSignOrg

// panelSelfSignMu serializes ssl.panel.selfsign. The reconciler and the
// admin's "Regenerate self-signed" action can both dispatch it; two runs at
// once could write one run's cert next to the other's key.
var panelSelfSignMu sync.Mutex

// defaultPanelSelfSignReload reloads nginx and restarts jabali-panel.
//
// The context is deliberately DETACHED from the caller's RPC context: this
// restarts jabali-panel, which — when the panel-cert reconciler dispatched
// us — is the very process that opened the panel→agent RPC. Tying the
// restart to the request ctx would let the panel dying mid-restart cancel
// the systemctl call and strand jabali-panel stopped. nginx only reloads
// (re-reads the cert on the next handshake); jabali-panel is a Go server
// that caches the cert in memory at startup and does NOT SIGHUP-reread, so
// a full restart is required to serve the new cert on :8443.
func defaultPanelSelfSignReload(_ context.Context) {
	bg, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_ = execCommandContext(bg, "systemctl", "reload", "nginx").Run()
	_ = execCommandContext(bg, "systemctl", "restart", "jabali-panel").Run()
}

func sslPanelSelfSignHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p sslPanelSelfSignParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	p.Hostname = strings.ToLower(strings.TrimSpace(p.Hostname))
	if !sslDomainRegex.MatchString(p.Hostname) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid hostname %q", p.Hostname),
		}
	}
	var ip net.IP
	if p.IP != "" {
		if ip = net.ParseIP(p.IP); ip == nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: fmt.Sprintf("invalid ip %q", p.IP),
			}
		}
	}

	dnsSANs := []string{p.Hostname, "mail." + p.Hostname, "localhost"}
	ipSANs := []net.IP{net.IPv4(127, 0, 0, 1)}
	if ip != nil {
		ipSANs = append(ipSANs, ip)
	}

	panelSelfSignMu.Lock()
	defer panelSelfSignMu.Unlock()

	// Inspect the current cert: preserve a live real-CA cert; skip a
	// self-signed cert that already covers the FQDN and is unexpired.
	data, err := os.ReadFile(panelSelfSignCertPath)
	if err != nil {
		data = nil
	}
	v := panelcert.Check(data, p.Hostname, time.Now())
	switch {
	case v.Action == panelcert.KeepCA:
		return sslPanelSelfSignResponse{
			Regenerated: false,
			Reason:      "preserved: cert issued by " + v.Issuer,
			CertPath:    panelSelfSignCertPath,
			ExpiresAt:   v.NotAfter.UTC().Format(time.RFC3339),
		}, nil
	case v.Action == panelcert.UpToDate && !p.NewKey:
		return sslPanelSelfSignResponse{
			Regenerated: false,
			Reason:      v.Reason,
			CertPath:    panelSelfSignCertPath,
			ExpiresAt:   v.NotAfter.UTC().Format(time.RFC3339),
		}, nil
	}

	expiresAt, err := generatePanelSelfSignedCert(p.Hostname, dnsSANs, ipSANs, panelSelfSignCertPath, panelSelfSignKeyPath)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to generate panel self-signed cert: %v", err),
		}
	}

	// Ownership + mode identical to install.sh provision_tls_cert: cert is
	// public (0644 root:root), the key is group-readable by nginx and the
	// panel (0640 root:www-data). Best-effort — www-data may not exist in a
	// container/CI, where the temp path used by tests needs no chown.
	_ = os.Chmod(panelSelfSignCertPath, 0o644)
	applyPanelKeyOwnership(panelSelfSignKeyPath)

	// GH #1507: :8443 now presents a self-signed cert — HSTS must be OFF before the
	// reload, or a browser that reaches the freshly-served cert gets a strict-
	// transport pin over an untrusted cert (hard block, no exception).
	writePanelHSTSSnippetOff()

	// Logged here, before the restart: when the panel dispatched this, the
	// restart ends the panel process that would log the reply.
	logPanelSelfSignWritten(p.Hostname, v, p.NewKey)

	panelSelfSignReloadFn(ctx)

	return sslPanelSelfSignResponse{
		Regenerated: true,
		CertPath:    panelSelfSignCertPath,
		ExpiresAt:   expiresAt.UTC().Format(time.RFC3339),
	}, nil
}

// logPanelSelfSignWritten records a rewritten panel cert in the agent's log:
// the hostname, why, and the new cert's SHA-256 fingerprint and expiry, as
// browsers and `openssl x509 -noout -fingerprint -sha256` show them.
func logPanelSelfSignWritten(hostname string, v panelcert.Verdict, newKey bool) {
	reason := v.Reason
	if newKey && v.Action == panelcert.UpToDate {
		reason = "new key requested"
	}
	attrs := []any{"hostname", hostname, "reason", reason, "cert", panelSelfSignCertPath}
	if data, err := os.ReadFile(panelSelfSignCertPath); err == nil {
		if d, err := panelcert.Describe(data); err == nil {
			attrs = append(attrs, "sha256", d.FingerprintSHA256, "not_after", d.NotAfter.UTC().Format(time.RFC3339))
		}
	}
	slog.Info("panel self-signed cert written, restarting the panel to serve it", attrs...)
}

// panelCertIssuerOrg returns the first issuer Organization of cert, trimmed,
// or "" when absent. Empty and "Jabali Panel" both mean "our self-signed
// bootstrap" (regenerable); anything else is a real CA cert to preserve.
func panelCertIssuerOrg(cert *x509.Certificate) string { return panelcert.IssuerOrg(cert) }

// generatePanelSelfSignedCert writes an ECDSA P-256 self-signed cert +
// PKCS#8 key to certPath/keyPath, valid 10 years, Subject CN=cn
// O="Jabali Panel", with the given DNS and IP SANs. Mirrors the openssl
// invocation in install.sh provision_tls_cert (EC prime256v1, /O=Jabali
// Panel) so the two paths produce interchangeable certs.
func generatePanelSelfSignedCert(cn string, dnsSANs []string, ipSANs []net.IP, certPath, keyPath string) (time.Time, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return time.Time{}, fmt.Errorf("generate EC key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return time.Time{}, fmt.Errorf("generate serial: %w", err)
	}
	now := time.Now()
	notAfter := now.AddDate(10, 0, 0)
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   cn,
			Organization: []string{panelSelfSignOrg},
		},
		NotBefore:             now,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              dnsSANs,
		IPAddresses:           ipSANs,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return time.Time{}, fmt.Errorf("create certificate: %w", err)
	}

	// Dir is install-provisioned in production; MkdirAll is a cheap guard so
	// a missing /etc/jabali/tls (or a test temp dir) doesn't fail the write.
	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		return time.Time{}, fmt.Errorf("ensure cert dir: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return time.Time{}, fmt.Errorf("marshal key: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := swapInPanelCertPair(certPath, certPEM, keyPath, keyPEM); err != nil {
		return time.Time{}, err
	}
	return notAfter, nil
}

// swapInPanelCertPair writes both files beside the live ones, then renames
// them into place one right after the other. Writing in place left a reader
// (an nginx reload, a panel start) a truncated file or the new cert next to
// the old key for as long as the second write took; now the old pair stays
// whole until both new files exist. The key is staged 0600: the caller sets
// its final owner and mode.
func swapInPanelCertPair(certPath string, certPEM []byte, keyPath string, keyPEM []byte) error {
	certTmp, keyTmp := certPath+".new", keyPath+".new"
	cleanup := func() { _ = os.Remove(certTmp); _ = os.Remove(keyTmp) }
	// A leftover from an interrupted run could carry a looser mode, which
	// WriteFile keeps on an existing file: start from nothing.
	cleanup()
	if err := os.WriteFile(keyTmp, keyPEM, 0o600); err != nil {
		cleanup()
		return fmt.Errorf("write key: %w", err)
	}
	if err := os.WriteFile(certTmp, certPEM, 0o644); err != nil {
		cleanup()
		return fmt.Errorf("write cert: %w", err)
	}
	if err := os.Rename(keyTmp, keyPath); err != nil {
		cleanup()
		return fmt.Errorf("install key: %w", err)
	}
	if err := os.Rename(certTmp, certPath); err != nil {
		cleanup()
		return fmt.Errorf("install cert (the new key is in place; run the command again): %w", err)
	}
	return nil
}

// applyPanelKeyOwnership sets the key to 0640 root:www-data (best-effort).
func applyPanelKeyOwnership(keyPath string) {
	_ = os.Chmod(keyPath, 0o640)
	grp, err := user.LookupGroup("www-data")
	if err != nil {
		return
	}
	gid, err := strconv.Atoi(grp.Gid)
	if err != nil {
		return
	}
	_ = os.Chown(keyPath, 0, gid)
}

func init() {
	Default.Register("ssl.panel.selfsign", sslPanelSelfSignHandler)
}
