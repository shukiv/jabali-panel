package hostedsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeCF is a minimal Cloudflare DNS API: an in-memory record set with
// list-by-name+type, create, put, and delete.
type fakeCF struct {
	mu      sync.Mutex
	records map[string]cfRecord // id -> record
	zones   map[string]string   // zone name -> id (JAB-235 account-level API)
	nextID  int
	writes  []cfRecord // every created/updated record, in order
}

func newFakeCF(t *testing.T) (*httptest.Server, *fakeCF) {
	f := &fakeCF{records: map[string]cfRecord{}, zones: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")

		// Account-level endpoints (JAB-235): token verify + zone list.
		if r.URL.Path == "/user/tokens/verify" {
			json.NewEncoder(w).Encode(cfWriteResp{Success: true})
			return
		}
		if r.URL.Path == "/zones" {
			var out []cfZone
			if name := r.URL.Query().Get("name"); name != "" {
				if id, ok := f.zones[name]; ok {
					out = append(out, cfZone{ID: id, Name: name})
				}
			}
			zr := cfZoneListResp{Success: true, Result: out}
			zr.ResultInfo.TotalCount = len(f.zones)
			json.NewEncoder(w).Encode(zr)
			return
		}

		// /zones/{zone}/dns_records[/{id}]
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		switch {
		case r.Method == http.MethodGet:
			name := r.URL.Query().Get("name")
			rtype := r.URL.Query().Get("type")
			var out []cfRecord
			for _, rec := range f.records {
				if rec.Name == name && rec.Type == rtype {
					out = append(out, rec)
				}
			}
			json.NewEncoder(w).Encode(cfListResp{Success: true, Result: out})
		case r.Method == http.MethodPost:
			var rec cfRecord
			json.NewDecoder(r.Body).Decode(&rec)
			f.nextID++
			rec.ID = jsonID(f.nextID)
			f.records[rec.ID] = rec
			f.writes = append(f.writes, rec)
			json.NewEncoder(w).Encode(cfWriteResp{Success: true})
		case r.Method == http.MethodPut:
			id := parts[len(parts)-1]
			var rec cfRecord
			json.NewDecoder(r.Body).Decode(&rec)
			rec.ID = id
			f.records[id] = rec
			f.writes = append(f.writes, rec)
			json.NewEncoder(w).Encode(cfWriteResp{Success: true})
		case r.Method == http.MethodDelete:
			id := parts[len(parts)-1]
			delete(f.records, id)
			json.NewEncoder(w).Encode(cfWriteResp{Success: true})
		}
	}))
	t.Cleanup(srv.Close)
	return srv, f
}

func jsonID(n int) string { return "rec" + string(rune('0'+n)) }

func newTestCFDNS(t *testing.T) (*CloudflareDNS, *fakeCF) {
	srv, f := newFakeCF(t)
	return &CloudflareDNS{Token: "test-token", ZoneID: "zone1", HTTP: srv.Client(), api: srv.URL}, f
}

func (f *fakeCF) byName(name, rtype string) (cfRecord, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, rec := range f.records {
		if rec.Name == name && rec.Type == rtype {
			return rec, true
		}
	}
	return cfRecord{}, false
}

