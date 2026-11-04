package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #2377: the DNS Zones page said every migrated zone was in place while
// PowerDNS had none of them, because "provisioned" only meant a dns_zones row
// existed. push_status says whether the DNS server accepted the zone.

const pushTestErr = "powerdns: Error 1045 (28000): Access denied for user 'pdns'@'localhost'"

type pushStateZoneRepo struct {
	repository.DNSZoneRepository
}

func (pushStateZoneRepo) FindByDomainIDs(_ context.Context, _ []string) ([]models.DNSZone, error) {
	at := time.Date(2026, 11, 4, 18, 0, 0, 0, time.UTC)
	failed := pushTestErr
	return []models.DNSZone{
		{ID: "z1", DomainID: "d1", LastPushError: &failed},                    // never reached the server
		{ID: "z2", DomainID: "d2", LastPushedAt: &at},                         // on the server
		{ID: "z3", DomainID: "d3"},                                            // no push recorded yet
		{ID: "z4", DomainID: "d4", LastPushedAt: &at, LastPushError: &failed}, // older copy on the server
	}, nil
}

func pushStatusGet(t *testing.T, userID string, isAdmin bool) (map[string]dnsZoneInventoryRow, string) {
	t.Helper()
	dr := &countingDomainRepo{domains: []models.Domain{
		{ID: "d1", Name: "failed.example", UserID: "u1"},
		{ID: "d2", Name: "pushed.example", UserID: "u1"},
		{ID: "d3", Name: "pending.example", UserID: "u1"},
		{ID: "d4", Name: "stale.example", UserID: "u1"},
		{ID: "d5", Name: "nozone.example", UserID: "u1"},
	}}
	h := &dnsHandler{cfg: DNSHandlerConfig{Domains: dr, Zones: pushStateZoneRepo{}, Records: &countingRecordRepo{}, ServerSettings: &countingSettingsRepo{}}}

	w := invGet(h, "/dns/zones", userID, isAdmin)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp struct {
		Data []dnsZoneInventoryRow `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	byName := map[string]dnsZoneInventoryRow{}
	for _, row := range resp.Data {
		byName[row.Name] = row
	}
	return byName, w.Body.String()
}

func TestZoneInventory_PushStatus(t *testing.T) {
	rows, _ := pushStatusGet(t, "admin", true)

	require.Equal(t, "failed", rows["failed.example"].PushStatus)
	require.Nil(t, rows["failed.example"].LastPushedAt)
	require.Equal(t, "on_server", rows["pushed.example"].PushStatus)
	require.NotNil(t, rows["pushed.example"].LastPushedAt)
	require.Equal(t, "pending", rows["pending.example"].PushStatus)
	require.Equal(t, "failed", rows["stale.example"].PushStatus, "a failed re-push is a failure even with an older copy on the server")
	require.NotNil(t, rows["stale.example"].LastPushedAt, "the older copy's time is still reported")

	require.False(t, rows["nozone.example"].Provisioned)
	require.Empty(t, rows["nozone.example"].PushStatus, "no zone row, no push status")
}

func TestZoneInventory_PushErrorIsAdminOnly(t *testing.T) {
	admin, _ := pushStatusGet(t, "admin", true)
	require.Equal(t, pushTestErr, admin["failed.example"].LastPushError, "admins see why the push failed")

	tenant, body := pushStatusGet(t, "u1", false)
	require.Equal(t, "failed", tenant["failed.example"].PushStatus, "tenants still see that it failed")
	require.Empty(t, tenant["failed.example"].LastPushError)
	require.False(t, strings.Contains(body, "Access denied"), "raw agent output must not reach a tenant: %s", body)
	require.False(t, strings.Contains(body, "last_push_error"), "no last_push_error key in tenant scope")
}
