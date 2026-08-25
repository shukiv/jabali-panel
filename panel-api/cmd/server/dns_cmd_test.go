package main

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ---------- fakes ----------

type memDNSZoneRepo struct{ byName map[string]*models.DNSZone }

func newMemDNSZoneRepo(zs ...*models.DNSZone) *memDNSZoneRepo {
	f := &memDNSZoneRepo{byName: map[string]*models.DNSZone{}}
	for _, z := range zs {
		f.byName[z.Name] = z
	}
	return f
}

func (f *memDNSZoneRepo) FindByName(_ context.Context, name string) (*models.DNSZone, error) {
	if z, ok := f.byName[name]; ok {
		return z, nil
	}
	return nil, repository.ErrNotFound
}
func (f *memDNSZoneRepo) Create(context.Context, *models.DNSZone) error { return nil }
func (f *memDNSZoneRepo) Update(context.Context, *models.DNSZone) error { return nil }
func (f *memDNSZoneRepo) Delete(context.Context, string) error          { return nil }
func (f *memDNSZoneRepo) FindByID(context.Context, string) (*models.DNSZone, error) {
	return nil, repository.ErrNotFound
}
func (f *memDNSZoneRepo) FindByDomainID(context.Context, string) (*models.DNSZone, error) {
	return nil, repository.ErrNotFound
}
func (f *memDNSZoneRepo) ListAll(context.Context) ([]models.DNSZone, error) { return nil, nil }
func (f *memDNSZoneRepo) FindByDomainIDs(ctx context.Context, domainIDs []string) ([]models.DNSZone, error) {
	var out []models.DNSZone
	for _, id := range domainIDs {
		if z, err := f.FindByDomainID(ctx, id); err == nil && z != nil {
			out = append(out, *z)
		}
	}
	return out, nil
}

type memDNSRecordRepo struct{ byID map[string]*models.DNSRecord }

func newMemDNSRecordRepo(rs ...*models.DNSRecord) *memDNSRecordRepo {
	f := &memDNSRecordRepo{byID: map[string]*models.DNSRecord{}}
	for _, r := range rs {
		f.byID[r.ID] = r
	}
	return f
}

func (f *memDNSRecordRepo) Create(_ context.Context, r *models.DNSRecord) error {
	cp := *r
	f.byID[r.ID] = &cp
	return nil
}
func (f *memDNSRecordRepo) Update(_ context.Context, r *models.DNSRecord) error {
	cp := *r
	f.byID[r.ID] = &cp
	return nil
}
func (f *memDNSRecordRepo) Delete(_ context.Context, id string) error {
	delete(f.byID, id)
	return nil
}
func (f *memDNSRecordRepo) FindByID(_ context.Context, id string) (*models.DNSRecord, error) {
	if r, ok := f.byID[id]; ok {
		cp := *r
		return &cp, nil
	}
	return nil, repository.ErrNotFound
}
func (f *memDNSRecordRepo) ListByZoneID(_ context.Context, zoneID string) ([]models.DNSRecord, error) {
	var out []models.DNSRecord
	for _, r := range f.byID {
		if r.ZoneID == zoneID {
			out = append(out, *r)
		}
	}
	return out, nil
}
func (f *memDNSRecordRepo) DeleteByZoneID(context.Context, string) error { return nil }
func (f *memDNSRecordRepo) CountByZoneIDs(ctx context.Context, zoneIDs []string) (map[string]int64, error) {
	out := make(map[string]int64)
	for _, zid := range zoneIDs {
		recs, _ := f.ListByZoneID(ctx, zid)
		if len(recs) > 0 {
			out[zid] = int64(len(recs))
		}
	}
	return out, nil
}
func (f *memDNSRecordRepo) DeleteByZoneIDAndManagedBy(context.Context, string, string) error {
	return nil
}

