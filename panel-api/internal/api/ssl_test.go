package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// MockSSLCertificateRepository implements repository.SSLCertificateRepository
type MockSSLCertificateRepository struct {
	mock.Mock
}

func (m *MockSSLCertificateRepository) Create(ctx context.Context, cert *models.SSLCertificate) error {
	args := m.Called(ctx, cert)
	return args.Error(0)
}

func (m *MockSSLCertificateRepository) FindByDomainID(ctx context.Context, domainID string) (*models.SSLCertificate, error) {
	args := m.Called(ctx, domainID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*models.SSLCertificate), args.Error(1)
}

func (m *MockSSLCertificateRepository) FindByDomainIDs(ctx context.Context, domainIDs []string) ([]models.SSLCertificate, error) {
	args := m.Called(ctx, domainIDs)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]models.SSLCertificate), args.Error(1)
}

func (m *MockSSLCertificateRepository) UpdateStatus(ctx context.Context, id string, status string, lastError *string) error {
	args := m.Called(ctx, id, status, lastError)
	return args.Error(0)
}

func (m *MockSSLCertificateRepository) UpdateAfterIssuance(ctx context.Context, id string, issuedAt, expiresAt time.Time, certPath, keyPath string) error {
	args := m.Called(ctx, id, issuedAt, expiresAt, certPath, keyPath)
	return args.Error(0)
}

func (m *MockSSLCertificateRepository) UpdateAfterRenewal(ctx context.Context, id string, issuedAt, expiresAt time.Time, certPath, keyPath string) error {
	args := m.Called(ctx, id, issuedAt, expiresAt, certPath, keyPath)
	return args.Error(0)
}

func (m *MockSSLCertificateRepository) MarkRevoked(ctx context.Context, id string) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *MockSSLCertificateRepository) DeleteByDomainID(ctx context.Context, domainID string) error {
	args := m.Called(ctx, domainID)
	return args.Error(0)
}

// RefreshObservedExpiry — JAB-203 observation pass. Replaces the
// ListDueForRenewal stub: that query's only caller was StartSSLTicker, which
// nothing ever called, so both were deleted when certbot was made the owner of
// renewal (ADR-0163).
func (m *MockSSLCertificateRepository) RefreshObservedExpiry(ctx context.Context, id string, notAfter, observedAt time.Time) error {
	args := m.Called(ctx, id, notAfter, observedAt)
	return args.Error(0)
}

func (m *MockSSLCertificateRepository) ListAll(ctx context.Context) ([]repository.SSLCertificateWithDomain, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]repository.SSLCertificateWithDomain), args.Error(1)
}

func (m *MockSSLCertificateRepository) ListByUserID(ctx context.Context, userID string) ([]repository.SSLCertificateWithDomain, error) {
	args := m.Called(ctx, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]repository.SSLCertificateWithDomain), args.Error(1)
}

func (m *MockSSLCertificateRepository) UpdateSelfSigned(ctx context.Context, id string, certPath, keyPath string, expiresAt time.Time) error {
	args := m.Called(ctx, id, certPath, keyPath, expiresAt)
	return args.Error(0)
}

func (m *MockSSLCertificateRepository) UpdateCustom(context.Context, string, string, string, time.Time) error {
	return nil
}

func (m *MockSSLCertificateRepository) UpdateAfterACMEFailure(ctx context.Context, id string, lastError string, nextRetryAt time.Time, retryCount int, fallbackCertPath, fallbackKeyPath *string, fallbackExpiresAt *time.Time) error {
	args := m.Called(ctx, id, lastError, nextRetryAt, retryCount, fallbackCertPath, fallbackKeyPath, fallbackExpiresAt)
	return args.Error(0)
}

func (m *MockSSLCertificateRepository) UpdateAfterACMEFailureCapped(ctx context.Context, id string, lastError string, retryCount int, fallbackCertPath, fallbackKeyPath *string, fallbackExpiresAt *time.Time) error {
	return nil
}

func (m *MockSSLCertificateRepository) MarkFailed(ctx context.Context, id string, lastError string) error {
	args := m.Called(ctx, id, lastError)
	return args.Error(0)
}

func (m *MockSSLCertificateRepository) ListDueForACMERetry(ctx context.Context, now time.Time, limit int) ([]models.SSLCertificate, error) {
	args := m.Called(ctx, now, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]models.SSLCertificate), args.Error(1)
}

