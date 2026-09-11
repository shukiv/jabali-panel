package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Fakes for the GH #1611 DNS-zone delete. Each embeds its repository interface so
// only the methods deleteZone/tearDownDNSFacet touch are implemented; any other
// call panics (guards against silent scope creep).

type dzDomainRepo struct {
	repository.DomainRepository
	dom       *models.Domain
	flipCalls int
	flipErr   error
	flippedTo bool
	notFound  bool
}

func (r *dzDomainRepo) FindByID(_ context.Context, id string) (*models.Domain, error) {
	if r.notFound || r.dom == nil || r.dom.ID != id {
		return nil, repository.ErrNotFound
	}
	return r.dom, nil
}
func (r *dzDomainRepo) UpdateDNSDisabled(_ context.Context, _ string, disabled bool) error {
	r.flipCalls++
	if r.flipErr != nil {
		return r.flipErr
	}
	r.flippedTo = disabled
	return nil
}

type dzZoneRepo struct {
	repository.DNSZoneRepository
	zone        *models.DNSZone
	deletedZone string
}

func (r *dzZoneRepo) FindByDomainID(_ context.Context, domainID string) (*models.DNSZone, error) {
	if r.zone == nil || r.zone.DomainID != domainID {
		return nil, repository.ErrNotFound
	}
	return r.zone, nil
}
func (r *dzZoneRepo) Delete(_ context.Context, id string) error {
	r.deletedZone = id
	return nil
}

type dzRecordRepo struct {
	repository.DNSRecordRepository
	deletedByZone string
}

func (r *dzRecordRepo) DeleteByZoneID(_ context.Context, zoneID string) error {
	r.deletedByZone = zoneID
	return nil
}

func dzDo(h *dnsHandler, id, userID string, isAdmin bool) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodDelete, "/api/v1/domains/"+id+"/dns/zone", nil)
	c.Params = gin.Params{{Key: "id", Value: id}}
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: userID, IsAdmin: isAdmin})
	h.deleteZone(c)
	return w
}

func dzHandler(dom *models.Domain, zone *models.DNSZone) (*dnsHandler, *dzDomainRepo, *dzZoneRepo, *dzRecordRepo, *mockAgent) {
	dr := &dzDomainRepo{dom: dom}
	zr := &dzZoneRepo{zone: zone}
	rr := &dzRecordRepo{}
	ag := &mockAgent{}
	h := &dnsHandler{cfg: DNSHandlerConfig{Domains: dr, Zones: zr, Records: rr, Agent: ag}}
	return h, dr, zr, rr, ag
}

