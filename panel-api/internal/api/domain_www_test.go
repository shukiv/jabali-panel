package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func wwwFixture(dom *models.Domain, zoneEnabled bool, existing ...models.DNSRecord) (*mockDNSZoneRepo, *mockDNSRecordRepo, *fakeSettingsRepo) {
	zones := newMockDNSZoneRepo()
	zones.zones["z1"] = &models.DNSZone{ID: "z1", DomainID: dom.ID, Name: dom.Name, IsEnabled: zoneEnabled}
	recs := newMockDNSRecordRepo()
	for i := range existing {
		r := existing[i]
		r.ZoneID = "z1"
		recs.records[r.ID] = &r
	}
	return zones, recs, &fakeSettingsRepo{s: &models.ServerSettings{PublicIPv4: "203.0.113.1", DefaultDNSTTL: 600}}
}

func TestEnsureWWWRecord_AddsTheBootstrapCNAME(t *testing.T) {
	dom := &models.Domain{ID: "d1", Name: "example.co.il"}
	zones, recs, srv := wwwFixture(dom, true)
	created, err := EnsureWWWRecord(context.Background(), zones, recs, srv, dom)
	if err != nil || !created {
		t.Fatalf("created=%v err=%v, want a new record", created, err)
	}
	if len(recs.records) != 1 {
		t.Fatalf("want 1 record, got %d", len(recs.records))
	}
	for _, r := range recs.records {
		if r.Name != "www" || r.Type != "CNAME" || r.Content != "example.co.il" || !r.Managed || !r.IsEnabled || r.TTL != 600 {
			t.Fatalf("want the bootstrap www CNAME, got %+v", r)
		}
	}
}

func TestEnsureWWWRecord_AddsNothing(t *testing.T) {
	served := &models.Domain{ID: "d1", Name: "example.com"}
	cases := map[string]struct {
		dom         *models.Domain
		zoneEnabled bool
		existing    []models.DNSRecord
	}{
		"www A exists":           {served, true, []models.DNSRecord{{ID: "r1", Name: "www", Type: "A", Content: "198.51.100.7"}}},
		"full-name www exists":   {served, true, []models.DNSRecord{{ID: "r1", Name: "WWW.Example.com.", Type: "CNAME", Content: "elsewhere.example."}}},
		"zone disabled":          {served, false, nil},
		"DNS hosted elsewhere":   {&models.Domain{ID: "d1", Name: "example.com", DNSDisabled: true}, true, nil},
		"web off":                {&models.Domain{ID: "d1", Name: "example.com", WebDisabled: true}, true, nil},
		"www.-prefixed domain":   {&models.Domain{ID: "d1", Name: "www.example.com"}, true, nil},
		"other names don't hide": {served, true, []models.DNSRecord{{ID: "r1", Name: "wwwx", Type: "A", Content: "198.51.100.7"}}},
	}
	for name, tc := range cases {
		zones, recs, srv := wwwFixture(tc.dom, tc.zoneEnabled, tc.existing...)
		before := len(recs.records)
		created, err := EnsureWWWRecord(context.Background(), zones, recs, srv, tc.dom)
		if err != nil {
			t.Errorf("%s: err %v", name, err)
		}
		want := name == "other names don't hide"
		if created != want || (len(recs.records) != before) == !want {
			t.Errorf("%s: created=%v records %d→%d, want created=%v", name, created, before, len(recs.records), want)
		}
	}
	// No zone at all (DNS elsewhere, never provisioned): nothing, no error.
	recs := newMockDNSRecordRepo()
	created, err := EnsureWWWRecord(context.Background(), newMockDNSZoneRepo(), recs, &fakeSettingsRepo{s: &models.ServerSettings{}}, served)
	if created || err != nil || len(recs.records) != 0 {
		t.Fatalf("no zone: created=%v err=%v", created, err)
	}
}

// create_www is settable after create, through its own writer: the general
// Update's column allowlist doesn't carry it, so a struct assignment alone
// would be dropped on the floor.
func TestDomainPatch_CreateWWW(t *testing.T) {
	dom := &models.Domain{ID: "d1", UserID: "u1", Name: "example.com", MailProvider: models.MailProviderJabali}
	r, repo := applyTxHarness(t, dom)

	w := patchDomainBody(r, "d1", `{"create_www":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		CreateWWW bool `json:"create_www"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(repo.createWWWCalls) != 1 || !repo.createWWWCalls[0] || !got.CreateWWW {
		t.Fatalf("want one UpdateCreateWWW(true) and create_www in the response, got calls %v body %s", repo.createWWWCalls, w.Body.String())
	}
	// Unchanged: no write.
	if w := patchDomainBody(r, "d1", `{"create_www":true}`); w.Code != http.StatusOK || len(repo.createWWWCalls) != 1 {
		t.Fatalf("unchanged value must not write again: code %d calls %v", w.Code, repo.createWWWCalls)
	}
	// Off again.
	if w := patchDomainBody(r, "d1", `{"create_www":false}`); w.Code != http.StatusOK || len(repo.createWWWCalls) != 2 || repo.createWWWCalls[1] {
		t.Fatalf("want UpdateCreateWWW(false): code %d calls %v", w.Code, repo.createWWWCalls)
	}
	// A PATCH without the field leaves it alone.
	if w := patchDomainBody(r, "d1", `{"index_priority":"php_first"}`); w.Code != http.StatusOK || len(repo.createWWWCalls) != 2 {
		t.Fatalf("absent field must not write: code %d calls %v", w.Code, repo.createWWWCalls)
	}
}