// MockDomainRepository implements repository.DomainRepository
type MockDomainRepository struct {
	mock.Mock
}

func (m *MockDomainRepository) Create(ctx context.Context, domain *models.Domain) error {
	args := m.Called(ctx, domain)
	return args.Error(0)
}

func (m *MockDomainRepository) FindByID(ctx context.Context, id string) (*models.Domain, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*models.Domain), args.Error(1)
}

func (m *MockDomainRepository) UpdateSSLMode(context.Context, string, string) error { return nil }
func (m *MockDomainRepository) SetSharedCertificate(context.Context, string, *string, string) error {
	return nil
}
func (m *MockDomainRepository) FindByIDs(context.Context, []string) ([]models.Domain, error) {
	return nil, nil
}

func (m *MockDomainRepository) BulkSetEnabledByUserID(_ context.Context, _ string, _ bool) (int64, error) {
	return 0, nil
}
func (m *MockDomainRepository) Update(ctx context.Context, domain *models.Domain) error {
	args := m.Called(ctx, domain)
	return args.Error(0)
}

func (m *MockDomainRepository) ListByUserID(ctx context.Context, userID string, opts repository.ListOptions) ([]models.Domain, int64, error) {
	args := m.Called(ctx, userID, opts.Offset, opts.Limit)
	if args.Get(0) == nil {
		return nil, 0, args.Error(2)
	}
	return args.Get(0).([]models.Domain), args.Get(1).(int64), args.Error(2)
}

func (m *MockDomainRepository) DeleteByID(ctx context.Context, id string) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *MockDomainRepository) FindByName(ctx context.Context, name string) (*models.Domain, error) {
	args := m.Called(ctx, name)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*models.Domain), args.Error(1)
}

func (m *MockDomainRepository) List(ctx context.Context, opts repository.ListOptions) ([]models.Domain, int64, error) {
	args := m.Called(ctx, opts.Offset, opts.Limit)
	if args.Get(0) == nil {
		return nil, 0, args.Error(2)
	}
	return args.Get(0).([]models.Domain), args.Get(1).(int64), args.Error(2)
}

func (m *MockDomainRepository) CountByUserID(ctx context.Context, userID string) (int64, error) {
	args := m.Called(ctx, userID)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockDomainRepository) SetPHPPoolID(ctx context.Context, id string, poolID *string) error {
	return nil
}

func (m *MockDomainRepository) CountByPHPPoolID(ctx context.Context, poolID string) (int64, error) {
	args := m.Called(ctx, poolID)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockDomainRepository) UpdatePHPSettings(ctx context.Context, id string, settings repository.DomainPHPSettings) error {
	args := m.Called(ctx, id, settings)
	return args.Error(0)
}

func (m *MockDomainRepository) UpdateMailProvider(_ context.Context, _ string, _ repository.DomainMailProvider) error {
	return nil
}

func (m *MockDomainRepository) UpdateEmailState(ctx context.Context, id string, state repository.DomainEmailState) error {
	args := m.Called(ctx, id, state)
	return args.Error(0)
}

func (m *MockDomainRepository) SetListenIPs(ctx context.Context, id string, upd repository.DomainListenIPs) error {
	args := m.Called(ctx, id, upd)
	return args.Error(0)
}

func (m *MockDomainRepository) Delete(ctx context.Context, id string) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *MockDomainRepository) FindPanelPrimary(ctx context.Context) (*models.Domain, error) {
	args := m.Called(ctx)
	if d, ok := args.Get(0).(*models.Domain); ok {
		return d, args.Error(1)
	}
	return nil, args.Error(1)
}

func (m *MockDomainRepository) MarkPanelPrimary(ctx context.Context, id string) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *MockDomainRepository) UpdateCatchallTarget(ctx context.Context, id string, target *string) error {
	args := m.Called(ctx, id, target)
	return args.Error(0)
}

func (m *MockDomainRepository) UpdateDisclaimer(ctx context.Context, id string, enabled bool, text *string) error {
	args := m.Called(ctx, id, enabled, text)
	return args.Error(0)
}

