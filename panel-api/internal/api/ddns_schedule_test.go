package api

// JAB-371 regression: a successful DDNS update must schedule the OWNING DOMAIN
// id, not the dns_zones id. Passing the zone id made Reconciler.ReconcileOne
// (which looks the value up as a domain) fail to resolve, so the change only
// landed on the next full periodic sweep.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// fakeDDNSTokens returns one active, full-scope token for any hash.
type fakeDDNSTokens struct{ tok *models.UserAPIToken }

func (f *fakeDDNSTokens) FindByHash(_ context.Context, _ string) (*models.UserAPIToken, error) {
	return f.tok, nil
}
func (f *fakeDDNSTokens) Create(context.Context, *models.UserAPIToken) error { return nil }
func (f *fakeDDNSTokens) ListByUser(context.Context, string) ([]models.UserAPIToken, error) {
	return nil, nil
}
func (f *fakeDDNSTokens) FindByID(context.Context, string) (*models.UserAPIToken, error) {
	return f.tok, nil
}
func (f *fakeDDNSTokens) Revoke(context.Context, string, time.Time) error               { return nil }
func (f *fakeDDNSTokens) BumpLastUsed(context.Context, string, string, time.Time) error { return nil }

func TestDDNSUpdate_SchedulesOwningDomainNotZone(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const (
		userID   = "01HUSER00000000000000000A"
		domainID = "01HDOMAIN0000000000000000"
		zoneID   = "01HZONE000000000000000000"
		recordID = "01HRECORD00000000000000AA"
	)

	domains := newMockDomainRepo()
	domains.listByUserResult = []models.Domain{{ID: domainID, Name: "example.com", UserID: userID}}

	zones := newMockDNSZoneRepo()
	zones.zones[zoneID] = &models.DNSZone{ID: zoneID, DomainID: domainID, Name: "example.com"}

	records := newMockDNSRecordRepo()
	records.records[recordID] = &models.DNSRecord{
		ID: recordID, ZoneID: zoneID, Name: "vpn", Type: "A", Content: "1.1.1.1",
	}

	sched := &mockDNSReconciler{}
	tokens := &fakeDDNSTokens{tok: &models.UserAPIToken{ID: "tok-1", UserID: userID}}

	r := gin.New()
	RegisterDDNSRoutes(r, DDNSConfig{
		Tokens:     tokens,
		Domains:    domains,
		Zones:      zones,
		Records:    records,
		Reconciler: sched,
	})

	req := httptest.NewRequest(http.MethodGet, "/nic/update?hostname=vpn.example.com&myip=2.2.2.2", nil)
	req.SetBasicAuth("ddns", "jat_testsecret_value")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q; want 200 good", w.Code, w.Body.String())
	}
	if len(sched.scheduled) != 1 {
		t.Fatalf("scheduled = %v; want exactly one entry", sched.scheduled)
	}
	got := sched.scheduled[0]
	if got == zoneID {
		t.Fatalf("scheduled the ZONE id %q — JAB-371 regression (must schedule the domain)", got)
	}
	if got != domainID {
		t.Fatalf("scheduled %q; want the owning domain id %q", got, domainID)
	}
}

// A no-change response must not schedule any work.
func TestDDNSUpdate_NoChangeSchedulesNothing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const (
		userID   = "01HUSER00000000000000000A"
		domainID = "01HDOMAIN0000000000000000"
		zoneID   = "01HZONE000000000000000000"
		recordID = "01HRECORD00000000000000AA"
	)
	domains := newMockDomainRepo()
	domains.listByUserResult = []models.Domain{{ID: domainID, Name: "example.com", UserID: userID}}
	zones := newMockDNSZoneRepo()
	zones.zones[zoneID] = &models.DNSZone{ID: zoneID, DomainID: domainID, Name: "example.com"}
	records := newMockDNSRecordRepo()
	records.records[recordID] = &models.DNSRecord{ID: recordID, ZoneID: zoneID, Name: "vpn", Type: "A", Content: "2.2.2.2"}
	sched := &mockDNSReconciler{}
	tokens := &fakeDDNSTokens{tok: &models.UserAPIToken{ID: "tok-1", UserID: userID}}

	r := gin.New()
	RegisterDDNSRoutes(r, DDNSConfig{Tokens: tokens, Domains: domains, Zones: zones, Records: records, Reconciler: sched})

	req := httptest.NewRequest(http.MethodGet, "/nic/update?hostname=vpn.example.com&myip=2.2.2.2", nil)
	req.SetBasicAuth("ddns", "jat_testsecret_value")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if len(sched.scheduled) != 0 {
		t.Fatalf("no-change update scheduled %v; want nothing", sched.scheduled)
	}
}
