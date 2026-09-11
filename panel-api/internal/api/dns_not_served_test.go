package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// GH #1622: the record create/update handlers only checked that a zone ROW
// existed, so they accepted records for a zone the reconciler never pushes to
// PowerDNS (domain DNS-disabled, or the zone disabled) — the tenant saw the row
// "added but not resolvable." These tests pin the fail-closed guard. They use an
// admin so the per-type policy gate (GH #466) is bypassed and only the
// not-served guard is under test.

// seedZoneWith provisions the domain + zone with explicit DNS-disabled / zone-
// enabled flags so the not-served guard can be exercised.
func seedZoneWith(t *testing.T, dr *mockDomainRepo, zr *mockDNSZoneRepo, owner string, dnsDisabled, zoneEnabled bool) {
	t.Helper()
	dr.Create(context.Background(), &models.Domain{
		ID: "test-domain-id", UserID: owner, Name: "example.com", DNSDisabled: dnsDisabled,
	})
	zr.Create(context.Background(), &models.DNSZone{
		ID: ids.NewULID(), DomainID: "test-domain-id", Name: "example.com",
		Serial: 1, RefreshSeconds: 3600, RetrySeconds: 600, ExpireSeconds: 604800,
		MinimumTTL: 3600, IsEnabled: zoneEnabled, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
}

func TestCreateRecord_RefusesDNSDisabledDomain(t *testing.T) {
	r, dr, zr := dnsRouterWithSettings("admin1", true, &models.ServerSettings{ID: 1})
	seedZoneWith(t, dr, zr, "user1", true /*dnsDisabled*/, true /*zoneEnabled*/)
	w := postRecord(t, r, "A", "192.0.2.1")
	require.Equal(t, http.StatusConflict, w.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "dns_not_served", resp["error"])
}

func TestCreateRecord_RefusesDisabledZone(t *testing.T) {
	r, dr, zr := dnsRouterWithSettings("admin1", true, &models.ServerSettings{ID: 1})
	seedZoneWith(t, dr, zr, "user1", false /*dnsDisabled*/, false /*zoneEnabled*/)
	w := postRecord(t, r, "A", "192.0.2.1")
	require.Equal(t, http.StatusConflict, w.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "dns_not_served", resp["error"])
}

// Control: a served zone still accepts the record — guards against the fix
// over-blocking a normal domain.
func TestCreateRecord_AllowsServedZone(t *testing.T) {
	r, dr, zr := dnsRouterWithSettings("admin1", true, &models.ServerSettings{ID: 1})
	seedZoneWith(t, dr, zr, "user1", false, true)
	w := postRecord(t, r, "A", "192.0.2.1")
	require.Equal(t, http.StatusCreated, w.Code)
}