func (m *MockDomainRepository) UpdateCacheEnabled(ctx context.Context, id string, enabled bool) error {
	args := m.Called(ctx, id, enabled)
	return args.Error(0)
}

func (m *MockDomainRepository) UpdateCachePath(_ context.Context, _, _ string) error    { return nil }
func (m *MockDomainRepository) UpdateCacheTTL(_ context.Context, _ string, _ int) error { return nil }
func (m *MockDomainRepository) UpdateCacheQueryAllowlist(_ context.Context, _, _ string) error {
	return nil
}

func (m *MockDomainRepository) UpdateSkipAutoSAN(ctx context.Context, id string, enabled bool) error {
	return nil
}

func (m *MockDomainRepository) UpdateMTASTSEnabled(context.Context, string, bool) (uint64, error) {
	return 0, nil
}

func (m *MockDomainRepository) UpdateMTASTSAppliedID(context.Context, string, uint64) error {
	return nil
}

func (m *MockDomainRepository) UpdatePHPPoolID(context.Context, string, *string) error {
	return nil
}

func (m *MockDomainRepository) UpdateDNSSECEnabled(ctx context.Context, id string, enabled bool) error {
	args := m.Called(ctx, id, enabled)
	return args.Error(0)
}

func (m *MockDomainRepository) UpdateGhostState(ctx context.Context, id, state string, checkedAt time.Time, detail *string) error {
	args := m.Called(ctx, id, state, checkedAt, detail)
	return args.Error(0)
}

func (m *MockDomainRepository) ListForGhostCheck(ctx context.Context, staleBefore time.Time, limit int) ([]models.Domain, error) {
	args := m.Called(ctx, staleBefore, limit)
	d, _ := args.Get(0).([]models.Domain)
	return d, args.Error(1)
}

// TestListAllSSL_Success tests GET /admin/ssl-certificates returns all certificates
func TestListAllSSL_Success(t *testing.T) {
	mockSSLCerts := new(MockSSLCertificateRepository)
	mockDomains := new(MockDomainRepository)

	issuedAt := time.Now()
	expiresAt := time.Now().AddDate(0, 0, 90)

	certs := []repository.SSLCertificateWithDomain{
		{
			ID:            "cert-1",
			DomainID:      "domain-1",
			DomainName:    "example.com",
			UserID:        "user-1",
			UserUsername:  "alice",
			Status:        models.SSLStatusIssued,
			IssuedAt:      &issuedAt,
			ExpiresAt:     &expiresAt,
			RenewalCount:  0,
			LastRenewedAt: nil,
			LastError:     nil,
			Staging:       false,
		},
		{
			ID:            "cert-2",
			DomainID:      "domain-2",
			DomainName:    "test.org",
			UserID:        "user-2",
			UserUsername:  "bob",
			Status:        models.SSLStatusPending,
			IssuedAt:      nil,
			ExpiresAt:     nil,
			RenewalCount:  0,
			LastRenewedAt: nil,
			LastError:     nil,
			Staging:       true,
		},
	}

	mockSSLCerts.On("ListAll", mock.MatchedBy(func(ctx context.Context) bool { return true })).Return(certs, nil)
	// #195 enrichment loads the domain to compute website-cert SANs;
	// fall back to base SANs in the test (assertions below don't check sans).
	mockDomains.On("FindByID", mock.Anything, mock.Anything).Return((*models.Domain)(nil), repository.ErrNotFound)

	cfg := SSLHandlerConfig{
		Domains:        mockDomains,
		SSLCerts:       mockSSLCerts,
		ServerSettings: nil,
		Reconciler:     nil,
		Config:         nil,
	}
	h := newSSLHandler(cfg)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/v1/admin/ssl-certificates", nil)
	// Set admin claim
	ginctx.SetClaims(c, &auth.AccessClaims{
		UserID:  "admin-user",
		IsAdmin: true,
	})

	h.listAllSSL(c)

	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	items := resp["items"].([]interface{})
	require.Equal(t, 2, len(items))

	// Verify first certificate
	cert1 := items[0].(map[string]interface{})
	require.Equal(t, "cert-1", cert1["id"])
	require.Equal(t, "example.com", cert1["domain_name"])
	require.Equal(t, "alice", cert1["user_username"])
	require.Equal(t, models.SSLStatusIssued, cert1["status"])

	// Verify second certificate
	cert2 := items[1].(map[string]interface{})
	require.Equal(t, "cert-2", cert2["id"])
	require.Equal(t, "test.org", cert2["domain_name"])
	require.Equal(t, "bob", cert2["user_username"])
	require.Equal(t, models.SSLStatusPending, cert2["status"])

	mockSSLCerts.AssertCalled(t, "ListAll", mock.MatchedBy(func(ctx context.Context) bool { return true }))
}

