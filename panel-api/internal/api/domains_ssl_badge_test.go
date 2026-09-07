package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// mockSSLCertsForBadge returns canned certs keyed by domain ID. Only
// FindByDomainIDs is exercised; the rest are stubs.
type mockSSLCertsForBadge struct {
	byDomainID map[string]*models.SSLCertificate
}

func (m *mockSSLCertsForBadge) FindByDomainIDs(_ context.Context, ids []string) ([]models.SSLCertificate, error) {
	out := make([]models.SSLCertificate, 0, len(ids))
	for _, id := range ids {
		if c, ok := m.byDomainID[id]; ok && c != nil {
			out = append(out, *c)
		}
	}
	return out, nil
}

// Stubs to satisfy SSLCertificateRepository.
func (m *mockSSLCertsForBadge) SetIssueMethod(context.Context, string, string) error { return nil }

func (m *mockSSLCertsForBadge) Create(context.Context, *models.SSLCertificate) error {
	return nil
}
func (m *mockSSLCertsForBadge) FindByDomainID(context.Context, string) (*models.SSLCertificate, error) {
	return nil, nil
}
func (m *mockSSLCertsForBadge) UpdateStatus(context.Context, string, string, *string) error {
	return nil
}
func (m *mockSSLCertsForBadge) UpdateAfterIssuance(context.Context, string, time.Time, time.Time, string, string) error {
	return nil
}
func (m *mockSSLCertsForBadge) UpdateAfterRenewal(context.Context, string, time.Time, time.Time, string, string) error {
	return nil
}
func (m *mockSSLCertsForBadge) MarkRevoked(context.Context, string) error { return nil }
func (m *mockSSLCertsForBadge) DeleteByDomainID(context.Context, string) error {
	return nil
}
func (m *mockSSLCertsForBadge) ListAll(context.Context) ([]repository.SSLCertificateWithDomain, error) {
	return nil, nil
}
func (m *mockSSLCertsForBadge) ListByUserID(context.Context, string) ([]repository.SSLCertificateWithDomain, error) {
	return nil, nil
}
func (m *mockSSLCertsForBadge) UpdateSelfSigned(context.Context, string, string, string, time.Time) error {
	return nil
}

func (m *mockSSLCertsForBadge) UpdateCustom(context.Context, string, string, string, time.Time) error {
	return nil
}
func (m *mockSSLCertsForBadge) UpdateAfterACMEFailure(context.Context, string, string, time.Time, int, *string, *string, *time.Time) error {
	return nil
}
func (m *mockSSLCertsForBadge) UpdateAfterACMEFailureCapped(context.Context, string, string, int, *string, *string, *time.Time) error {
	return nil
}
func (m *mockSSLCertsForBadge) MarkFailed(context.Context, string, string) error { return nil }
func (m *mockSSLCertsForBadge) ListDueForACMERetry(context.Context, time.Time, int) ([]models.SSLCertificate, error) {
	return nil, nil
}

// domainListWithSeedData wraps mockDomainRepo (whose List returns a stub)
// with a real List implementation that returns the seeded domains map.
type domainListWithSeedData struct {
	*mockDomainRepo
}

func (r *domainListWithSeedData) List(_ context.Context, _ repository.ListOptions) ([]models.Domain, int64, error) {
	out := make([]models.Domain, 0, len(r.domains))
	for _, d := range r.domains {
		out = append(out, *d)
	}
	return out, int64(len(out)), nil
}

