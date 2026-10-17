package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/services"
)

// memPanelCerts is an in-memory PanelCertificateRepository keyed by kind.
type memPanelCerts struct {
	mu          sync.Mutex
	rows        map[string]*models.PanelCertificate
	pendingKind []string
}

func (m *memPanelCerts) get(kind string) *models.PanelCertificate {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *m.rows[kind]
	return &cp
}
func (m *memPanelCerts) Get(ctx context.Context) (*models.PanelCertificate, error) {
	return m.GetByKind(ctx, models.PanelCertKindHostname)
}
func (m *memPanelCerts) GetByKind(_ context.Context, kind string) (*models.PanelCertificate, error) {
	return m.get(kind), nil
}
func (m *memPanelCerts) ListAll(context.Context) ([]*models.PanelCertificate, error) {
	return []*models.PanelCertificate{m.get(models.PanelCertKindHostname), m.get(models.PanelCertKindMail)}, nil
}
func (m *memPanelCerts) EnsureDefault(context.Context, string) (*models.PanelCertificate, error) {
	return m.get(models.PanelCertKindHostname), nil
}
func (m *memPanelCerts) Upsert(_ context.Context, c *models.PanelCertificate) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *c
	m.rows[c.Kind] = &cp
	return nil
}
func (m *memPanelCerts) MarkIssued(context.Context, time.Time, time.Time) error        { return nil }
func (m *memPanelCerts) MarkPendingRetry(context.Context, string, time.Duration) error { return nil }
func (m *memPanelCerts) MarkIssuedKind(context.Context, string, time.Time, time.Time) error {
	return nil
}
func (m *memPanelCerts) MarkPendingRetryKind(_ context.Context, kind, errMsg string, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pendingKind = append(m.pendingKind, kind)
	r := m.rows[kind]
	r.Status, r.LastError = models.PanelCertStatusPendingACMERetry, errMsg
	return nil
}

// callsAgent records every agent call and signals it on calls.
type callsAgent struct {
	mu      sync.Mutex
	methods []string
	params  []map[string]any
	calls   chan string
}

func (a *callsAgent) Call(_ context.Context, method string, p any) (json.RawMessage, error) {
	a.mu.Lock()
	a.methods = append(a.methods, method)
	m, _ := p.(map[string]any)
	a.params = append(a.params, m)
	a.mu.Unlock()
	a.calls <- method
	return json.RawMessage(`{"regenerated":true}`), nil
}

// countingLookup answers every lookup with a Cloudflare address, as for the
// reporter's proxied hostname, and counts the lookups.
type countingLookup struct {
	mu sync.Mutex
	n  int
}

func (l *countingLookup) LookupHost(context.Context, string) ([]string, error) {
	l.mu.Lock()
	l.n++
	l.mu.Unlock()
	return []string{"104.21.0.1"}, nil
}
func (l *countingLookup) public(context.Context, string) ([]string, bool) {
	l.mu.Lock()
	l.n++
	l.mu.Unlock()
	return []string{"104.21.0.1"}, true
}
func (l *countingLookup) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.n
}

func writePanelCertPEM(t *testing.T, org, cn string, dnsNames []string, notAfter time.Time) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn, Organization: []string{org}},
		NotBefore:    notAfter.AddDate(-1, 0, 0),
		NotAfter:     notAfter,
		DNSNames:     dnsNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "panel.crt")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type panelCertOpts struct {
	useLE        bool
	certPath     string // "" = a path with no cert
	noAgent      bool
	noAdminEmail bool
}

type panelCertFixture struct {
	certs  *memPanelCerts
	agent  *callsAgent
	lookup *countingLookup
	router *gin.Engine
}

