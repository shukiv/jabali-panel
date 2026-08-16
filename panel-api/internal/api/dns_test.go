package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Mock repositories

type mockDomainRepo struct {
	domains map[string]*models.Domain
}

func newMockDomainRepo() *mockDomainRepo {
	return &mockDomainRepo{domains: make(map[string]*models.Domain)}
}

func (m *mockDomainRepo) Create(ctx context.Context, d *models.Domain) error {
	m.domains[d.ID] = d
	return nil
}

func (m *mockDomainRepo) Update(ctx context.Context, d *models.Domain) error {
	m.domains[d.ID] = d
	return nil
}

func (m *mockDomainRepo) BulkSetEnabledByUserID(_ context.Context, _ string, _ bool) (int64, error) {
	return 0, nil
}

func (m *mockDomainRepo) Delete(ctx context.Context, id string) error {
	delete(m.domains, id)
	return nil
}

func (m *mockDomainRepo) FindByID(ctx context.Context, id string) (*models.Domain, error) {
	if d, ok := m.domains[id]; ok {
		return d, nil
	}
	return nil, repository.ErrNotFound
}

func (m *mockDomainRepo) FindByIDs(_ context.Context, ids []string) ([]models.Domain, error) {
	var out []models.Domain
	for _, id := range ids {
		if d, ok := m.domains[id]; ok {
			out = append(out, *d)
		}
	}
	return out, nil
}