func TestCloudflareDNS_EnsureA_NeverProxied(t *testing.T) {
	c, f := newTestCFDNS(t)
	ctx := context.Background()
	if err := c.EnsureA(ctx, "192-0-2-7", "192.0.2.7"); err != nil {
		t.Fatal(err)
	}
	rec, ok := f.byName("192-0-2-7.jabalihosted.com", "A")
	if !ok {
		t.Fatal("A record not created")
	}
	if rec.Proxied {
		t.Fatal("label A record MUST be DNS-only (proxied=false) — it points at a customer box")
	}
	if rec.Content != "192.0.2.7" {
		t.Errorf("content = %q", rec.Content)
	}

	// Second EnsureA updates in place (PUT), does not duplicate.
	if err := c.EnsureA(ctx, "192-0-2-7", "192.0.2.99"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	n := len(f.records)
	f.mu.Unlock()
	if n != 1 {
		t.Fatalf("upsert duplicated the record (%d records)", n)
	}
	rec, _ = f.byName("192-0-2-7.jabalihosted.com", "A")
	if rec.Content != "192.0.2.99" {
		t.Errorf("update didn't take: %q", rec.Content)
	}
}

func TestCloudflareDNS_ChallengeLifecycle(t *testing.T) {
	c, f := newTestCFDNS(t)
	ctx := context.Background()
	if err := c.SetChallenge(ctx, "192-0-2-7", "token-value-xyz"); err != nil {
		t.Fatal(err)
	}
	rec, ok := f.byName("_acme-challenge.192-0-2-7.jabalihosted.com", "TXT")
	if !ok || rec.Content != "token-value-xyz" || rec.Proxied {
		t.Fatalf("challenge TXT wrong: %+v ok=%v", rec, ok)
	}
	if err := c.ClearChallenge(ctx, "192-0-2-7"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.byName("_acme-challenge.192-0-2-7.jabalihosted.com", "TXT"); ok {
		t.Fatal("challenge not cleared")
	}
	// Clearing an absent record is a no-op, not an error.
	if err := c.ClearChallenge(ctx, "192-0-2-7"); err != nil {
		t.Fatalf("idempotent clear errored: %v", err)
	}
}

// The wildcard-cert case: two challenge values must coexist at one name, and
// cleanup must remove both. A replace-semantics present would clobber the
// first and fail issuance.
func TestCloudflareDNS_ChallengeMultiValue(t *testing.T) {
	c, f := newTestCFDNS(t)
	ctx := context.Background()
	if err := c.SetChallenge(ctx, "192-0-2-7", "value-apex"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetChallenge(ctx, "192-0-2-7", "value-wildcard"); err != nil {
		t.Fatal(err)
	}
	recs, _ := c.listRecords(ctx, "_acme-challenge.192-0-2-7.jabalihosted.com", "TXT")
	if len(recs) != 2 {
		t.Fatalf("want 2 coexisting challenge values, got %d", len(recs))
	}
	// Idempotent: re-adding an existing value doesn't duplicate.
	if err := c.SetChallenge(ctx, "192-0-2-7", "value-apex"); err != nil {
		t.Fatal(err)
	}
	recs, _ = c.listRecords(ctx, "_acme-challenge.192-0-2-7.jabalihosted.com", "TXT")
	if len(recs) != 2 {
		t.Fatalf("duplicate value added: %d records", len(recs))
	}
	// Cleanup removes ALL.
	if err := c.ClearChallenge(ctx, "192-0-2-7"); err != nil {
		t.Fatal(err)
	}
	recs, _ = c.listRecords(ctx, "_acme-challenge.192-0-2-7.jabalihosted.com", "TXT")
	if len(recs) != 0 {
		t.Fatalf("cleanup left %d challenge records", len(recs))
	}
	_ = f
}

func TestCloudflareDNS_RemoveLabel(t *testing.T) {
	c, f := newTestCFDNS(t)
	ctx := context.Background()
	c.EnsureA(ctx, "192-0-2-7", "192.0.2.7")
	c.SetChallenge(ctx, "192-0-2-7", "v")
	if err := c.RemoveLabel(ctx, "192-0-2-7"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.byName("192-0-2-7.jabalihosted.com", "A"); ok {
		t.Error("A survived RemoveLabel")
	}
	if _, ok := f.byName("_acme-challenge.192-0-2-7.jabalihosted.com", "TXT"); ok {
		t.Error("challenge survived RemoveLabel")
	}
}

// JAB-235: challenge writes into an explicit customer zone with a full
// FQDN record name — no hostedsvc base-domain coupling.
func TestCloudflareDNS_ChallengeTXTCustomerZone(t *testing.T) {
	c, f := newTestCFDNS(t)
	ctx := context.Background()
	name := "_acme-challenge.arama.co.il"
	if err := c.SetChallengeTXT(ctx, "zone-cust", name, "v1"); err != nil {
		t.Fatal(err)
	}
	rec, ok := f.byName(name, "TXT")
	if !ok || rec.Content != "v1" || rec.Proxied {
		t.Fatalf("customer-zone challenge TXT wrong: %+v ok=%v", rec, ok)
	}
	// Add-only multi-value + duplicate idempotence survive the generalization.
	if err := c.SetChallengeTXT(ctx, "zone-cust", name, "v2"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetChallengeTXT(ctx, "zone-cust", name, "v1"); err != nil {
		t.Fatal(err)
	}
	recs, _ := c.listRecordsIn(ctx, "zone-cust", name, "TXT")
	if len(recs) != 2 {
		t.Fatalf("want 2 challenge values, got %d", len(recs))
	}
	// The cap still holds for arbitrary FQDNs: fill to the limit, then the
	// next distinct value must be refused.
	for i := len(recs); i < maxChallengeRecordsPerLabel; i++ {
		if err := c.SetChallengeTXT(ctx, "zone-cust", name, fmt.Sprintf("fill-%d", i)); err != nil {
			t.Fatalf("fill %d: %v", i, err)
		}
	}
	if err := c.SetChallengeTXT(ctx, "zone-cust", name, "one-too-many"); err == nil {
		t.Fatal("value beyond maxChallengeRecordsPerLabel must be refused")
	}
	if err := c.ClearChallengeTXT(ctx, "zone-cust", name); err != nil {
		t.Fatal(err)
	}
	if recs, _ := c.listRecordsIn(ctx, "zone-cust", name, "TXT"); len(recs) != 0 {
		t.Fatalf("cleanup left %d records", len(recs))
	}
	if err := c.ClearChallengeTXT(ctx, "zone-cust", name); err != nil {
		t.Fatalf("idempotent clear errored: %v", err)
	}
}

func TestCloudflareDNS_FindZoneID(t *testing.T) {
	c, f := newTestCFDNS(t)
	f.mu.Lock()
	f.zones["arama.co.il"] = "zid-1"
	f.mu.Unlock()
	ctx := context.Background()
	id, err := c.FindZoneID(ctx, "arama.co.il")
	if err != nil || id != "zid-1" {
		t.Fatalf("FindZoneID = %q, %v", id, err)
	}
	// A zone the token cannot see returns "" with NO error — the caller
	// turns that into "customer must grant access", not a retry loop.
	id, err = c.FindZoneID(ctx, "uncovered.co.il")
	if err != nil || id != "" {
		t.Fatalf("uncovered zone: FindZoneID = %q, %v", id, err)
	}
}

func TestCloudflareDNS_VerifyToken(t *testing.T) {
	c, f := newTestCFDNS(t)
	f.mu.Lock()
	f.zones["a.com"] = "1"
	f.zones["b.com"] = "2"
	f.mu.Unlock()
	n, err := c.VerifyToken(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("VerifyToken = %d, %v", n, err)
	}
	bad := &CloudflareDNS{Token: "wrong", HTTP: c.HTTP, api: c.api}
	if _, err := bad.VerifyToken(context.Background()); err == nil {
		t.Fatal("bad token must fail verification")
	}
}

// The interface contract both backends satisfy.
var _ DNSBackend = (*CloudflareDNS)(nil)
var _ DNSBackend = (*PDNS)(nil)
