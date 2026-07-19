package cpanel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- fake DNS repos (JAB-28: ImportDNS writes the panel dns_records table) ---

type fakeDNSZoneRepo struct{ byName map[string]*models.DNSZone }

func (f *fakeDNSZoneRepo) FindByName(_ context.Context, name string) (*models.DNSZone, error) {
	return f.byName[name], nil
}
func (f *fakeDNSZoneRepo) Create(context.Context, *models.DNSZone) error             { return nil }
func (f *fakeDNSZoneRepo) Update(context.Context, *models.DNSZone) error             { return nil }
func (f *fakeDNSZoneRepo) Delete(context.Context, string) error                      { return nil }
func (f *fakeDNSZoneRepo) FindByID(context.Context, string) (*models.DNSZone, error) { return nil, nil }
func (f *fakeDNSZoneRepo) FindByDomainID(context.Context, string) (*models.DNSZone, error) {
	return nil, nil
}
func (f *fakeDNSZoneRepo) ListAll(context.Context) ([]models.DNSZone, error) { return nil, nil }

type fakeDNSRecordRepo struct {
	existing []models.DNSRecord
	created  []models.DNSRecord
}

func (f *fakeDNSRecordRepo) ListByZoneID(context.Context, string) ([]models.DNSRecord, error) {
	return f.existing, nil
}
func (f *fakeDNSRecordRepo) Create(_ context.Context, r *models.DNSRecord) error {
	f.created = append(f.created, *r)
	return nil
}
func (f *fakeDNSRecordRepo) Update(context.Context, *models.DNSRecord) error { return nil }
func (f *fakeDNSRecordRepo) Delete(context.Context, string) error            { return nil }
func (f *fakeDNSRecordRepo) FindByID(context.Context, string) (*models.DNSRecord, error) {
	return nil, nil
}
func (f *fakeDNSRecordRepo) DeleteByZoneID(context.Context, string) error { return nil }
func (f *fakeDNSRecordRepo) DeleteByZoneIDAndManagedBy(context.Context, string, string) error {
	return nil
}

var (
	_ repository.DNSZoneRepository   = (*fakeDNSZoneRepo)(nil)
	_ repository.DNSRecordRepository = (*fakeDNSRecordRepo)(nil)
)

// fakeDomainRepo embeds the interface (nil) and overrides only FindByName —
// the only method ImportDNS calls (JAB-28 find-or-create the zone).
type fakeDomainRepo struct {
	repository.DomainRepository
	byName map[string]*models.Domain
}

func (f *fakeDomainRepo) FindByName(_ context.Context, name string) (*models.Domain, error) {
	return f.byName[name], nil
}

func domains(names ...string) *fakeDomainRepo {
	m := map[string]*models.Domain{}
	for _, n := range names {
		m[n] = &models.Domain{ID: "dom-" + n, Name: n}
	}
	return &fakeDomainRepo{byName: m}
}

func writeZone(t *testing.T, zone string) *ParsedTarball {
	t.Helper()
	dir := t.TempDir()
	zonePath := filepath.Join(dir, "example.com.db")
	if err := os.WriteFile(zonePath, []byte(zone), 0o644); err != nil {
		t.Fatal(err)
	}
	return &ParsedTarball{ZoneFiles: []string{zonePath}}
}

func createdNames(recs *fakeDNSRecordRepo) map[string]bool {
	out := map[string]bool{}
	for _, r := range recs.created {
		out[r.Name+"/"+r.Type] = true
	}
	return out
}

// Apex NS + apex A/AAAA are filtered once per zone; www A imports.
func TestImportDNS_ApexFilteredWwwImported(t *testing.T) {
	parsed := writeZone(t, `$ORIGIN example.com.
$TTL 14400
@   IN  SOA ns1.example.com. admin.example.com. ( 2026010101 3600 1800 1209600 86400 )
@       IN  NS  ns1.example.com.
@       IN  NS  ns2.example.com.
www     IN  A   192.0.2.10
@       IN  A   192.0.2.10
`)
	zones := &fakeDNSZoneRepo{byName: map[string]*models.DNSZone{"example.com": {ID: "z1", Name: "example.com"}}}
	recs := &fakeDNSRecordRepo{}
	res, err := ImportDNS(context.Background(), zones, recs, domains("example.com"), parsed, "203.0.113.9")
	if err != nil {
		t.Fatalf("ImportDNS: %v", err)
	}
	n := 0
	for _, s := range res.Skipped {
		if strings.HasPrefix(s, "apex_ns_a_handled_by_jabali:") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("apex skip note emitted %d times, want 1 (skipped=%v)", n, res.Skipped)
	}
	got := createdNames(recs)
	if !got["www/A"] {
		t.Errorf("www A should be imported, created=%v", recs.created)
	}
	if got["@/A"] {
		t.Errorf("apex A must NOT be imported (jabali owns the apex)")
	}
}