// newPanelCertFixture: panel.example.com behind a proxy (not routable). The
// hostname row starts where the reporter's was: a pending Let's Encrypt
// retry with "not routable".
func newPanelCertFixture(t *testing.T, o panelCertOpts) *panelCertFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	if o.certPath == "" {
		o.certPath = filepath.Join(t.TempDir(), "panel.crt")
	}
	retry := time.Now().Add(time.Hour)
	f := &panelCertFixture{
		certs: &memPanelCerts{rows: map[string]*models.PanelCertificate{
			models.PanelCertKindHostname: {
				Kind: models.PanelCertKindHostname, Hostname: "panel.example.com", CertPEMPath: o.certPath,
				UseLE: o.useLE, Status: models.PanelCertStatusPendingACMERetry,
				LastError: "not routable: dns points elsewhere", AttemptCount: 3, NextRetryAt: &retry,
			},
			models.PanelCertKindMail: {
				Kind: models.PanelCertKindMail, Hostname: "mail.panel.example.com",
				CertPEMPath: "/etc/jabali/tls/panel-mail.crt", Status: models.PanelCertStatusSelfSigned,
			},
		}},
		agent:  &callsAgent{calls: make(chan string, 4)},
		lookup: &countingLookup{},
	}
	settings := &models.ServerSettings{Hostname: "panel.example.com", AdminEmail: "admin@example.com", PublicIPv4: "203.0.113.10"}
	if o.noAdminEmail {
		settings.AdminEmail = ""
	}
	cfg := AdminPanelCertificateHandlerConfig{
		PanelCerts:     f.certs,
		ServerSettings: &fakeSettingsRepo{s: settings},
		Routability:    &services.PanelCertRoutability{Resolver: f.lookup, PublicLookup: f.lookup.public},
	}
	if !o.noAgent {
		cfg.Agent = f.agent
	}
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "a1", IsAdmin: true})
		c.Next()
	})
	RegisterAdminPanelCertificateRoutes(v1, cfg)
	f.router = r
	return f
}

func (f *panelCertFixture) issue(t *testing.T, kind string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/admin/panel-certificate/issue?kind="+kind, nil))
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec, body
}

// waitCall returns the next agent method called within d, or "".
func (f *panelCertFixture) waitCall(d time.Duration) string {
	select {
	case m := <-f.agent.calls:
		return m
	case <-time.After(d):
		return ""
	}
}

func detail(body map[string]any) string {
	s, _ := body["detail"].(string)
	return s
}

var panelCertBothNames = []string{"panel.example.com", "mail.panel.example.com", "localhost"}

// GH #2166: with Let's Encrypt off, the hostname's Issue action regenerates
// the self-signed cert. No routability check, no Let's Encrypt, no retry.
func TestPanelCertIssue_LEOffRegeneratesSelfSigned(t *testing.T) {
	f := newPanelCertFixture(t, panelCertOpts{})

	rec, body := f.issue(t, "hostname")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d %s", rec.Code, rec.Body.String())
	}
	if body["regenerating"] != true || !strings.Contains(detail(body), "Regenerating the self-signed certificate for panel.example.com") {
		t.Errorf("body = %v", body)
	}
	if m := f.waitCall(2 * time.Second); m != "ssl.panel.selfsign" {
		t.Fatalf("agent call = %q, want ssl.panel.selfsign", m)
	}
	f.agent.mu.Lock()
	p := f.agent.params[0]
	f.agent.mu.Unlock()
	if p["hostname"] != "panel.example.com" || p["ip"] != "203.0.113.10" {
		t.Errorf("selfsign params = %v", p)
	}
	if n := f.lookup.count(); n != 0 {
		t.Errorf("routability consulted %d times; self-signed needs no public DNS", n)
	}
	if len(f.certs.pendingKind) != 0 {
		t.Errorf("marked a Let's Encrypt retry: %v", f.certs.pendingKind)
	}
	row := f.certs.get("hostname")
	if row.Status != models.PanelCertStatusSelfSigned || row.LastError != "" || row.NextRetryAt != nil || row.AttemptCount != 0 {
		t.Errorf("hostname row not reset to self-signed: %+v", row)
	}
}

