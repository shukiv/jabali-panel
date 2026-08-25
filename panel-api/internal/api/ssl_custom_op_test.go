package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func selfSignedCertPEM(t *testing.T, dnsName string) string {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: dnsName},
		DNSNames:     []string{dnsName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	var buf bytes.Buffer
	if err := pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.String()
}

type ccAgent struct {
	calls      []string
	installErr error
}

func (a *ccAgent) Call(_ context.Context, method string, _ any) (json.RawMessage, error) {
	a.calls = append(a.calls, method)
	if method == "ssl.install_custom" {
		if a.installErr != nil {
			return nil, a.installErr
		}
		return json.RawMessage(`{"cert_path":"/etc/letsencrypt/live/d/fullchain.pem","key_path":"/etc/letsencrypt/live/d/privkey.pem"}`), nil
	}
	return json.RawMessage(`{}`), nil
}

type ccDomains struct {
	repository.DomainRepository
	failModeTimes int
	modeCalls     int
	lastMode      string
}

func (d *ccDomains) UpdateSSLMode(_ context.Context, _, mode string) error {
	d.modeCalls++
	if d.failModeTimes > 0 {
		d.failModeTimes--
		return errors.New("db timeout")
	}
	d.lastMode = mode
	return nil
}

type ccSSL struct {
	repository.SSLCertificateRepository
	createCalls int
	updateCalls int
	created     *models.SSLCertificate
}

func (s *ccSSL) FindByDomainID(_ context.Context, _ string) (*models.SSLCertificate, error) {
	return nil, repository.ErrNotFound
}
func (s *ccSSL) Create(_ context.Context, c *models.SSLCertificate) error {
	s.createCalls++
	s.created = c
	return nil
}
func (s *ccSSL) UpdateCustom(_ context.Context, _, _, _ string, _ time.Time) error {
	s.updateCalls++
	return nil
}

type ccScheduler struct{ scheduled []string }

func (s *ccScheduler) Schedule(id string) { s.scheduled = append(s.scheduled, id) }

func ccDeps(ag *ccAgent, dom *ccDomains, ssl *ccSSL) CustomCertDeps {
	return CustomCertDeps{Agent: ag, Domains: dom, SSLCerts: ssl}
}

func TestInstallCustomDomainCert_HappyPath(t *testing.T) {
	certPEM := selfSignedCertPEM(t, "site.example.com")
	ag, dom, ssl, sch := &ccAgent{}, &ccDomains{}, &ccSSL{}, &ccScheduler{}

	res, err := InstallCustomDomainCert(context.Background(), ccDeps(ag, dom, ssl),
		&models.Domain{ID: "d1", Name: "site.example.com"}, certPEM, "KEY", sch)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if res == nil || res.CertPath == "" {
		t.Fatal("nil/empty result")
	}
	if len(ag.calls) != 1 || ag.calls[0] != "ssl.install_custom" {
		t.Fatalf("agent calls = %v", ag.calls)
	}
	if dom.modeCalls != 1 || dom.lastMode != models.SSLModeCustom {
		t.Errorf("mode not set once to custom: calls=%d last=%q", dom.modeCalls, dom.lastMode)
	}
	if ssl.createCalls != 1 {
		t.Errorf("cert row not created once: %d", ssl.createCalls)
	}
	if len(sch.scheduled) != 1 || sch.scheduled[0] != "d1" {
		t.Errorf("convergence not scheduled once: %v", sch.scheduled)
	}
}