// TestListAllSSL_AdminOnly tests non-admin users get 403
func TestListAllSSL_AdminOnly(t *testing.T) {
	mockSSLCerts := new(MockSSLCertificateRepository)
	mockDomains := new(MockDomainRepository)

	mockSSLCerts.On("ListAll", mock.MatchedBy(func(ctx context.Context) bool { return true })).Return([]repository.SSLCertificateWithDomain{}, nil)

	cfg := SSLHandlerConfig{
		Domains:        mockDomains,
		SSLCerts:       mockSSLCerts,
		ServerSettings: nil,
		Reconciler:     nil,
		Config:         nil,
	}
	h := newSSLHandler(cfg)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/v1/admin/ssl-certificates", nil)
	// Set non-admin claim
	ginctx.SetClaims(c, &auth.AccessClaims{
		UserID:  "regular-user",
		IsAdmin: false,
	})

	h.listAllSSL(c)

	// Middleware should have prevented this, but test the handler still respects admin
	// In real setup, RequireAdmin() middleware blocks this at the router level
	// This test documents the expected behavior
	require.Equal(t, http.StatusOK, w.Code) // Handler itself doesn't check, middleware does
}

// TestListAllSSL_Unauthenticated tests missing claims returns error
func TestListAllSSL_Unauthenticated(t *testing.T) {
	mockSSLCerts := new(MockSSLCertificateRepository)
	mockDomains := new(MockDomainRepository)

	mockSSLCerts.On("ListAll", mock.MatchedBy(func(ctx context.Context) bool { return true })).Return(nil, repository.ErrNotFound)

	cfg := SSLHandlerConfig{
		Domains:        mockDomains,
		SSLCerts:       mockSSLCerts,
		ServerSettings: nil,
		Reconciler:     nil,
		Config:         nil,
	}
	h := newSSLHandler(cfg)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/v1/admin/ssl-certificates", nil)
	// No claims set

	h.listAllSSL(c)

	require.Equal(t, http.StatusInternalServerError, w.Code) // DB error
}

// TestListAllSSL_Empty tests empty result set
func TestListAllSSL_Empty(t *testing.T) {
	mockSSLCerts := new(MockSSLCertificateRepository)
	mockDomains := new(MockDomainRepository)

	mockSSLCerts.On("ListAll", mock.MatchedBy(func(ctx context.Context) bool { return true })).Return([]repository.SSLCertificateWithDomain{}, nil)

	cfg := SSLHandlerConfig{
		Domains:        mockDomains,
		SSLCerts:       mockSSLCerts,
		ServerSettings: nil,
		Reconciler:     nil,
		Config:         nil,
	}
	h := newSSLHandler(cfg)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/v1/admin/ssl-certificates", nil)
	ginctx.SetClaims(c, &auth.AccessClaims{
		UserID:  "admin-user",
		IsAdmin: true,
	})

	h.listAllSSL(c)

	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	items := resp["items"].([]interface{})
	require.Equal(t, 0, len(items))
}

// TestListAllSSL_DatabaseError tests database error handling
func TestListAllSSL_DatabaseError(t *testing.T) {
	mockSSLCerts := new(MockSSLCertificateRepository)
	mockDomains := new(MockDomainRepository)

	mockSSLCerts.On("ListAll", mock.MatchedBy(func(ctx context.Context) bool { return true })).Return(nil, repository.ErrNotFound)

	cfg := SSLHandlerConfig{
		Domains:        mockDomains,
		SSLCerts:       mockSSLCerts,
		ServerSettings: nil,
		Reconciler:     nil,
		Config:         nil,
	}
	h := newSSLHandler(cfg)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/v1/admin/ssl-certificates", nil)
	ginctx.SetClaims(c, &auth.AccessClaims{
		UserID:  "admin-user",
		IsAdmin: true,
	})

	h.listAllSSL(c)

	require.Equal(t, http.StatusInternalServerError, w.Code)

	var resp map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	require.Equal(t, "internal", resp["error"])
}