func TestDeleteZone_HappyPath_KeepsWebMail(t *testing.T) {
	dom := &models.Domain{ID: "d1", Name: "ex.com", UserID: "u1", EmailEnabled: true}
	zone := &models.DNSZone{ID: "z1", DomainID: "d1"}
	h, dr, zr, rr, ag := dzHandler(dom, zone)

	w := dzDo(h, "d1", "u1", false)
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Kept struct {
			Web  bool `json:"web"`
			Mail bool `json:"mail"`
		} `json:"kept"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Kept.Web, "web kept")
	assert.True(t, resp.Kept.Mail, "mail kept")

	assert.Equal(t, 1, dr.flipCalls, "dns_disabled flipped once")
	assert.True(t, dr.flippedTo, "flipped to disabled")
	assert.Equal(t, "dns.zone.delete", ag.lastCommand, "pdns zone deleted")
	assert.Equal(t, 1, ag.callCount)
	assert.Equal(t, "z1", rr.deletedByZone, "records cleared")
	assert.Equal(t, "z1", zr.deletedZone, "zone row cleared")
}

func TestDeleteZone_PanelPrimary_Refused(t *testing.T) {
	dom := &models.Domain{ID: "d1", Name: "panel.example", UserID: "u1", IsPanelPrimary: true}
	h, dr, _, _, ag := dzHandler(dom, nil)
	w := dzDo(h, "d1", "u1", true)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "panel_primary_protected")
	assert.Equal(t, 0, dr.flipCalls, "no flip on refusal")
	assert.Equal(t, 0, ag.callCount, "no teardown on refusal")
}

func TestDeleteZone_DNSSEC_Refused(t *testing.T) {
	dom := &models.Domain{ID: "d1", Name: "ex.com", UserID: "u1", EmailEnabled: true, DNSSECEnabled: true}
	h, dr, _, _, ag := dzHandler(dom, nil)
	w := dzDo(h, "d1", "u1", false)
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "dnssec_enabled")
	assert.Equal(t, 0, dr.flipCalls)
	assert.Equal(t, 0, ag.callCount)
}

func TestDeleteZone_LastFacet_Refused(t *testing.T) {
	// web off + mail off → DNS is the only facet → refuse (delete the domain).
	dom := &models.Domain{ID: "d1", Name: "ex.com", UserID: "u1", WebDisabled: true, EmailEnabled: false}
	h, dr, _, _, ag := dzHandler(dom, nil)
	w := dzDo(h, "d1", "u1", false)
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "last_facet")
	assert.Equal(t, 0, dr.flipCalls)
	assert.Equal(t, 0, ag.callCount)
}

func TestDeleteZone_NonOwnerTenant_Forbidden(t *testing.T) {
	dom := &models.Domain{ID: "d1", Name: "ex.com", UserID: "owner", EmailEnabled: true}
	h, dr, _, _, ag := dzHandler(dom, nil)
	w := dzDo(h, "d1", "someone-else", false)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "forbidden")
	assert.Equal(t, 0, dr.flipCalls)
	assert.Equal(t, 0, ag.callCount)
}

func TestDeleteZone_NotFound(t *testing.T) {
	h, _, _, _, ag := dzHandler(nil, nil)
	w := dzDo(h, "nope", "u1", true)
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, 0, ag.callCount)
}

func TestDeleteZone_FlipFail_500_NoTeardown(t *testing.T) {
	// The flip failing means dns_disabled is still 0 → the reconciler still owns
	// the zone; tearDownDNSFacet must NOT delete it, and the caller returns 500.
	dom := &models.Domain{ID: "d1", Name: "ex.com", UserID: "u1", EmailEnabled: true}
	zone := &models.DNSZone{ID: "z1", DomainID: "d1"}
	h, dr, zr, rr, ag := dzHandler(dom, zone)
	dr.flipErr = assert.AnError
	w := dzDo(h, "d1", "u1", false)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, 1, dr.flipCalls, "flip attempted")
	assert.Equal(t, 0, ag.callCount, "no pdns delete after a failed flip")
	assert.Empty(t, zr.deletedZone, "zone row untouched")
	assert.Empty(t, rr.deletedByZone, "records untouched")
}

func TestDeleteZone_AgentNil_503(t *testing.T) {
	dom := &models.Domain{ID: "d1", Name: "ex.com", UserID: "u1", EmailEnabled: true}
	h := &dnsHandler{cfg: DNSHandlerConfig{Domains: &dzDomainRepo{dom: dom}}}
	w := dzDo(h, "d1", "u1", false)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "dns_teardown_unavailable")
}

func TestDeleteZone_TenantRestrictedByPolicy(t *testing.T) {
	// GH #466: an admin has locked tenant record deletes (locked-down denies
	// delete on MX/TXT/SRV/CAA). A non-admin owner must not drop the whole zone —
	// that would delete the very records they're barred from deleting — but an
	// admin still bypasses the matrix.
	locked, _ := models.DNSPolicyPreset("locked-down")
	settings := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1, DNSUserRecordPolicy: locked}}

	newH := func() (*dnsHandler, *dzDomainRepo, *mockAgent) {
		dom := &models.Domain{ID: "d1", Name: "ex.com", UserID: "u1", EmailEnabled: true}
		dr := &dzDomainRepo{dom: dom}
		ag := &mockAgent{}
		h := &dnsHandler{cfg: DNSHandlerConfig{
			Domains: dr, Zones: &dzZoneRepo{}, Records: &dzRecordRepo{}, Agent: ag, ServerSettings: settings,
		}}
		return h, dr, ag
	}

	t.Run("non-admin owner forbidden", func(t *testing.T) {
		h, dr, ag := newH()
		w := dzDo(h, "d1", "u1", false)
		assert.Equal(t, http.StatusForbidden, w.Code)
		assert.Contains(t, w.Body.String(), "record_type_forbidden")
		assert.Equal(t, 0, dr.flipCalls, "no flip when policy forbids")
		assert.Equal(t, 0, ag.callCount, "no teardown when policy forbids")
	})

	t.Run("admin bypasses the matrix", func(t *testing.T) {
		h, dr, ag := newH()
		w := dzDo(h, "d1", "admin", true)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, 1, dr.flipCalls, "admin tears down despite the locked policy")
		assert.Equal(t, 1, ag.callCount)
	})
}

func TestDeleteZone_AlreadyDisabled_Idempotent(t *testing.T) {
	// A retry after a partial run: dns_disabled is already true and no zone row
	// remains. The transition guards are skipped; tearDownDNSFacet re-flips
	// (idempotent), still calls the agent, finds no row to clean, returns 200.
	dom := &models.Domain{ID: "d1", Name: "ex.com", UserID: "u1", EmailEnabled: true, DNSDisabled: true}
	h, dr, _, _, ag := dzHandler(dom, nil) // nil zone → FindByDomainID ErrNotFound
	w := dzDo(h, "d1", "u1", false)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 1, dr.flipCalls, "re-flip is idempotent")
	assert.Equal(t, 1, ag.callCount, "pdns delete still attempted")
}

// --- GH #1611: re-enable DNS management (POST /domains/:id/dns/zone) ---

func ezDo(h *dnsHandler, id, userID string, isAdmin bool) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/domains/"+id+"/dns/zone", nil)
	c.Params = gin.Params{{Key: "id", Value: id}}
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: userID, IsAdmin: isAdmin})
	h.enableZone(c)
	return w
}

func TestEnableZone_Flips_And_Returns200(t *testing.T) {
	dom := &models.Domain{ID: "d1", Name: "ex.com", UserID: "u1", DNSDisabled: true}
	dr := &dzDomainRepo{dom: dom}
	// No agent needed for enable — the reconciler re-creates the zone.
	h := &dnsHandler{cfg: DNSHandlerConfig{Domains: dr}}

	w := ezDo(h, "d1", "u1", false)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "dns_enabled")
	assert.Equal(t, 1, dr.flipCalls, "flipped once")
	assert.False(t, dr.flippedTo, "flipped to enabled (dns_disabled=false)")
}

func TestEnableZone_AlreadyEnabled_Idempotent(t *testing.T) {
	dom := &models.Domain{ID: "d1", Name: "ex.com", UserID: "u1", DNSDisabled: false}
	dr := &dzDomainRepo{dom: dom}
	h := &dnsHandler{cfg: DNSHandlerConfig{Domains: dr}}

	w := ezDo(h, "d1", "u1", false)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 0, dr.flipCalls, "no write when DNS already enabled")
}

func TestEnableZone_NonOwner_Forbidden(t *testing.T) {
	dom := &models.Domain{ID: "d1", Name: "ex.com", UserID: "owner", DNSDisabled: true}
	dr := &dzDomainRepo{dom: dom}
	h := &dnsHandler{cfg: DNSHandlerConfig{Domains: dr}}

	w := ezDo(h, "d1", "someone-else", false)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, 0, dr.flipCalls, "no flip for a non-owner")
}

func TestEnableZone_NotFound(t *testing.T) {
	dr := &dzDomainRepo{notFound: true}
	h := &dnsHandler{cfg: DNSHandlerConfig{Domains: dr}}
	w := ezDo(h, "nope", "u1", true)
	assert.Equal(t, http.StatusNotFound, w.Code)
}
