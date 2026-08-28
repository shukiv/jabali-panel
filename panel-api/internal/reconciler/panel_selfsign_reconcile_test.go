package reconciler

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"os"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func selfSignTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// makeSelfSignPEM builds a PEM cert with the given issuer/subject Org, CN and
// DNS SANs — a stand-in for a cert on disk. org=="" → empty issuer Org.
func makeSelfSignPEM(t *testing.T, org, cn string, dnsSANs []string, notAfter time.Time) []byte {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	subj := pkix.Name{CommonName: cn}
	if org != "" {
		subj.Organization = []string{org}
	}
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               subj,
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              dnsSANs,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func newSelfSignReconciler(agent *fakeAgent, certPEM []byte, readErr error) *Reconciler {
	return &Reconciler{
		agent: agent,
		log:   selfSignTestLogger(),
		readCertFile: func(string) ([]byte, error) {
			if readErr != nil {
				return nil, readErr
			}
			return certPEM, nil
		},
	}
}

func countCalls(fa *fakeAgent, method string) (int, map[string]any) {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	var n int
	var last map[string]any
	for _, c := range fa.calls {
		if c.method == method {
			n++
			if m, ok := c.params.(map[string]any); ok {
				last = m
			}
		}
	}
	return n, last
}

func TestPanelSelfSignedDrift_CNMismatchDispatches(t *testing.T) {
	pem := makeSelfSignPEM(t, panelSelfSignOrgMarker, "old.example.com",
		[]string{"old.example.com", "mail.old.example.com", "localhost"}, time.Now().AddDate(5, 0, 0))
	fa := &fakeAgent{}
	r := newSelfSignReconciler(fa, pem, nil)
	row := &models.PanelCertificate{CertPEMPath: "/etc/jabali/tls/panel.crt"}

	if drift, _ := r.panelSelfSignedCertDrifted(row.CertPEMPath, "new.example.com"); !drift {
		t.Fatalf("expected drift for CN mismatch")
	}
	r.reconcilePanelSelfSignedCert(context.Background(), row, "new.example.com", "203.0.113.9")

	n, params := countCalls(fa, "ssl.panel.selfsign")
	if n != 1 {
		t.Fatalf("expected 1 ssl.panel.selfsign dispatch, got %d", n)
	}
	if params["hostname"] != "new.example.com" || params["ip"] != "203.0.113.9" {
		t.Fatalf("dispatch params wrong: %v", params)
	}
}

func TestPanelSelfSignedDrift_MissingSANDispatches(t *testing.T) {
	// CN matches but the mail.<hostname> SAN is absent (pre-M6.4 style cert).
	pem := makeSelfSignPEM(t, panelSelfSignOrgMarker, "host.example.com",
		[]string{"host.example.com", "localhost"}, time.Now().AddDate(5, 0, 0))
	fa := &fakeAgent{}
	r := newSelfSignReconciler(fa, pem, nil)
	if drift, reason := r.panelSelfSignedCertDrifted("/x", "host.example.com"); !drift {
		t.Fatalf("expected drift for missing mail SAN, reason=%q", reason)
	}
}

func TestPanelSelfSignedDrift_CoveredNoDispatch(t *testing.T) {
	pem := makeSelfSignPEM(t, panelSelfSignOrgMarker, "host.example.com",
		[]string{"host.example.com", "mail.host.example.com", "localhost"}, time.Now().AddDate(5, 0, 0))
	fa := &fakeAgent{}
	r := newSelfSignReconciler(fa, pem, nil)
	row := &models.PanelCertificate{CertPEMPath: "/etc/jabali/tls/panel.crt"}

	if drift, _ := r.panelSelfSignedCertDrifted(row.CertPEMPath, "host.example.com"); drift {
		t.Fatalf("did not expect drift for a covering cert")
	}
	r.reconcilePanelSelfSignedCert(context.Background(), row, "host.example.com", "")
	if n, _ := countCalls(fa, "ssl.panel.selfsign"); n != 0 {
		t.Fatalf("expected no dispatch, got %d", n)
	}
}

// LE cert (issuer O="Let's Encrypt") must be preserved even in self-signed
// mode: never reported as drift, never dispatched over.
func TestPanelSelfSignedDrift_PreservesLECert(t *testing.T) {
	pem := makeSelfSignPEM(t, "Let's Encrypt", "old.example.com",
		[]string{"old.example.com"}, time.Now().AddDate(0, 3, 0))
	fa := &fakeAgent{}
	r := newSelfSignReconciler(fa, pem, nil)
	row := &models.PanelCertificate{CertPEMPath: "/etc/jabali/tls/panel.crt"}

	if drift, _ := r.panelSelfSignedCertDrifted(row.CertPEMPath, "new.example.com"); drift {
		t.Fatalf("LE cert reported as drift — must be preserved")
	}
	r.reconcilePanelSelfSignedCert(context.Background(), row, "new.example.com", "")
	if n, _ := countCalls(fa, "ssl.panel.selfsign"); n != 0 {
		t.Fatalf("dispatched over an LE cert (%d calls) — must be preserved", n)
	}
}

func TestPanelSelfSignedDrift_MissingCertDispatches(t *testing.T) {
	fa := &fakeAgent{}
	r := newSelfSignReconciler(fa, nil, os.ErrNotExist)
	row := &models.PanelCertificate{CertPEMPath: "/etc/jabali/tls/panel.crt"}
	if drift, _ := r.panelSelfSignedCertDrifted(row.CertPEMPath, "host.example.com"); !drift {
		t.Fatalf("expected drift for missing cert")
	}
	r.reconcilePanelSelfSignedCert(context.Background(), row, "host.example.com", "")
	if n, _ := countCalls(fa, "ssl.panel.selfsign"); n != 1 {
		t.Fatalf("expected dispatch for missing cert, got %d", n)
	}
}


func TestPanelSelfSignedDrift_ExpiredCoveringCertDispatches(t *testing.T) {
	// Cert covers the FQDN but is expired → must self-heal (symmetry with the
	// agent's no-churn expiry check).
	pem := makeSelfSignPEM(t, panelSelfSignOrgMarker, "host.example.com",
		[]string{"host.example.com", "mail.host.example.com", "localhost"}, time.Now().Add(-time.Hour))
	fa := &fakeAgent{}
	r := newSelfSignReconciler(fa, pem, nil)
	row := &models.PanelCertificate{CertPEMPath: "/etc/jabali/tls/panel.crt"}
	if drift, reason := r.panelSelfSignedCertDrifted(row.CertPEMPath, "host.example.com"); !drift {
		t.Fatalf("expected drift for expired cert, reason=%q", reason)
	}
	r.reconcilePanelSelfSignedCert(context.Background(), row, "host.example.com", "")
	if n, _ := countCalls(fa, "ssl.panel.selfsign"); n != 1 {
		t.Fatalf("expected dispatch for expired cert, got %d", n)
	}
}