// Custom/subdomain records import; a source record whose (name,type) collides
// with a jabali bootstrap record (apex MX) is skipped so mail stays on jabali.
func TestImportDNS_ImportsCustomDedupsBootstrap(t *testing.T) {
	parsed := writeZone(t, `$ORIGIN example.com.
$TTL 14400
@   IN  SOA ns1.example.com. admin.example.com. ( 1 3600 1800 1209600 86400 )
@              IN  MX  10 mail.source.example.
digibandit     IN  A   209.166.145.172
pizza          IN  A   209.166.145.175
mail._domainkey IN TXT "v=DKIM1; k=rsa; p=abc"
`)
	zones := &fakeDNSZoneRepo{byName: map[string]*models.DNSZone{"example.com": {ID: "z1", Name: "example.com"}}}
	recs := &fakeDNSRecordRepo{existing: []models.DNSRecord{{Name: "@", Type: "MX", Content: "10 mail.example.com"}}}
	if _, err := ImportDNS(context.Background(), zones, recs, domains("example.com"), parsed, "203.0.113.9"); err != nil {
		t.Fatalf("ImportDNS: %v", err)
	}
	got := createdNames(recs)
	for _, want := range []string{"digibandit/A", "pizza/A"} {
		if !got[want] {
			t.Errorf("expected %s imported; created=%v", want, recs.created)
		}
	}
	if got["@/MX"] {
		t.Errorf("source apex MX must be skipped (jabali bootstrap MX wins)")
	}
	// #327: source mail-infrastructure records (DKIM here) are NOT imported —
	// jabali regenerates DKIM/SPF/DMARC for THIS server, so importing the
	// source's would duplicate them and pin the old server's selector/IP.
	if got["mail._domainkey/TXT"] {
		t.Errorf("source DKIM must be dropped as mail-infra (jabali owns DKIM)")
	}
}

// zone_not_provisioned when the domain's zone doesn't exist yet.
func TestImportDNS_SkipsWhenZoneMissing(t *testing.T) {
	parsed := writeZone(t, `$ORIGIN example.com.
www IN A 192.0.2.1
`)
	zones := &fakeDNSZoneRepo{byName: map[string]*models.DNSZone{}}
	recs := &fakeDNSRecordRepo{}
	// Neither the zone nor the domain exist -> domain_not_found, nothing created.
	res, _ := ImportDNS(context.Background(), zones, recs, &fakeDomainRepo{byName: map[string]*models.Domain{}}, parsed, "203.0.113.9")
	if len(recs.created) != 0 {
		t.Errorf("no records should be created when the domain is missing")
	}
	found := false
	for _, s := range res.Skipped {
		if strings.HasPrefix(s, "domain_not_found:") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected domain_not_found skip, got %v", res.Skipped)
	}

	// Zone missing but the domain EXISTS -> the zone is created + records land.
	recs2 := &fakeDNSRecordRepo{}
	res2, _ := ImportDNS(context.Background(), &fakeDNSZoneRepo{byName: map[string]*models.DNSZone{}}, recs2, domains("example.com"), writeZone(t, "$ORIGIN example.com.\nwww IN A 192.0.2.1\n"), "203.0.113.9")
	if !createdNames(recs2)["www/A"] {
		t.Errorf("zone should be auto-created and www A imported; created=%v skipped=%v", recs2.created, res2.Skipped)
	}
}

func TestToPanelRecordName(t *testing.T) {
	cases := []struct{ name, origin, want string }{
		{"itflow.app", "itflow.app", "@"},
		{"itflow.app.", "itflow.app", "@"},
		{"@", "itflow.app", "@"},
		{"digibandit.itflow.app", "itflow.app", "digibandit"},
		{"mail._domainkey.itflow.app", "itflow.app", "mail._domainkey"},
		{"www", "itflow.app", "www"},
	}
	for _, c := range cases {
		if got := toPanelRecordName(c.name, c.origin); got != c.want {
			t.Errorf("toPanelRecordName(%q,%q) = %q, want %q", c.name, c.origin, got, c.want)
		}
	}
}

// JAB-28: SRV priority must go in the DNSRecord.Prio column, with content =
// "weight port target" only — else PowerDNS serves a malformed SRV that doesn't
// resolve (QA: _submission._tcp.itflow.app existed but didn't answer).
func TestParseBINDZone_SRVPriorityColumn(t *testing.T) {
	zone := "$ORIGIN example.com.\n_submission._tcp  IN  SRV  1 0 587 mail.example.com.\n"
	z, _, ok := ParseBINDZone(strings.NewReader(zone), "example.com")
	if !ok {
		t.Fatal("ParseBINDZone failed")
	}
	var srv *migrate.DNSRecord
	for i := range z.Records {
		if z.Records[i].Type == "SRV" {
			srv = &z.Records[i]
			break
		}
	}
	if srv == nil {
		t.Fatal("no SRV record parsed")
	}
	if srv.Prio != 1 {
		t.Errorf("SRV Prio = %d, want 1 (priority in the prio column)", srv.Prio)
	}
	if srv.Content != "0 587 mail.example.com" {
		t.Errorf("SRV Content = %q, want \"0 587 mail.example.com\" (weight port target, no priority)", srv.Content)
	}
}

// GH #327: CAA records must import (PowerDNS + the panel API both support them),
// not be dropped as unsupported.
func TestParseBINDZone_CAA(t *testing.T) {
	zone := "$ORIGIN example.com.\n@  IN  CAA  0 issue \"letsencrypt.org\"\n"
	z, _, ok := ParseBINDZone(strings.NewReader(zone), "example.com")
	if !ok {
		t.Fatal("ParseBINDZone failed")
	}
	var caa *migrate.DNSRecord
	for i := range z.Records {
		if z.Records[i].Type == "CAA" {
			caa = &z.Records[i]
			break
		}
	}
	if caa == nil {
		t.Fatal("CAA record dropped on import")
	}
	if caa.Content != "0 issue \"letsencrypt.org\"" {
		t.Errorf("CAA content = %q, want verbatim rdata", caa.Content)
	}
}