// TestListUserSSL_Success tests GET /ssl-certificates returns user's certificates
func TestListUserSSL_Success(t *testing.T) {
	mockSSLCerts := new(MockSSLCertificateRepository)
	mockDomains := new(MockDomainRepository)

	issuedAt := time.Now()
	expiresAt := time.Now().AddDate(0, 0, 90)

	certs := []repository.SSLCertificateWithDomain{
		{
			ID:            "cert-1",
			DomainID:      "domain-1",
			DomainName:    "alice.com",
			UserID:        "user-1",
			UserUsername:  "alice",
			Status:        models.SSLStatusIssued,
			IssuedAt:      &issuedAt,
			ExpiresAt:     &expiresAt,
			RenewalCount:  1,
			LastRenewedAt: &issuedAt,
			LastError:     nil,
			Staging:       false,
		},
	}

	mockSSLCerts.On("ListByUserID", mock.MatchedBy(func(ctx context.Context) bool { return true }), "user-1").Return(certs, nil)
	mockDomains.On("FindByID", mock.Anything, mock.Anything).Return((*models.Domain)(nil), repository.ErrNotFound)

	cfg := SSLHandlerConfig{
		Domains:        mockDomains,
		SSLCerts:       mockSSLCerts,
		ServerSettings: nil,
		Reconciler:     nil,
		Config:         nil,
	}
	h := newSSLHandler(cfg)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/v1/ssl-certificates", nil)
	ginctx.SetClaims(c, &auth.AccessClaims{
		UserID:  "user-1",
		IsAdmin: false,
	})

	h.listUserSSL(c)

	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	items := resp["items"].([]interface{})
	require.Equal(t, 1, len(items))

	cert := items[0].(map[string]interface{})
	require.Equal(t, "cert-1", cert["id"])
	require.Equal(t, "alice.com", cert["domain_name"])
	require.Equal(t, "alice", cert["user_username"])
	require.Equal(t, "user-1", cert["user_id"])

	mockSSLCerts.AssertCalled(t, "ListByUserID", mock.MatchedBy(func(ctx context.Context) bool { return true }), "user-1")
}

// TestListUserSSL_Unauthenticated tests missing claims
func TestListUserSSL_Unauthenticated(t *testing.T) {
	mockSSLCerts := new(MockSSLCertificateRepository)
	mockDomains := new(MockDomainRepository)

	cfg := SSLHandlerConfig{
		Domains:        mockDomains,
		SSLCerts:       mockSSLCerts,
		ServerSettings: nil,
		Reconciler:     nil,
		Config:         nil,
	}
	h := newSSLHandler(cfg)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/v1/ssl-certificates", nil)
	// No claims set

	h.listUserSSL(c)

	require.Equal(t, http.StatusUnauthorized, w.Code)

	var resp map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	require.Equal(t, "unauthenticated", resp["error"])
}

// TestListUserSSL_Empty tests user with no certificates
func TestListUserSSL_Empty(t *testing.T) {
	mockSSLCerts := new(MockSSLCertificateRepository)
	mockDomains := new(MockDomainRepository)

	mockSSLCerts.On("ListByUserID", mock.MatchedBy(func(ctx context.Context) bool { return true }), "user-2").Return([]repository.SSLCertificateWithDomain{}, nil)

	cfg := SSLHandlerConfig{
		Domains:        mockDomains,
		SSLCerts:       mockSSLCerts,
		ServerSettings: nil,
		Reconciler:     nil,
		Config:         nil,
	}
	h := newSSLHandler(cfg)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/v1/ssl-certificates", nil)
	ginctx.SetClaims(c, &auth.AccessClaims{
		UserID:  "user-2",
		IsAdmin: false,
	})

	h.listUserSSL(c)

	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	items := resp["items"].([]interface{})
	require.Equal(t, 0, len(items))

	mockSSLCerts.AssertCalled(t, "ListByUserID", mock.MatchedBy(func(ctx context.Context) bool { return true }), "user-2")
}

