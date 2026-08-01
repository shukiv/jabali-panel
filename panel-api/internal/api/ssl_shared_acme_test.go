package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type acmeTestDomainRepo struct {
	repository.DomainRepository
	byID map[string]*models.Domain
}

func (r *acmeTestDomainRepo) FindByID(_ context.Context, id string) (*models.Domain, error) {
	if d, ok := r.byID[id]; ok {
		return d, nil
	}
	return nil, repository.ErrNotFound
}

type acmeTestZoneRepo struct {
	repository.DNSZoneRepository
	byDomainID map[string]*models.DNSZone
}

func (r *acmeTestZoneRepo) FindByDomainID(_ context.Context, domainID string) (*models.DNSZone, error) {
	if z, ok := r.byDomainID[domainID]; ok {
		return z, nil
	}
	return nil, repository.ErrNotFound
}

type acmeTestSettingsRepo struct {
	repository.ServerSettingsRepository
	srv *models.ServerSettings
}

func (r *acmeTestSettingsRepo) Get(_ context.Context) (*models.ServerSettings, error) {
	return r.srv, nil
}

func acmeTestHandler(zones repository.DNSZoneRepository, shared *fakeSharedRepo) *sslHandler {
	return &sslHandler{cfg: SSLHandlerConfig{
		Domains: &acmeTestDomainRepo{byID: map[string]*models.Domain{
			"D1": {ID: "D1", Name: "example.com", UserID: "U1"},
		}},
		SharedCerts:    shared,
		DNSZones:       zones,
		Agent:          &fakeSharedAgent{},
		ServerSettings: &acmeTestSettingsRepo{srv: &models.ServerSettings{AdminEmail: "admin@host.tld"}},
	}}
}

func postAcme(t *testing.T, h *sslHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/api/v1/admin/certificates/shared/acme", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	h.requestAcmeSharedCert(c)
	return w
}

func TestRequestAcmeSharedCert_CreatesPendingRow(t *testing.T) {
	shared := &fakeSharedRepo{}
	zones := &acmeTestZoneRepo{byDomainID: map[string]*models.DNSZone{
		"D1": {ID: "Z1", DomainID: "D1", Name: "example.com", IsEnabled: true},
	}}
	w := postAcme(t, acmeTestHandler(zones, shared), `{"domain_id":"D1"}`)

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	require.Len(t, shared.created, 1)
	row := shared.created[0]
	require.Equal(t, models.SharedCertSourceACME, row.Source)
	require.Equal(t, "*.example.com", row.Name)
	require.NotNil(t, row.UserID)
	require.Equal(t, "U1", *row.UserID)
	require.Nil(t, row.CertPath, "row must be pending — issuance happens on the reconcile tick")

	var sans []string
	require.NotNil(t, row.SANs)
	require.NoError(t, json.Unmarshal([]byte(*row.SANs), &sans))
	require.Equal(t, []string{"example.com", "*.example.com"}, sans)
}

func TestRequestAcmeSharedCert_RefusesForeignZone(t *testing.T) {
	shared := &fakeSharedRepo{}
	zones := &acmeTestZoneRepo{byDomainID: map[string]*models.DNSZone{}}
	w := postAcme(t, acmeTestHandler(zones, shared), `{"domain_id":"D1"}`)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "dns_zone_not_local")
	require.Empty(t, shared.created)
}

func TestRequestAcmeSharedCert_NilZonesRepoDegrades(t *testing.T) {
	shared := &fakeSharedRepo{}
	w := postAcme(t, acmeTestHandler(nil, shared), `{"domain_id":"D1"}`)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestRequestAcmeSharedCert_RequiresAdminEmail(t *testing.T) {
	shared := &fakeSharedRepo{}
	zones := &acmeTestZoneRepo{byDomainID: map[string]*models.DNSZone{
		"D1": {ID: "Z1", DomainID: "D1", Name: "example.com", IsEnabled: true},
	}}
	h := acmeTestHandler(zones, shared)
	h.cfg.ServerSettings = &acmeTestSettingsRepo{srv: &models.ServerSettings{}}
	w := postAcme(t, h, `{"domain_id":"D1"}`)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	require.Contains(t, w.Body.String(), "admin_email_required")
}
