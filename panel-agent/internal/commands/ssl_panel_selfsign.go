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
	"math/big"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
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
//   - PRESERVE any cert whose issuer Organization is neither empty nor
//     panelSelfSignOrg ("Jabali Panel"): that is a deployed Let's Encrypt
//     or custom cert. The 2026-05-09 regression clobbered a live LE cert
//     with a self-signed one; the reconciler's drift check applies the same
//     guard, and this handler re-checks so a stale reconciler dispatch can
//     never destroy a real CA cert.
//   - No-churn: when the existing self-signed cert already covers the FQDN
//     (CN + the hostname and mail.<hostname> SANs) and is unexpired, do
//     nothing — no regenerate, no nginx reload, no panel restart.
//   - SAN set identical to install.sh:
//     [hostname, mail.hostname, localhost, 127.0.0.1, <ip>].
type sslPanelSelfSignParams struct {
	Hostname string `json:"hostname"`
	IP       string `json:"ip,omitempty"`
}

type sslPanelSelfSignResponse struct {
	Regenerated bool   `json:"regenerated"`
	Reason      string `json:"reason,omitempty"`
	CertPath    string `json:"cert_path"`
	ExpiresAt   string `json:"expires_at,omitempty"`
}

// Overridable in tests.
var (
	panelSelfSignCertPath = "/etc/jabali/tls/panel.crt"
	panelSelfSignKeyPath  = "/etc/jabali/tls/panel.key"
	// panelSelfSignReloadFn applies the freshly written cert to the running
	// services. Seam so unit tests don't spawn systemctl.
	panelSelfSignReloadFn = defaultPanelSelfSignReload
)

// panelSelfSignOrg is the Subject/Issuer Organization stamped onto our
// self-signed panel cert (matches install.sh's -subj "…/O=Jabali Panel").
// The preserve-guard keys off this exact string, so it MUST stay in sync
// with install.sh provision_tls_cert.
const panelSelfSignOrg = "Jabali Panel"

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

	// Inspect the current cert: preserve a real CA cert; skip a self-signed
	// cert that already covers the FQDN and is unexpired.
	if data, err := os.ReadFile(panelSelfSignCertPath); err == nil {
		if block, _ := pem.Decode(data); block != nil {
			if cert, perr := x509.ParseCertificate(block.Bytes); perr == nil {
				if o := panelCertIssuerOrg(cert); o != "" && o != panelSelfSignOrg {
					return sslPanelSelfSignResponse{
						Regenerated: false,
						Reason:      "preserved: cert issued by " + o,
						CertPath:    panelSelfSignCertPath,
					}, nil
				}
				if panelSelfSignedCertCovers(cert, p.Hostname) && cert.NotAfter.After(time.Now()) {
					return sslPanelSelfSignResponse{
						Regenerated: false,
						Reason:      "up to date for " + p.Hostname,
						CertPath:    panelSelfSignCertPath,
						ExpiresAt:   cert.NotAfter.UTC().Format(time.RFC3339),
					}, nil
				}
			}
		}
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

	panelSelfSignReloadFn(ctx)

	return sslPanelSelfSignResponse{
		Regenerated: true,
		CertPath:    panelSelfSignCertPath,
		ExpiresAt:   expiresAt.UTC().Format(time.RFC3339),
	}, nil
}

// panelCertIssuerOrg returns the first issuer Organization of cert, trimmed,
// or "" when absent. Empty and "Jabali Panel" both mean "our self-signed
// bootstrap" (regenerable); anything else is a real CA cert to preserve.
func panelCertIssuerOrg(cert *x509.Certificate) string {
	if len(cert.Issuer.Organization) > 0 {
		return strings.TrimSpace(cert.Issuer.Organization[0])
	}
	return ""
}

// panelSelfSignedCertCovers reports whether cert already presents the panel
// FQDN: CN == hostname AND the hostname + mail.<hostname> SANs are present.
func panelSelfSignedCertCovers(cert *x509.Certificate, hostname string) bool {
	if !strings.EqualFold(cert.Subject.CommonName, hostname) {
		return false
	}
	have := make(map[string]struct{}, len(cert.DNSNames))
	for _, d := range cert.DNSNames {
		have[strings.ToLower(d)] = struct{}{}
	}
	for _, need := range []string{hostname, "mail." + hostname} {
		if _, ok := have[strings.ToLower(need)]; !ok {
			return false
		}
	}
	return true
}

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
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return time.Time{}, fmt.Errorf("write cert: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return time.Time{}, fmt.Errorf("marshal key: %w", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return time.Time{}, fmt.Errorf("write key: %w", err)
	}
	return notAfter, nil
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