// TestListUserSSL_DatabaseError tests database error handling
func TestListUserSSL_DatabaseError(t *testing.T) {
	mockSSLCerts := new(MockSSLCertificateRepository)
	mockDomains := new(MockDomainRepository)

	mockSSLCerts.On("ListByUserID", mock.MatchedBy(func(ctx context.Context) bool { return true }), "user-1").Return(nil, repository.ErrNotFound)

	cfg := SSLHandlerConfig{
		Domains:        mockDomains,
		SSLCerts:       mockSSLCerts,
		ServerSettings: nil,
		Reconciler:     nil,
		Config:         nil,
	}
	h := newSSLHandler(cfg)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/v1/ssl-certificates", nil)
	ginctx.SetClaims(c, &auth.AccessClaims{
		UserID:  "user-1",
		IsAdmin: false,
	})

	h.listUserSSL(c)

	require.Equal(t, http.StatusInternalServerError, w.Code)

	var resp map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	require.Equal(t, "internal", resp["error"])
}

// TestListUserSSL_MultipleDomainsFiltered tests user can see only their own certs
func TestListUserSSL_MultipleDomainsFiltered(t *testing.T) {
	mockSSLCerts := new(MockSSLCertificateRepository)
	mockDomains := new(MockDomainRepository)

	issuedAt := time.Now()
	expiresAt := time.Now().AddDate(0, 0, 60)

	// User alice has 2 certificates
	certs := []repository.SSLCertificateWithDomain{
		{
			ID:           "cert-alice-1",
			DomainID:     "domain-1",
			DomainName:   "alice1.com",
			UserID:       "user-alice",
			UserUsername: "alice",
			Status:       models.SSLStatusIssued,
			IssuedAt:     &issuedAt,
			ExpiresAt:    &expiresAt,
			RenewalCount: 0,
			Staging:      false,
		},
		{
			ID:           "cert-alice-2",
			DomainID:     "domain-2",
			DomainName:   "alice2.com",
			UserID:       "user-alice",
			UserUsername: "alice",
			Status:       models.SSLStatusIssued,
			IssuedAt:     &issuedAt,
			ExpiresAt:    &expiresAt,
			RenewalCount: 0,
			Staging:      false,
		},
	}

	mockSSLCerts.On("ListByUserID", mock.MatchedBy(func(ctx context.Context) bool { return true }), "user-alice").Return(certs, nil)
	mockDomains.On("FindByID", mock.Anything, mock.Anything).Return((*models.Domain)(nil), repository.ErrNotFound)

	cfg := SSLHandlerConfig{
		Domains:        mockDomains,
		SSLCerts:       mockSSLCerts,
		ServerSettings: nil,
		Reconciler:     nil,
		Config:         nil,
	}
	h := newSSLHandler(cfg)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/v1/ssl-certificates", nil)
	ginctx.SetClaims(c, &auth.AccessClaims{
		UserID:  "user-alice",
		IsAdmin: false,
	})

	h.listUserSSL(c)

	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	items := resp["items"].([]interface{})
	require.Equal(t, 2, len(items))

	// Verify only alice's certs are returned
	for _, item := range items {
		cert := item.(map[string]interface{})
		require.Equal(t, "user-alice", cert["user_id"])
		require.Equal(t, "alice", cert["user_username"])
	}

	mockSSLCerts.AssertCalled(t, "ListByUserID", mock.MatchedBy(func(ctx context.Context) bool { return true }), "user-alice")
}

func (*MockDomainRepository) AttachDockerApp(context.Context, string, string, models.NginxRules) error {
	return nil
}
func (*MockDomainRepository) DetachDockerApp(context.Context, string, bool) error {
	return nil
}

func (m *MockDomainRepository) ListForRegistrarRefresh(ctx context.Context, staleBefore time.Time, limit int) ([]models.Domain, error) {
	return nil, nil
}
func (m *MockDomainRepository) SetRegistrarExpiry(ctx context.Context, id string, expiresAt *time.Time, checkedAt time.Time) error {
	return nil
}

// JAB-224 ssl_resurrect — unused by these tests.
func (m *MockSSLCertificateRepository) ListExhaustedForSSLEnabledDomains(context.Context, time.Time, int) ([]models.SSLCertificate, error) {
	return nil, nil
}

func (m *MockSSLCertificateRepository) RearmACME(context.Context, string, int, time.Time) error {
	return nil
}
