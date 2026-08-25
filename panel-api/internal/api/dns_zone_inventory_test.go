package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Counting fakes for the JAB-377 zone-inventory query budget. Each embeds its
// repository interface so only the methods the handler calls are implemented;
// any other call would panic (guard against scope creep).

type countingDomainRepo struct {
	repository.DomainRepository
	listCalls       int
	listByUserCalls int
	domains         []models.Domain
}

func (c *countingDomainRepo) List(_ context.Context, _ repository.ListOptions) ([]models.Domain, int64, error) {
	c.listCalls++
	return c.domains, int64(len(c.domains)), nil
}
func (c *countingDomainRepo) ListByUserID(_ context.Context, _ string, _ repository.ListOptions) ([]models.Domain, int64, error) {
	c.listByUserCalls++
	return c.domains, int64(len(c.domains)), nil
}

type countingZoneRepo struct {
	repository.DNSZoneRepository
	calls int
}

func (c *countingZoneRepo) FindByDomainIDs(_ context.Context, ids []string) ([]models.DNSZone, error) {
	c.calls++
	out := make([]models.DNSZone, 0, len(ids))
	for i, id := range ids {
		out = append(out, models.DNSZone{ID: "z" + strconv.Itoa(i), DomainID: id})
	}
	return out, nil
}

type countingRecordRepo struct {
	repository.DNSRecordRepository
	calls int
}

func (c *countingRecordRepo) CountByZoneIDs(_ context.Context, zoneIDs []string) (map[string]int64, error) {
	c.calls++
	out := make(map[string]int64, len(zoneIDs))
	for _, z := range zoneIDs {
		out[z] = 3
	}
	return out, nil
}

type countingSettingsRepo struct {
	repository.ServerSettingsRepository
	calls int
}

func (c *countingSettingsRepo) Get(_ context.Context) (*models.ServerSettings, error) {
	c.calls++
	return &models.ServerSettings{DefaultDNSTTL: 300}, nil
}

type countingUserRepo struct {
	repository.UserRepository
	calls int
}

func (c *countingUserRepo) FindByIDs(_ context.Context, ids []string) ([]models.User, error) {
	c.calls++
	out := make([]models.User, 0, len(ids))
	for _, id := range ids {
		name := "owner-" + id
		out = append(out, models.User{ID: id, Username: &name})
	}
	return out, nil
}

func inventoryHandler(dr *countingDomainRepo) (*dnsHandler, *countingZoneRepo, *countingRecordRepo, *countingSettingsRepo, *countingUserRepo) {
	zr := &countingZoneRepo{}
	rr := &countingRecordRepo{}
	sr := &countingSettingsRepo{}
	ur := &countingUserRepo{}
	h := &dnsHandler{cfg: DNSHandlerConfig{Domains: dr, Zones: zr, Records: rr, ServerSettings: sr, Users: ur}}
	return h, zr, rr, sr, ur
}

func invGet(h *dnsHandler, url, userID string, isAdmin bool) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, url, nil)
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: userID, IsAdmin: isAdmin})
	h.listZoneInventory(c)
	return w
}

// The query budget is bounded and independent of page size: one domain list,
// one zones-by-domain read, one COUNT(*) GROUP BY aggregate, one settings read,
// one owner-username batch — the same counts at 1, 10, and 100 rows (JAB-377).
func TestZoneInventory_ConstantQueryBudget(t *testing.T) {
	for _, n := range []int{1, 10, 100} {
		doms := make([]models.Domain, n)
		for i := range doms {
			doms[i] = models.Domain{ID: "d" + strconv.Itoa(i), Name: fmt.Sprintf("ex%d.com", i), UserID: "u1"}
		}
		dr := &countingDomainRepo{domains: doms}
		h, zr, rr, sr, ur := inventoryHandler(dr)

		w := invGet(h, "/dns/zones?page=1&page_size=200", "admin", true)

		require.Equal(t, http.StatusOK, w.Code, "n=%d body=%s", n, w.Body.String())
		require.Equal(t, 1, dr.listCalls, "domain list exactly once (n=%d)", n)
		require.Equal(t, 1, zr.calls, "zones batched exactly once (n=%d)", n)
		require.Equal(t, 1, rr.calls, "record counts aggregated exactly once (n=%d)", n)
		require.Equal(t, 1, sr.calls, "settings/effective-TTL read exactly once (n=%d)", n)
		require.Equal(t, 1, ur.calls, "owner usernames batched exactly once (n=%d)", n)

		var resp struct {
			Data  []dnsZoneInventoryRow `json:"data"`
			Total int64                 `json:"total"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Len(t, resp.Data, n)
		require.Equal(t, int64(n), resp.Total)
		require.True(t, resp.Data[0].Provisioned)
		require.EqualValues(t, 3, resp.Data[0].RecordCount)
		require.Equal(t, 300, resp.Data[0].EffectiveTTL)
		require.NotNil(t, resp.Data[0].Username, "admin scope carries owner username")
	}
}

// A tenant is always owner-scoped and cannot select another owner: the handler
// ignores ?user_id for non-admins and never issues the all-domains List. Owner
// username is admin-only.
func TestZoneInventory_TenantScopedCannotSelectOwner(t *testing.T) {
	dr := &countingDomainRepo{domains: []models.Domain{{ID: "d1", Name: "t.com", UserID: "u1"}}}
	h, _, _, _, ur := inventoryHandler(dr)

	w := invGet(h, "/dns/zones?user_id=someone-else", "u1", false)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 0, dr.listCalls, "tenant must never call the all-domains List")
	require.Equal(t, 1, dr.listByUserCalls, "tenant scoped via ListByUserID")
	require.Equal(t, 0, ur.calls, "owner-username enrich is admin-only")

	var resp struct {
		Data []dnsZoneInventoryRow `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1)
	require.Nil(t, resp.Data[0].Username, "tenant scope must not expose owner username")
}

// A not-yet-provisioned domain (no zone row) reports provisioned=false with a
// zero record count — distinct from an error (which is a 500, never rendered as
// "not provisioned").
func TestZoneInventory_NotProvisioned(t *testing.T) {
	dr := &countingDomainRepo{domains: []models.Domain{{ID: "d1", Name: "np.com", UserID: "u1"}}}
	zr := &noZoneRepo{}
	h := &dnsHandler{cfg: DNSHandlerConfig{Domains: dr, Zones: zr, Records: &countingRecordRepo{}, ServerSettings: &countingSettingsRepo{}}}

	w := invGet(h, "/dns/zones", "admin", true)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp struct {
		Data []dnsZoneInventoryRow `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1)
	require.False(t, resp.Data[0].Provisioned)
	require.EqualValues(t, 0, resp.Data[0].RecordCount)
}

type noZoneRepo struct {
	repository.DNSZoneRepository
}

func (noZoneRepo) FindByDomainIDs(_ context.Context, _ []string) ([]models.DNSZone, error) {
	return nil, nil
}