// A transient finalize failure (dead request ctx) must be recovered on a fresh
// context: mode-write fails once, the retry succeeds, the install still reports
// success.
func TestInstallCustomDomainCert_FinalizeRetrySucceeds(t *testing.T) {
	certPEM := selfSignedCertPEM(t, "site.example.com")
	ag, dom, ssl := &ccAgent{}, &ccDomains{failModeTimes: 1}, &ccSSL{}

	res, err := InstallCustomDomainCert(context.Background(), ccDeps(ag, dom, ssl),
		&models.Domain{ID: "d1", Name: "site.example.com"}, certPEM, "KEY", nil)
	if err != nil {
		t.Fatalf("a single transient finalize failure must be retried, got %v", err)
	}
	if res == nil {
		t.Fatal("nil result after successful retry")
	}
	if dom.modeCalls != 2 {
		t.Errorf("expected one failed + one successful mode write, got %d", dom.modeCalls)
	}
	if ssl.createCalls != 1 {
		t.Errorf("cert row must persist on the successful attempt, got %d", ssl.createCalls)
	}
}

// If finalize never succeeds the cert is already live on the host, so the op
// reports the distinct ErrCustomCertFinalize (naming idempotent recovery) — and
// must NEVER try to remove the just-installed files: the only agent call is the
// install itself.
func TestInstallCustomDomainCert_FinalizeExhausted_NeverRemoves(t *testing.T) {
	certPEM := selfSignedCertPEM(t, "site.example.com")
	ag, dom, ssl := &ccAgent{}, &ccDomains{failModeTimes: 99}, &ccSSL{}

	res, err := InstallCustomDomainCert(context.Background(), ccDeps(ag, dom, ssl),
		&models.Domain{ID: "d1", Name: "site.example.com"}, certPEM, "KEY", nil)
	if !errors.Is(err, ErrCustomCertFinalize) {
		t.Fatalf("expected ErrCustomCertFinalize, got %v", err)
	}
	if res != nil {
		t.Error("no result on finalize failure")
	}
	if len(ag.calls) != 1 || ag.calls[0] != "ssl.install_custom" {
		t.Fatalf("never-remove invariant: the only agent call must be the install, got %v", ag.calls)
	}
	if dom.modeCalls != customCertFinalizeRetries {
		t.Errorf("finalize should try exactly %d times, got %d", customCertFinalizeRetries, dom.modeCalls)
	}
}

func TestInstallCustomDomainCert_NilScheduler_NoPanic(t *testing.T) {
	certPEM := selfSignedCertPEM(t, "site.example.com")
	if _, err := InstallCustomDomainCert(context.Background(), ccDeps(&ccAgent{}, &ccDomains{}, &ccSSL{}),
		&models.Domain{ID: "d1", Name: "site.example.com"}, certPEM, "KEY", nil); err != nil {
		t.Fatalf("nil scheduler must be a no-op, got %v", err)
	}
}

// A cert that does not cover the domain is rejected BEFORE the agent is touched.
func TestInstallCustomDomainCert_HostnameMismatch_NoAgentCall(t *testing.T) {
	certPEM := selfSignedCertPEM(t, "other.example.com")
	ag := &ccAgent{}
	_, err := InstallCustomDomainCert(context.Background(), ccDeps(ag, &ccDomains{}, &ccSSL{}),
		&models.Domain{ID: "d1", Name: "site.example.com"}, certPEM, "KEY", nil)
	if !errors.Is(err, ErrCustomCertMismatch) {
		t.Fatalf("expected ErrCustomCertMismatch, got %v", err)
	}
	if len(ag.calls) != 0 {
		t.Errorf("a mismatched cert must never reach the agent, got %v", ag.calls)
	}
}

func TestInstallCustomDomainCert_InvalidCert_NoAgentCall(t *testing.T) {
	ag := &ccAgent{}
	_, err := InstallCustomDomainCert(context.Background(), ccDeps(ag, &ccDomains{}, &ccSSL{}),
		&models.Domain{ID: "d1", Name: "site.example.com"}, "not a pem", "KEY", nil)
	if !errors.Is(err, ErrCustomCertInvalid) {
		t.Fatalf("expected ErrCustomCertInvalid, got %v", err)
	}
	if len(ag.calls) != 0 {
		t.Errorf("an unparseable cert must never reach the agent, got %v", ag.calls)
	}
}