func (m *mockDomainRepo) FindByName(ctx context.Context, name string) (*models.Domain, error) {
	for _, d := range m.domains {
		if d.Name == name {
			return d, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *mockDomainRepo) List(ctx context.Context, opts repository.ListOptions) ([]models.Domain, int64, error) {
	return nil, 0, nil
}

func (m *mockDomainRepo) ListByUserID(ctx context.Context, userID string, opts repository.ListOptions) ([]models.Domain, int64, error) {
	return nil, 0, nil
}

func (m *mockDomainRepo) ListForRegistrarRefresh(ctx context.Context, staleBefore time.Time, limit int) ([]models.Domain, error) {
	return nil, nil
}
func (m *mockDomainRepo) SetRegistrarExpiry(ctx context.Context, id string, expiresAt *time.Time, checkedAt time.Time) error {
	return nil
}
func (m *mockDomainRepo) CountByUserID(ctx context.Context, userID string) (int64, error) {
	return 0, nil
}

func (m *mockDomainRepo) SetPHPPoolID(ctx context.Context, id string, poolID *string) error {
	return nil
}

func (m *mockDomainRepo) CountByPHPPoolID(ctx context.Context, poolID string) (int64, error) {
	return 0, nil
}

func (m *mockDomainRepo) UpdatePHPSettings(ctx context.Context, id string, settings repository.DomainPHPSettings) error {
	return nil
}

func (m *mockDomainRepo) UpdateMailProvider(_ context.Context, _ string, _ repository.DomainMailProvider) error {
	return nil
}

func (m *mockDomainRepo) UpdateSSLMode(context.Context, string, string) error { return nil }
func (m *mockDomainRepo) SetSharedCertificate(context.Context, string, *string, string) error {
	return nil
}

func (m *mockDomainRepo) UpdateEmailState(ctx context.Context, id string, state repository.DomainEmailState) error {
	d, ok := m.domains[id]
	if !ok {
		return repository.ErrNotFound
	}
	d.EmailEnabled = state.Enabled
	d.EmailEnabledAt = state.EmailEnabledAt
	if state.DkimSelector != nil {
		d.DkimSelector = state.DkimSelector
	}
	if state.DkimPublicKey != nil {
		d.DkimPublicKey = state.DkimPublicKey
	}
	return nil
}

func (m *mockDomainRepo) FindPanelPrimary(ctx context.Context) (*models.Domain, error) {
	for _, d := range m.domains {
		if d.IsPanelPrimary {
			return d, nil
		}
	}
	return nil, repository.ErrPanelPrimaryNotFound
}

func (m *mockDomainRepo) MarkPanelPrimary(ctx context.Context, id string) error {
	if _, ok := m.domains[id]; !ok {
		return repository.ErrNotFound
	}
	for otherID, d := range m.domains {
		if otherID == id {
			continue
		}
		d.IsPanelPrimary = false
	}
	m.domains[id].IsPanelPrimary = true
	return nil
}

func (m *mockDomainRepo) SetListenIPs(ctx context.Context, id string, upd repository.DomainListenIPs) error {
	d, ok := m.domains[id]
	if !ok {
		return repository.ErrNotFound
	}
	if upd.ChangeIPv4 {
		d.ListenIPv4ID = upd.IPv4ID
	}
	if upd.ChangeIPv6 {
		d.ListenIPv6ID = upd.IPv6ID
	}
	return nil
}

func (m *mockDomainRepo) UpdateCatchallTarget(ctx context.Context, id string, target *string) error {
	d, ok := m.domains[id]
	if !ok {
		return repository.ErrNotFound
	}
	d.CatchallTarget = target
	return nil
}

func (m *mockDomainRepo) UpdateDisclaimer(ctx context.Context, id string, enabled bool, text *string) error {
	d, ok := m.domains[id]
	if !ok {
		return repository.ErrNotFound
	}
	d.DisclaimerEnabled = enabled
	d.DisclaimerText = text
	return nil
}

func (m *mockDomainRepo) UpdateCacheEnabled(ctx context.Context, id string, enabled bool) error {
	return nil
}

func (m *mockDomainRepo) UpdateCachePath(_ context.Context, _, _ string) error    { return nil }
func (m *mockDomainRepo) UpdateCacheTTL(_ context.Context, _ string, _ int) error { return nil }
func (m *mockDomainRepo) UpdateCacheQueryAllowlist(_ context.Context, _, _ string) error {
	return nil
}

func (m *mockDomainRepo) UpdateSkipAutoSAN(ctx context.Context, id string, enabled bool) error {
	return nil
}

func (m *mockDomainRepo) UpdateMTASTSEnabled(ctx context.Context, id string, enabled bool) (uint64, error) {
	d, ok := m.domains[id]
	if !ok {
		return 0, repository.ErrNotFound
	}
	d.MTASTSEnabled = enabled
	var newID uint64
	if enabled {
		newID = uint64(time.Now().UTC().Unix())
		d.MTASTSId = newID
	}
	return newID, nil
}

func (m *mockDomainRepo) UpdateMTASTSAppliedID(context.Context, string, uint64) error {
	return nil
}

func (m *mockDomainRepo) UpdatePHPPoolID(context.Context, string, *string) error {
	return nil
}

func (m *mockDomainRepo) UpdateDNSSECEnabled(ctx context.Context, id string, enabled bool) error {
	d, ok := m.domains[id]
	if !ok {
		return repository.ErrNotFound
	}
	d.DNSSECEnabled = enabled
	return nil
}

func (m *mockDomainRepo) UpdateGhostState(ctx context.Context, id, state string, checkedAt time.Time, detail *string) error {
	d, ok := m.domains[id]
	if !ok {
		return repository.ErrNotFound
	}
	d.GhostState = state
	t := checkedAt
	d.GhostCheckedAt = &t
	d.GhostDetail = detail
	return nil
}

func (m *mockDomainRepo) ListForGhostCheck(ctx context.Context, staleBefore time.Time, limit int) ([]models.Domain, error) {
	return nil, nil
}

type mockDNSZoneRepo struct {
	zones map[string]*models.DNSZone
}

func newMockDNSZoneRepo() *mockDNSZoneRepo {
	return &mockDNSZoneRepo{zones: make(map[string]*models.DNSZone)}
}

func (m *mockDNSZoneRepo) Create(ctx context.Context, z *models.DNSZone) error {
	m.zones[z.ID] = z
	return nil
}

func (m *mockDNSZoneRepo) Update(ctx context.Context, z *models.DNSZone) error {
	m.zones[z.ID] = z
	return nil
}

func (m *mockDNSZoneRepo) Delete(ctx context.Context, id string) error {
	delete(m.zones, id)
	return nil
}

func (m *mockDNSZoneRepo) FindByID(ctx context.Context, id string) (*models.DNSZone, error) {
	if z, ok := m.zones[id]; ok {
		return z, nil
	}
	return nil, repository.ErrNotFound
}

func (m *mockDNSZoneRepo) FindByName(ctx context.Context, name string) (*models.DNSZone, error) {
	for _, z := range m.zones {
		if z.Name == name {
			return z, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *mockDNSZoneRepo) FindByDomainID(ctx context.Context, domainID string) (*models.DNSZone, error) {
	for _, z := range m.zones {
		if z.DomainID == domainID {
			return z, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *mockDNSZoneRepo) ListAll(ctx context.Context) ([]models.DNSZone, error) {
	var zones []models.DNSZone
	for _, z := range m.zones {
		zones = append(zones, *z)
	}
	return zones, nil
}

type mockDNSRecordRepo struct {
	records map[string]*models.DNSRecord
}

func newMockDNSRecordRepo() *mockDNSRecordRepo {
	return &mockDNSRecordRepo{records: make(map[string]*models.DNSRecord)}
}

func (m *mockDNSRecordRepo) Create(ctx context.Context, r *models.DNSRecord) error {
	m.records[r.ID] = r
	return nil
}

func (m *mockDNSRecordRepo) Update(ctx context.Context, r *models.DNSRecord) error {
	m.records[r.ID] = r
	return nil
}

func (m *mockDNSRecordRepo) Delete(ctx context.Context, id string) error {
	delete(m.records, id)
	return nil
}

func (m *mockDNSRecordRepo) FindByID(ctx context.Context, id string) (*models.DNSRecord, error) {
	if r, ok := m.records[id]; ok {
		return r, nil
	}
	return nil, repository.ErrNotFound
}

func (m *mockDNSRecordRepo) ListByZoneID(ctx context.Context, zoneID string) ([]models.DNSRecord, error) {
	var records []models.DNSRecord
	for _, r := range m.records {
		if r.ZoneID == zoneID {
			records = append(records, *r)
		}
	}
	return records, nil
}

func (m *mockDNSRecordRepo) DeleteByZoneID(ctx context.Context, zoneID string) error {
	for id, r := range m.records {
		if r.ZoneID == zoneID {
			delete(m.records, id)
		}
	}
	return nil
}

func (m *mockDNSRecordRepo) DeleteByZoneIDAndManagedBy(ctx context.Context, zoneID, managedBy string) error {
	for id, r := range m.records {
		if r.ZoneID != zoneID {
			continue
		}
		if r.ManagedBy == nil || *r.ManagedBy != managedBy {
			continue
		}
		delete(m.records, id)
	}
	return nil
}

// Mock reconciler
type mockDNSReconciler struct {
	scheduled []string
}

func (m *mockDNSReconciler) Schedule(domainID string) {
	m.scheduled = append(m.scheduled, domainID)
}

// Helper to setup router with optional claims
func dnsRouter(userID string, isAdmin bool) (*gin.Engine, *mockDomainRepo, *mockDNSZoneRepo, *mockDNSRecordRepo, *mockDNSReconciler) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")

	// Inject claims if provided
	if userID != "" {
		v1.Use(func(c *gin.Context) {
			ginctx.SetClaims(c, &auth.AccessClaims{
				UserID:  userID,
				IsAdmin: isAdmin,
			})
			c.Next()
		})
	}

	domainRepo := newMockDomainRepo()
	zoneRepo := newMockDNSZoneRepo()
	recordRepo := newMockDNSRecordRepo()
	reconciler := &mockDNSReconciler{}

	RegisterDNSRoutes(v1, DNSHandlerConfig{
		Domains:    domainRepo,
		Zones:      zoneRepo,
		Records:    recordRepo,
		Reconciler: reconciler,
	})

	return r, domainRepo, zoneRepo, recordRepo, reconciler
}

// Test cases

func TestGetZoneUnauthenticated(t *testing.T) {
	r, _, _, _, _ := dnsRouter("", false)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/domains/test-domain-id/dns/zone", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, "unauthenticated", resp["error"])
}

func TestGetZoneNotFound(t *testing.T) {
	r, domainRepo, _, _, _ := dnsRouter("user1", false)

	// Add domain to repo
	domain := &models.Domain{
		ID:     "test-domain-id",
		UserID: "user1",
		Name:   "example.com",
	}
	domainRepo.Create(context.Background(), domain)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/domains/test-domain-id/dns/zone", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, "zone_not_provisioned", resp["error"])
}

func TestGetZoneNonOwnerForbidden(t *testing.T) {
	r, domainRepo, _, _, _ := dnsRouter("user2", false)

	domain := &models.Domain{
		ID:     "test-domain-id",
		UserID: "user1",
		Name:   "example.com",
	}
	domainRepo.Create(context.Background(), domain)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/domains/test-domain-id/dns/zone", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
}

func TestGetZoneSuccess(t *testing.T) {
	r, domainRepo, zoneRepo, _, _ := dnsRouter("user1", false)

	domain := &models.Domain{
		ID:     "test-domain-id",
		UserID: "user1",
		Name:   "example.com",
	}
	domainRepo.Create(context.Background(), domain)

	zoneID := ids.NewULID()
	zone := &models.DNSZone{
		ID:             zoneID,
		DomainID:       "test-domain-id",
		Name:           "example.com",
		Serial:         1,
		RefreshSeconds: 3600,
		RetrySeconds:   600,
		ExpireSeconds:  604800,
		MinimumTTL:     3600,
		IsEnabled:      true,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	zoneRepo.Create(context.Background(), zone)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/domains/test-domain-id/dns/zone", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	assert.NotNil(t, resp["zone"])
}

func TestUpdateZoneSOATimers(t *testing.T) {
	r, domainRepo, zoneRepo, _, reconciler := dnsRouter("user1", false)

	domain := &models.Domain{
		ID:     "test-domain-id",
		UserID: "user1",
		Name:   "example.com",
	}
	domainRepo.Create(context.Background(), domain)

	zoneID := ids.NewULID()
	zone := &models.DNSZone{
		ID:             zoneID,
		DomainID:       "test-domain-id",
		Name:           "example.com",
		Serial:         1,
		RefreshSeconds: 3600,
		RetrySeconds:   600,
		ExpireSeconds:  604800,
		MinimumTTL:     3600,
		IsEnabled:      true,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	zoneRepo.Create(context.Background(), zone)

	// Update with new refresh_seconds
	refreshSeconds := 7200
	body, _ := json.Marshal(map[string]interface{}{
		"refresh_seconds": refreshSeconds,
	})

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/domains/test-domain-id/dns/zone", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	// Verify reconciler was called
	assert.Equal(t, 1, len(reconciler.scheduled))
	assert.Equal(t, "test-domain-id", reconciler.scheduled[0])

	// Verify zone was updated
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	zoneResp := resp["zone"].(map[string]interface{})
	assert.Equal(t, float64(7200), zoneResp["refresh_seconds"])
}

func TestUpdateZoneOutOfRangeRefresh(t *testing.T) {
	r, domainRepo, zoneRepo, _, _ := dnsRouter("user1", false)

	domain := &models.Domain{
		ID:     "test-domain-id",
		UserID: "user1",
		Name:   "example.com",
	}
	domainRepo.Create(context.Background(), domain)

	zoneID := ids.NewULID()
	zone := &models.DNSZone{
		ID:             zoneID,
		DomainID:       "test-domain-id",
		Name:           "example.com",
		Serial:         1,
		RefreshSeconds: 3600,
		RetrySeconds:   600,
		ExpireSeconds:  604800,
		MinimumTTL:     3600,
		IsEnabled:      true,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	zoneRepo.Create(context.Background(), zone)

	// Update with invalid refresh_seconds (too low)
	body, _ := json.Marshal(map[string]interface{}{
		"refresh_seconds": 30,
	})

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/domains/test-domain-id/dns/zone", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, "validation_failed", resp["error"])
}

func TestCreateRecordSuccess(t *testing.T) {
	r, domainRepo, zoneRepo, _, reconciler := dnsRouter("user1", false)

	domain := &models.Domain{
		ID:     "test-domain-id",
		UserID: "user1",
		Name:   "example.com",
	}
	domainRepo.Create(context.Background(), domain)

	zoneID := ids.NewULID()
	zone := &models.DNSZone{
		ID:             zoneID,
		DomainID:       "test-domain-id",
		Name:           "example.com",
		Serial:         1,
		RefreshSeconds: 3600,
		RetrySeconds:   600,
		ExpireSeconds:  604800,
		MinimumTTL:     3600,
		IsEnabled:      true,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	zoneRepo.Create(context.Background(), zone)

	// Create A record
	body, _ := json.Marshal(map[string]interface{}{
		"name":    "www",
		"type":    "A",
		"content": "192.168.1.1",
		"ttl":     3600,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/domains/test-domain-id/dns/records", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)

	// Verify reconciler was called
	assert.Equal(t, 1, len(reconciler.scheduled))
	assert.Equal(t, "test-domain-id", reconciler.scheduled[0])

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	assert.NotNil(t, resp["record"])
}

func TestCreateRecordInvalidIPv4(t *testing.T) {
	r, domainRepo, zoneRepo, _, _ := dnsRouter("user1", false)

	domain := &models.Domain{
		ID:     "test-domain-id",
		UserID: "user1",
		Name:   "example.com",
	}
	domainRepo.Create(context.Background(), domain)

	zoneID := ids.NewULID()
	zone := &models.DNSZone{
		ID:             zoneID,
		DomainID:       "test-domain-id",
		Name:           "example.com",
		Serial:         1,
		RefreshSeconds: 3600,
		RetrySeconds:   600,
		ExpireSeconds:  604800,
		MinimumTTL:     3600,
		IsEnabled:      true,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	zoneRepo.Create(context.Background(), zone)

	// Create A record with IPv6 content
	body, _ := json.Marshal(map[string]interface{}{
		"name":    "www",
		"type":    "A",
		"content": "::1",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/domains/test-domain-id/dns/records", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, "invalid_record", resp["error"])
}

func TestCreateRecordLongTXT(t *testing.T) {
	r, domainRepo, zoneRepo, _, _ := dnsRouter("user1", false)

	domain := &models.Domain{
		ID:     "test-domain-id",
		UserID: "user1",
		Name:   "example.com",
	}
	domainRepo.Create(context.Background(), domain)

	zoneID := ids.NewULID()
	zone := &models.DNSZone{
		ID:             zoneID,
		DomainID:       "test-domain-id",
		Name:           "example.com",
		Serial:         1,
		RefreshSeconds: 3600,
		RetrySeconds:   600,
		ExpireSeconds:  604800,
		MinimumTTL:     3600,
		IsEnabled:      true,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	zoneRepo.Create(context.Background(), zone)

	// Create TXT record with overly long content
	longContent := ""
	for i := 0; i < 5000; i++ {
		longContent += "a"
	}

	body, _ := json.Marshal(map[string]interface{}{
		"name":    "_dmarc",
		"type":    "TXT",
		"content": longContent,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/domains/test-domain-id/dns/records", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, "invalid_record", resp["error"])
}

func TestUpdateRecordManagedSOAForbidden(t *testing.T) {
	r, domainRepo, zoneRepo, recordRepo, _ := dnsRouter("user1", false)

	domain := &models.Domain{
		ID:     "test-domain-id",
		UserID: "user1",
		Name:   "example.com",
	}
	domainRepo.Create(context.Background(), domain)

	zoneID := ids.NewULID()
	zone := &models.DNSZone{
		ID:             zoneID,
		DomainID:       "test-domain-id",
		Name:           "example.com",
		Serial:         1,
		RefreshSeconds: 3600,
		RetrySeconds:   600,
		ExpireSeconds:  604800,
		MinimumTTL:     3600,
		IsEnabled:      true,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	zoneRepo.Create(context.Background(), zone)

	recordID := ids.NewULID()
	record := &models.DNSRecord{
		ID:        recordID,
		ZoneID:    zoneID,
		Name:      "@",
		Type:      "SOA",
		Content:   "ns.example.com. admin.example.com. 1 3600 600 604800 3600",
		TTL:       3600,
		Priority:  0,
		Managed:   true,
		IsEnabled: true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	recordRepo.Create(context.Background(), record)

	// Try to update managed SOA
	body, _ := json.Marshal(map[string]interface{}{
		"content": "updated.example.com. admin.example.com. 2 3600 600 604800 3600",
	})

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/dns/records/"+recordID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, "record_managed", resp["error"])
}

func TestUpdateRecordManagedARecord(t *testing.T) {
	r, domainRepo, zoneRepo, recordRepo, reconciler := dnsRouter("user1", false)

	domain := &models.Domain{
		ID:     "test-domain-id",
		UserID: "user1",
		Name:   "example.com",
	}
	domainRepo.Create(context.Background(), domain)

	zoneID := ids.NewULID()
	zone := &models.DNSZone{
		ID:             zoneID,
		DomainID:       "test-domain-id",
		Name:           "example.com",
		Serial:         1,
		RefreshSeconds: 3600,
		RetrySeconds:   600,
		ExpireSeconds:  604800,
		MinimumTTL:     3600,
		IsEnabled:      true,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	zoneRepo.Create(context.Background(), zone)

	recordID := ids.NewULID()
	record := &models.DNSRecord{
		ID:        recordID,
		ZoneID:    zoneID,
		Name:      "@",
		Type:      "A",
		Content:   "192.168.1.1",
		TTL:       3600,
		Priority:  0,
		Managed:   true,
		IsEnabled: true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	recordRepo.Create(context.Background(), record)

	// Update managed A record (should succeed - it's editable)
	body, _ := json.Marshal(map[string]interface{}{
		"content": "192.168.1.2",
	})

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/dns/records/"+recordID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	// Verify reconciler was called
	assert.Equal(t, 1, len(reconciler.scheduled))

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	recordResp := resp["record"].(map[string]interface{})
	assert.Equal(t, "192.168.1.2", recordResp["content"])
}

// ADR-0107: editing a jabali-managed MX is allowed; the edit is
// authoritative and demotes the row to operator-owned (Managed=false)
// so no reconciler reverts it. (Was blocked pre-ADR-0107.)
func TestUpdateRecordManagedMXEditableDemotes(t *testing.T) {
	r, domainRepo, zoneRepo, recordRepo, reconciler := dnsRouter("user1", false)

	domain := &models.Domain{ID: "test-domain-id", UserID: "user1", Name: "example.com"}
	domainRepo.Create(context.Background(), domain)

	zoneID := ids.NewULID()
	zone := &models.DNSZone{
		ID: zoneID, DomainID: "test-domain-id", Name: "example.com",
		Serial: 1, RefreshSeconds: 3600, RetrySeconds: 600,
		ExpireSeconds: 604800, MinimumTTL: 3600, IsEnabled: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	zoneRepo.Create(context.Background(), zone)

	recordID := ids.NewULID()
	m6 := "m6"
	record := &models.DNSRecord{
		ID: recordID, ZoneID: zoneID, Name: "@", Type: "MX",
		Content: "mail.example.com.", TTL: 3600, Priority: 10,
		Managed: true, ManagedBy: &m6, IsEnabled: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	recordRepo.Create(context.Background(), record)

	body, _ := json.Marshal(map[string]interface{}{"content": "mx.riva.local."})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/dns/records/"+recordID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 1, len(reconciler.scheduled))
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	rec := resp["record"].(map[string]interface{})
	assert.Equal(t, "mx.riva.local.", rec["content"])
	// Demoted to operator-owned so the reconciler hands off.
	assert.Equal(t, false, rec["managed"])
	_, hasManagedBy := rec["managed_by"]
	assert.False(t, hasManagedBy, "managed_by must be cleared (omitempty) on operator edit")
}

func TestDeleteRecordManagedNSForbidden(t *testing.T) {
	r, domainRepo, zoneRepo, recordRepo, _ := dnsRouter("user1", false)

	domain := &models.Domain{
		ID:     "test-domain-id",
		UserID: "user1",
		Name:   "example.com",
	}
	domainRepo.Create(context.Background(), domain)

	zoneID := ids.NewULID()
	zone := &models.DNSZone{
		ID:             zoneID,
		DomainID:       "test-domain-id",
		Name:           "example.com",
		Serial:         1,
		RefreshSeconds: 3600,
		RetrySeconds:   600,
		ExpireSeconds:  604800,
		MinimumTTL:     3600,
		IsEnabled:      true,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	zoneRepo.Create(context.Background(), zone)

	recordID := ids.NewULID()
	record := &models.DNSRecord{
		ID:        recordID,
		ZoneID:    zoneID,
		Name:      "@",
		Type:      "NS",
		Content:   "ns1.example.com.",
		TTL:       3600,
		Priority:  0,
		Managed:   true,
		IsEnabled: true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	recordRepo.Create(context.Background(), record)

	// Try to delete managed NS
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/dns/records/"+recordID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, "record_managed", resp["error"])
}

func TestDeleteRecordNonManagedSuccess(t *testing.T) {
	r, domainRepo, zoneRepo, recordRepo, reconciler := dnsRouter("user1", false)

	domain := &models.Domain{
		ID:     "test-domain-id",
		UserID: "user1",
		Name:   "example.com",
	}
	domainRepo.Create(context.Background(), domain)

	zoneID := ids.NewULID()
	zone := &models.DNSZone{
		ID:             zoneID,
		DomainID:       "test-domain-id",
		Name:           "example.com",
		Serial:         1,
		RefreshSeconds: 3600,
		RetrySeconds:   600,
		ExpireSeconds:  604800,
		MinimumTTL:     3600,
		IsEnabled:      true,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	zoneRepo.Create(context.Background(), zone)

	recordID := ids.NewULID()
	record := &models.DNSRecord{
		ID:        recordID,
		ZoneID:    zoneID,
		Name:      "www",
		Type:      "A",
		Content:   "192.168.1.1",
		TTL:       3600,
		Priority:  0,
		Managed:   false,
		IsEnabled: true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	recordRepo.Create(context.Background(), record)

	// Delete non-managed record
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/dns/records/"+recordID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusNoContent, w.Code)

	// Verify reconciler was called
	assert.Equal(t, 1, len(reconciler.scheduled))
	assert.Equal(t, "test-domain-id", reconciler.scheduled[0])
}

func TestListRecordsSuccess(t *testing.T) {
	r, domainRepo, zoneRepo, recordRepo, _ := dnsRouter("user1", false)

	domain := &models.Domain{
		ID:     "test-domain-id",
		UserID: "user1",
		Name:   "example.com",
	}
	domainRepo.Create(context.Background(), domain)

	zoneID := ids.NewULID()
	zone := &models.DNSZone{
		ID:             zoneID,
		DomainID:       "test-domain-id",
		Name:           "example.com",
		Serial:         1,
		RefreshSeconds: 3600,
		RetrySeconds:   600,
		ExpireSeconds:  604800,
		MinimumTTL:     3600,
		IsEnabled:      true,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	zoneRepo.Create(context.Background(), zone)

	// Add records
	for i := 0; i < 3; i++ {
		record := &models.DNSRecord{
			ID:        ids.NewULID(),
			ZoneID:    zoneID,
			Name:      "www",
			Type:      "A",
			Content:   "192.168.1.1",
			TTL:       3600,
			Priority:  0,
			Managed:   false,
			IsEnabled: true,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		recordRepo.Create(context.Background(), record)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/domains/test-domain-id/dns/records", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	records := resp["records"].([]interface{})
	assert.Equal(t, 3, len(records))
}

func TestAdminCanAccessAnyDomain(t *testing.T) {
	r, domainRepo, zoneRepo, _, _ := dnsRouter("admin-user", true)

	domain := &models.Domain{
		ID:     "test-domain-id",
		UserID: "user1",
		Name:   "example.com",
	}
	domainRepo.Create(context.Background(), domain)

	zoneID := ids.NewULID()
	zone := &models.DNSZone{
		ID:             zoneID,
		DomainID:       "test-domain-id",
		Name:           "example.com",
		Serial:         1,
		RefreshSeconds: 3600,
		RetrySeconds:   600,
		ExpireSeconds:  604800,
		MinimumTTL:     3600,
		IsEnabled:      true,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	zoneRepo.Create(context.Background(), zone)

	// Admin accessing user1's domain zone
	req := httptest.NewRequest(http.MethodGet, "/api/v1/domains/test-domain-id/dns/zone", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	assert.NotNil(t, resp["zone"])
}

// --- GET /domains/:id/dns/system-records -------------------------------

// fakeSettingsRepo is a minimal ServerSettingsRepository stub used only
// in the system-records tests below. The rest of the DNS test suite
// runs without settings (nil repo), so we keep the fake isolated to
// avoid coupling unrelated tests to a concrete settings shape.
type fakeSettingsRepo struct {
	s *models.ServerSettings
}

func (f *fakeSettingsRepo) Get(ctx context.Context) (*models.ServerSettings, error) {
	if f.s == nil {
		return nil, repository.ErrNotFound
	}
	return f.s, nil
}
func (f *fakeSettingsRepo) Upsert(ctx context.Context, s *models.ServerSettings) error {
	f.s = s
	return nil
}
func (f *fakeSettingsRepo) SetDigestLastSent(context.Context, string) error            { return nil }
func (f *fakeSettingsRepo) RecordDRSync(context.Context, string, string, string) error { return nil }

func (f *fakeSettingsRepo) EnsureVAPID(ctx context.Context, hostname string) (bool, error) {
	return false, nil
}

func (f *fakeSettingsRepo) ReassertDRPairing(context.Context, string, string, *time.Time) error {
	return nil
}

func dnsRouterWithSettings(userID string, isAdmin bool, s *models.ServerSettings) (*gin.Engine, *mockDomainRepo, *mockDNSZoneRepo) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	if userID != "" {
		v1.Use(func(c *gin.Context) {
			ginctx.SetClaims(c, &auth.AccessClaims{UserID: userID, IsAdmin: isAdmin})
			c.Next()
		})
	}
	domainRepo := newMockDomainRepo()
	zoneRepo := newMockDNSZoneRepo()
	recordRepo := newMockDNSRecordRepo()
	RegisterDNSRoutes(v1, DNSHandlerConfig{
		Domains:        domainRepo,
		Zones:          zoneRepo,
		Records:        recordRepo,
		ServerSettings: &fakeSettingsRepo{s: s},
		Reconciler:     &mockDNSReconciler{},
	})
	return r, domainRepo, zoneRepo
}

func TestSystemRecords_ReturnsSOAAndNSFromSettings(t *testing.T) {
	r, domainRepo, zoneRepo := dnsRouterWithSettings("user1", false, &models.ServerSettings{
		NS1Name:    "ns1.jabali.test",
		NS2Name:    "ns2.jabali.test",
		AdminEmail: "ops@example.com",
	})
	domainRepo.Create(context.Background(), &models.Domain{
		ID: "dom1", UserID: "user1", Name: "example.com",
	})
	zoneRepo.Create(context.Background(), &models.DNSZone{
		ID:         "zone1",
		DomainID:   "dom1",
		Name:       "example.com",
		MinimumTTL: 3600,
		IsEnabled:  true,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/domains/dom1/dns/system-records", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		SystemRecords []struct {
			Name    string `json:"name"`
			Type    string `json:"type"`
			Content string `json:"content"`
			TTL     int    `json:"ttl"`
		} `json:"system_records"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp.SystemRecords, 3) // SOA + 2 NS

	// SOA first, primary NS = ns1, hostmaster = ops.example.com.
	require.Equal(t, "SOA", resp.SystemRecords[0].Type)
	assert.Contains(t, resp.SystemRecords[0].Content, "ns1.jabali.test")
	assert.Contains(t, resp.SystemRecords[0].Content, "ops.example.com")

	// Two NS records — ns1 and ns2.
	nsContents := []string{resp.SystemRecords[1].Content, resp.SystemRecords[2].Content}
	assert.ElementsMatch(t, []string{"ns1.jabali.test", "ns2.jabali.test"}, nsContents)
}

func TestSystemRecords_ZoneNotProvisionedReturns404(t *testing.T) {
	r, domainRepo, _ := dnsRouterWithSettings("user1", false, &models.ServerSettings{})
	domainRepo.Create(context.Background(), &models.Domain{
		ID: "dom1", UserID: "user1", Name: "example.com",
	})
	// No zone.

	req := httptest.NewRequest(http.MethodGet, "/api/v1/domains/dom1/dns/system-records", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, "zone_not_provisioned", resp["error"])
}

func TestSystemRecords_NonOwnerForbidden(t *testing.T) {
	r, domainRepo, zoneRepo := dnsRouterWithSettings("other-user", false, &models.ServerSettings{
		NS1Name: "ns1.jabali.test",
	})
	domainRepo.Create(context.Background(), &models.Domain{
		ID: "dom1", UserID: "user1", Name: "example.com", // owned by user1
	})
	zoneRepo.Create(context.Background(), &models.DNSZone{
		ID: "zone1", DomainID: "dom1", Name: "example.com", MinimumTTL: 3600, IsEnabled: true,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/domains/dom1/dns/system-records", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
}

func (*mockDomainRepo) AttachDockerApp(context.Context, string, string, models.NginxRules) error {
	return nil
}
func (*mockDomainRepo) DetachDockerApp(context.Context, string, bool) error {
	return nil
}