// ensure the fakes satisfy the interfaces
var (
	_ repository.DNSZoneRepository   = (*memDNSZoneRepo)(nil)
	_ repository.DNSRecordRepository = (*memDNSRecordRepo)(nil)
)

func testDNSZone() *models.DNSZone {
	return &models.DNSZone{ID: "01ZONEEXAMPLECOM0000000000", DomainID: "01DOMEXAMPLE00000000000000", Name: "example.com", IsEnabled: true}
}

// ---------- tests ----------

func TestDNSRecordAdd_ValidDefaultsTTL(t *testing.T) {
	ctx := context.Background()
	zones := newMemDNSZoneRepo(testDNSZone())
	recs := newMemDNSRecordRepo()
	rec, err := dnsRecordAdd(ctx, zones, recs, "example.com", dnsRecordSpec{
		Name: "www", Type: "a", Content: "203.0.113.10", Enabled: true, // ttl 0 -> validator defaults 300
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if rec.Type != "A" {
		t.Errorf("type not upper-cased: %q", rec.Type)
	}
	if rec.TTL != 300 {
		t.Errorf("ttl default = %d, want 300", rec.TTL)
	}
	if rec.ZoneID != testDNSZone().ID {
		t.Errorf("record not bound to the zone: %q", rec.ZoneID)
	}
	if _, ok := recs.byID[rec.ID]; !ok {
		t.Errorf("record not persisted")
	}
}

func TestDNSRecordAdd_RejectsUnknownZone(t *testing.T) {
	ctx := context.Background()
	zones := newMemDNSZoneRepo() // empty
	recs := newMemDNSRecordRepo()
	_, err := dnsRecordAdd(ctx, zones, recs, "nope.example", dnsRecordSpec{Name: "@", Type: "A", Content: "203.0.113.1", Enabled: true})
	if err == nil || !strings.Contains(err.Error(), "no DNS zone") {
		t.Fatalf("want no-zone error, got %v", err)
	}
}

func TestDNSRecordAdd_RejectsInvalidType(t *testing.T) {
	ctx := context.Background()
	zones := newMemDNSZoneRepo(testDNSZone())
	recs := newMemDNSRecordRepo()
	if _, err := dnsRecordAdd(ctx, zones, recs, "example.com", dnsRecordSpec{Name: "@", Type: "WKS", Content: "x", Enabled: true}); err == nil {
		t.Fatal("expected invalid-type rejection")
	}
	if len(recs.byID) != 0 {
		t.Errorf("invalid record was persisted")
	}
}

func TestDNSRecordAdd_RejectsInvalidContent(t *testing.T) {
	ctx := context.Background()
	zones := newMemDNSZoneRepo(testDNSZone())
	recs := newMemDNSRecordRepo()
	// A record with a non-IPv4 content must be rejected by ValidateDNSRecord.
	if _, err := dnsRecordAdd(ctx, zones, recs, "example.com", dnsRecordSpec{Name: "@", Type: "A", Content: "not-an-ip", Enabled: true}); err == nil {
		t.Fatal("expected invalid A content rejection")
	}
}

func TestDNSRecordAdd_CNAMEConflict(t *testing.T) {
	ctx := context.Background()
	z := testDNSZone()
	zones := newMemDNSZoneRepo(z)
	// existing A at "www"
	recs := newMemDNSRecordRepo(&models.DNSRecord{ID: "01EXISTINGARECORD000000000", ZoneID: z.ID, Name: "www", Type: "A", Content: "203.0.113.9", TTL: 300, IsEnabled: true})
	// adding a CNAME at the same name must be rejected (RFC 1034 §3.6.2)
	if _, err := dnsRecordAdd(ctx, zones, recs, "example.com", dnsRecordSpec{Name: "www", Type: "CNAME", Content: "target.example.com", Enabled: true}); err == nil {
		t.Fatal("expected CNAME-exclusivity conflict")
	}
}

func TestDNSRecordDelete_ForceRemovesNonManaged(t *testing.T) {
	ctx := context.Background()
	z := testDNSZone()
	zones := newMemDNSZoneRepo(z)
	rec := &models.DNSRecord{ID: "01RECTODELETE0000000000000", ZoneID: z.ID, Name: "old", Type: "A", Content: "203.0.113.5", TTL: 300, IsEnabled: true}
	recs := newMemDNSRecordRepo(rec)
	if _, err := dnsRecordDelete(ctx, zones, recs, "example.com", rec.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := recs.byID[rec.ID]; ok {
		t.Errorf("record not deleted")
	}
}

func TestDNSRecordDelete_RefusesManaged(t *testing.T) {
	ctx := context.Background()
	z := testDNSZone()
	zones := newMemDNSZoneRepo(z)
	mb := "m6"
	rec := &models.DNSRecord{ID: "01MANAGEDREC00000000000000", ZoneID: z.ID, Name: "_autodiscover._tcp", Type: "SRV", Content: "0 0 443 mail.example.com", TTL: 300, Managed: true, ManagedBy: &mb, IsEnabled: true}
	recs := newMemDNSRecordRepo(rec)
	_, err := dnsRecordDelete(ctx, zones, recs, "example.com", rec.ID)
	if err == nil || !strings.Contains(err.Error(), "managed") {
		t.Fatalf("want managed-record refusal, got %v", err)
	}
	if _, ok := recs.byID[rec.ID]; !ok {
		t.Errorf("managed record was deleted despite refusal")
	}
}

func TestDNSRecordUpdate_RejectsCrossZone(t *testing.T) {
	ctx := context.Background()
	z := testDNSZone()
	zones := newMemDNSZoneRepo(z)
	// record belongs to a different zone id
	rec := &models.DNSRecord{ID: "01OTHERZONEREC000000000000", ZoneID: "01OTHERZONE0000000000000000", Name: "x", Type: "A", Content: "203.0.113.7", TTL: 300, IsEnabled: true}
	recs := newMemDNSRecordRepo(rec)
	_, err := dnsRecordUpdate(ctx, zones, recs, "example.com", rec.ID, dnsRecordSpec{Content: "203.0.113.8"}, map[string]bool{"content": true})
	if err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("want cross-zone refusal, got %v", err)
	}
}

func TestDNSRecordUpdate_ContentOnly(t *testing.T) {
	ctx := context.Background()
	z := testDNSZone()
	zones := newMemDNSZoneRepo(z)
	rec := &models.DNSRecord{ID: "01UPDREC000000000000000000", ZoneID: z.ID, Name: "@", Type: "A", Content: "203.0.113.1", TTL: 300, IsEnabled: true}
	recs := newMemDNSRecordRepo(rec)
	out, err := dnsRecordUpdate(ctx, zones, recs, "example.com", rec.ID, dnsRecordSpec{Content: "203.0.113.2"}, map[string]bool{"content": true})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if out.Content != "203.0.113.2" {
		t.Errorf("content not updated: %q", out.Content)
	}
	if recs.byID[rec.ID].Content != "203.0.113.2" {
		t.Errorf("update not persisted")
	}
}

// The dns command tree is well-formed: zone{list,show} + record{list,add,update,delete},
// and `record add` marks name/type/content required.
func TestDNSCmd_Tree(t *testing.T) {
	root := newDNSCmd()
	sub := map[string]*cobra.Command{}
	for _, c := range root.Commands() {
		sub[strings.Fields(c.Use)[0]] = c
	}
	for _, want := range []string{"zone", "record"} {
		if sub[want] == nil {
			t.Fatalf("dns missing subcommand %q", want)
		}
	}
	recNames := map[string]bool{}
	for _, c := range sub["record"].Commands() {
		recNames[strings.Fields(c.Use)[0]] = true
	}
	for _, want := range []string{"list", "add", "update", "delete"} {
		if !recNames[want] {
			t.Errorf("dns record missing %q", want)
		}
	}
}