// TestDomainList_EmbedsSSLBadge asserts that GET /domains enriches each row
// with a nested `ssl` object keyed off the cert table. Covers the three
// shipping labels: Let's Encrypt (issued), Self-signed, and Off (no cert).
func TestDomainList_EmbedsSSLBadge(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", IsAdmin: true})
		c.Next()
	})

	now := time.Now()
	base := newMockDomainRepo()
	base.Create(context.Background(), &models.Domain{ID: "d1", UserID: "u1", Name: "a.com"})
	base.Create(context.Background(), &models.Domain{ID: "d2", UserID: "u1", Name: "b.com"})
	base.Create(context.Background(), &models.Domain{ID: "d3", UserID: "u1", Name: "c.com"})
	domains := &domainListWithSeedData{mockDomainRepo: base}

	certs := &mockSSLCertsForBadge{byDomainID: map[string]*models.SSLCertificate{
		"d1": {ID: "c1", DomainID: "d1", Status: models.SSLStatusIssued, IssuedAt: &now},
		"d2": {ID: "c2", DomainID: "d2", Status: models.SSLStatusSelfSigned, IssuedAt: &now},
		// d3 has no cert → "Off" on the UI side.
	}}

	RegisterDomainRoutes(v1, DomainHandlerConfig{Domains: domains, SSLCerts: certs})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/domains", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []struct {
			ID  string `json:"id"`
			SSL *struct {
				Status string  `json:"status"`
				Issuer *string `json:"issuer"`
			} `json:"ssl"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Data) != 3 {
		t.Fatalf("rows: got %d want 3", len(resp.Data))
	}

	byID := map[string]struct {
		status string
		issuer string
	}{}
	for _, row := range resp.Data {
		if row.SSL == nil {
			byID[row.ID] = struct{ status, issuer string }{status: "", issuer: ""}
		} else {
			issuer := ""
			if row.SSL.Issuer != nil {
				issuer = *row.SSL.Issuer
			}
			byID[row.ID] = struct{ status, issuer string }{status: row.SSL.Status, issuer: issuer}
		}
	}

	if byID["d1"].status != "issued" || byID["d1"].issuer != "Let's Encrypt" {
		t.Errorf("d1 (issued): got %+v, want status=issued issuer=Let's Encrypt", byID["d1"])
	}
	if byID["d2"].status != "self_signed" || byID["d2"].issuer != "Self-signed" {
		t.Errorf("d2 (self_signed): got %+v, want status=self_signed issuer=Self-signed", byID["d2"])
	}
	if byID["d3"].status != "" {
		t.Errorf("d3 (no cert): got %+v, want no ssl", byID["d3"])
	}
}

// TestDomainGet_PopulatesSSLState is the GH #1543 fix: the single-domain detail
// endpoint (GET /domains/:id, which feeds the Web Domain Overview) must return a
// flat ssl_state that matches the list/SSL page. FindByID returns the raw row
// with an empty ssl_state (omitempty → absent → the Overview showed "Off" even
// with a live cert); enrichDomainResponse now fills it from the cert it fetches.
func TestDomainGet_PopulatesSSLState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	lePath := "/etc/letsencrypt/live/x/fullchain.pem"
	now := time.Now()

	cases := []struct {
		name     string
		mode     string
		cert     *models.SSLCertificate
		wantSSL  string // expected flat ssl_state on the detail response
		wantHTTP int
	}{
		{"active LE cert", models.SSLModeLE, &models.SSLCertificate{ID: "c", DomainID: "d1", Status: models.SSLStatusIssued, CertPath: &lePath, IssuedAt: &now}, "active_le", 200},
		{"issued non-LE cert", models.SSLModeLE, &models.SSLCertificate{ID: "c", DomainID: "d1", Status: models.SSLStatusIssued, IssuedAt: &now}, "self_signed", 200},
		{"LE mode, no cert yet", models.SSLModeLE, nil, "pending", 200},
		{"None mode ignores cert", models.SSLModeNone, &models.SSLCertificate{ID: "c", DomainID: "d1", Status: models.SSLStatusIssued, CertPath: &lePath, IssuedAt: &now}, "off", 200},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			v1 := r.Group("/api/v1")
			v1.Use(func(c *gin.Context) {
				ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", IsAdmin: true})
				c.Next()
			})
			base := newMockDomainRepo()
			base.Create(context.Background(), &models.Domain{ID: "d1", UserID: "u1", Name: "x.com", SSLMode: tc.mode, SSLEnabled: true})
			certMap := map[string]*models.SSLCertificate{}
			if tc.cert != nil {
				certMap["d1"] = tc.cert
			}
			RegisterDomainRoutes(v1, DomainHandlerConfig{Domains: base, SSLCerts: &mockSSLCertsForBadge{byDomainID: certMap}})

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/domains/d1", nil))
			if w.Code != tc.wantHTTP {
				t.Fatalf("status: got %d want %d, body=%s", w.Code, tc.wantHTTP, w.Body.String())
			}
			var resp struct {
				SSLState string `json:"ssl_state"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if resp.SSLState != tc.wantSSL {
				t.Errorf("ssl_state: got %q want %q (body=%s)", resp.SSLState, tc.wantSSL, w.Body.String())
			}
		})
	}
}

// GH #246 follow-up: the badge must reflect ssl_mode, not just the cert row —
// a None-mode domain keeps a revoked cert; a Self/None domain with no usable
// cert must not read as "pending".
func TestSSLBadgeForDomain_ModeAware(t *testing.T) {
	dom := func(mode string) *models.Domain { return &models.Domain{ID: "d", Name: "x.com", SSLMode: mode} }
	revoked := &models.SSLCertificate{Status: models.SSLStatusRevoked}
	issued := &models.SSLCertificate{Status: models.SSLStatusIssued}
	self := &models.SSLCertificate{Status: models.SSLStatusSelfSigned}

	cases := []struct {
		name       string
		d          *models.Domain
		cert       *models.SSLCertificate
		wantNil    bool
		wantStatus string
	}{
		{"none+revoked", dom(models.SSLModeNone), revoked, false, "none"},
		{"none+nocert", dom(models.SSLModeNone), nil, false, "none"},
		{"none+issued", dom(models.SSLModeNone), issued, false, "none"}, // mode wins
		{"self+nocert", dom(models.SSLModeSelf), nil, false, "provisioning"},
		{"self+revoked", dom(models.SSLModeSelf), revoked, false, "provisioning"},
		{"self+selfsigned", dom(models.SSLModeSelf), self, false, "self_signed"},
		{"le+nocert", dom(models.SSLModeLE), nil, true, ""},
		{"le+revoked", dom(models.SSLModeLE), revoked, true, ""},
		{"le+issued", dom(models.SSLModeLE), issued, false, "issued"},
	}
	for _, tc := range cases {
		b := sslBadgeForDomain(tc.d, tc.cert)
		if tc.wantNil {
			if b != nil {
				t.Errorf("%s: want nil badge, got %+v", tc.name, b)
			}
			continue
		}
		if b == nil || b.Status != tc.wantStatus {
			t.Errorf("%s: want status %q, got %+v", tc.name, tc.wantStatus, b)
		}
	}
}

// RefreshObservedExpiry — JAB-203 observation pass; no behaviour needed here.
func (m *mockSSLCertsForBadge) RefreshObservedExpiry(context.Context, string, time.Time, time.Time) error {
	return nil
}

// JAB-224 ssl_resurrect — unused by these tests.
func (m *mockSSLCertsForBadge) ListExhaustedForSSLEnabledDomains(context.Context, time.Time, int) ([]models.SSLCertificate, error) {
	return nil, nil
}

func (m *mockSSLCertsForBadge) RearmACME(context.Context, string, int, time.Time) error { return nil }
func (m *mockSSLCertsForBadge) ResetForRetry(context.Context, string, time.Time) error  { return nil }