// A self-signed cert that already covers the hostname is left alone, and the
// stale Let's Encrypt failure on the row is cleared.
func TestPanelCertIssue_LEOffAlreadySelfSigned(t *testing.T) {
	path := writePanelCertPEM(t, "Jabali Panel", "panel.example.com", panelCertBothNames, time.Now().AddDate(5, 0, 0))
	f := newPanelCertFixture(t, panelCertOpts{certPath: path})

	rec, body := f.issue(t, "hostname")
	if rec.Code != http.StatusOK || body["regenerating"] != false {
		t.Fatalf("want 200 regenerating=false, got %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(detail(body), "already serves a self-signed certificate for panel.example.com") {
		t.Errorf("detail = %q", detail(body))
	}
	if body["expires_at"] == nil || body["expires_at"] == "" {
		t.Errorf("no expires_at for the UI: %v", body)
	}
	if m := f.waitCall(100 * time.Millisecond); m != "" {
		t.Errorf("agent called (%s) for an up-to-date cert", m)
	}
	row := f.certs.get("hostname")
	if row.Status != models.PanelCertStatusSelfSigned || row.LastError != "" || row.NextRetryAt != nil {
		t.Errorf("stale retry not cleared: %+v", row)
	}
}

// A live Let's Encrypt cert is kept until it expires.
func TestPanelCertIssue_LEOffKeepsLiveLECert(t *testing.T) {
	path := writePanelCertPEM(t, "Let's Encrypt", "panel.example.com", []string{"panel.example.com"}, time.Now().AddDate(0, 2, 0))
	f := newPanelCertFixture(t, panelCertOpts{certPath: path})

	rec, body := f.issue(t, "hostname")
	if rec.Code != http.StatusOK || body["regenerating"] != false {
		t.Fatalf("want 200 regenerating=false, got %d %s", rec.Code, rec.Body.String())
	}
	if d := detail(body); !strings.Contains(d, "issued by Let's Encrypt") || !strings.Contains(d, "until it expires") {
		t.Errorf("detail = %q", d)
	}
	if m := f.waitCall(100 * time.Millisecond); m != "" {
		t.Errorf("agent called (%s) over a live Let's Encrypt cert", m)
	}
	if body["expires_at"] == nil || body["expires_at"] == "" {
		t.Errorf("no expires_at for the UI: %v", body)
	}
	if row := f.certs.get("hostname"); row.Status != models.PanelCertStatusIssued || row.LastError != "" || row.ExpiresAt == nil {
		t.Errorf("row = %+v, want issued with its expiry and no error", row)
	}
}

// An expired Let's Encrypt cert is replaced.
func TestPanelCertIssue_LEOffReplacesExpiredLECert(t *testing.T) {
	path := writePanelCertPEM(t, "Let's Encrypt", "panel.example.com", []string{"panel.example.com"}, time.Now().Add(-time.Hour))
	f := newPanelCertFixture(t, panelCertOpts{certPath: path})

	if rec, _ := f.issue(t, "hostname"); rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d %s", rec.Code, rec.Body.String())
	}
	if m := f.waitCall(2 * time.Second); m != "ssl.panel.selfsign" {
		t.Fatalf("agent call = %q, want ssl.panel.selfsign", m)
	}
}

// Self-signed needs no admin email; that is for Let's Encrypt registration.
func TestPanelCertIssue_LEOffNeedsNoAdminEmail(t *testing.T) {
	f := newPanelCertFixture(t, panelCertOpts{noAdminEmail: true})
	if rec, _ := f.issue(t, "hostname"); rec.Code != http.StatusAccepted {
		t.Fatalf("want 202 without an admin email, got %d %s", rec.Code, rec.Body.String())
	}
	f.waitCall(2 * time.Second)
}

func TestPanelCertIssue_LEOffNoAgent(t *testing.T) {
	f := newPanelCertFixture(t, panelCertOpts{noAgent: true})
	if rec, _ := f.issue(t, "hostname"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d %s", rec.Code, rec.Body.String())
	}
}

// Unchanged: the mail cert stays a Let's Encrypt action with the panel's
// Let's Encrypt off (mail.<hostname> can't sit behind an HTTP proxy).
func TestPanelCertIssue_LEOffMailStillUsesLetsEncrypt(t *testing.T) {
	f := newPanelCertFixture(t, panelCertOpts{})
	rec, _ := f.issue(t, "mail")
	if rec.Code != http.StatusFailedDependency {
		t.Fatalf("want 424 not routable, got %d %s", rec.Code, rec.Body.String())
	}
	if f.lookup.count() == 0 || len(f.certs.pendingKind) != 1 || f.certs.pendingKind[0] != "mail" {
		t.Errorf("mail issue skipped the Let's Encrypt path: lookups=%d pending=%v", f.lookup.count(), f.certs.pendingKind)
	}
}

// Unchanged: with Let's Encrypt on, the hostname issue checks routability.
func TestPanelCertIssue_LEOnStillChecksRoutability(t *testing.T) {
	f := newPanelCertFixture(t, panelCertOpts{useLE: true})
	rec, _ := f.issue(t, "hostname")
	if rec.Code != http.StatusFailedDependency {
		t.Fatalf("want 424 not routable, got %d %s", rec.Code, rec.Body.String())
	}
	if m := f.waitCall(100 * time.Millisecond); m != "" {
		t.Errorf("agent called (%s) for an unroutable Let's Encrypt issue", m)
	}
}
