package hostedsvc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// zonePager serves /zones in pages of the given zones, one per page.
func zonePager(t *testing.T, zones []cfZone, totalPages int, queries *[]string) *CloudflareDNS {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		*queries = append(*queries, r.URL.RawQuery)
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		zr := cfZoneListResp{Success: true}
		if page >= 1 && page <= len(zones) {
			zr.Result = []cfZone{zones[page-1]}
		}
		zr.ResultInfo.TotalPages = totalPages
		_ = json.NewEncoder(w).Encode(zr)
	}))
	t.Cleanup(srv.Close)
	c := NewCloudflareAPI("test-token")
	c.api = srv.URL
	return c
}

// GH #2032: Verify & Save probes the tenant's zones the token can see, so
// a zone on a later page must not be missed, and only active zones count
// (FindZoneID, which routes issuance, also asks for status=active).
func TestCloudflareDNS_ListZones(t *testing.T) {
	var queries []string
	c := zonePager(t, []cfZone{{ID: "z1", Name: "a.com"}, {ID: "z2", Name: "b.org"}}, 2, &queries)
	zones, err := c.ListZones(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(zones) != 2 || zones[0] != (CloudflareZone{ID: "z1", Name: "a.com"}) || zones[1] != (CloudflareZone{ID: "z2", Name: "b.org"}) {
		t.Fatalf("zones = %+v", zones)
	}
	if len(queries) != 2 {
		t.Fatalf("queries = %v, want 2 pages", queries)
	}
	for _, q := range queries {
		if !strings.Contains(q, "status=active") {
			t.Errorf("query %q does not ask for active zones", q)
		}
	}
}

// A token that sees more zones than the cap is refused rather than read
// partly: a tenant's token should be scoped to the tenant's own zones.
func TestCloudflareDNS_ListZonesTooMany(t *testing.T) {
	var queries []string
	c := zonePager(t, nil, cfListZonesMaxPages+1, &queries)
	if _, err := c.ListZones(context.Background()); err == nil {
		t.Fatal("want an error for a token that sees too many zones")
	}
	if len(queries) > cfListZonesMaxPages {
		t.Fatalf("read %d pages, cap is %d", len(queries), cfListZonesMaxPages)
	}
}

// Cloudflare answers verify for a disabled or expired token with
// success:true and the status in result: only "active" may be saved.
func TestCloudflareDNS_VerifyTokenStatus(t *testing.T) {
	for status, wantErr := range map[string]bool{"active": false, "disabled": true, "expired": true} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/user/tokens/verify" {
				_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":{"id":"x","status":"` + status + `"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":[],"result_info":{"total_count":3}}`))
		}))
		c := NewCloudflareAPI("test-token")
		c.api = srv.URL
		_, err := c.VerifyToken(context.Background())
		srv.Close()
		if (err != nil) != wantErr {
			t.Errorf("status %q: err = %v, want error %v", status, err, wantErr)
		}
		if err != nil && !strings.Contains(err.Error(), status) {
			t.Errorf("status %q: error %q does not say why", status, err)
		}
	}
}
