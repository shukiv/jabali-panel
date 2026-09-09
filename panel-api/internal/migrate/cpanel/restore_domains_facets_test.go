package cpanel

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// recordingDomainRepo captures every row ImportDomains creates so a test can
// assert the facet flags (WebDisabled / DNSDisabled) on each.
type recordingDomainRepo struct {
	repository.DomainRepository
	created []*models.Domain
}

func (recordingDomainRepo) FindByName(context.Context, string) (*models.Domain, error) {
	return nil, repository.ErrNotFound
}
func (r *recordingDomainRepo) Create(_ context.Context, d *models.Domain) error {
	r.created = append(r.created, d)
	return nil
}

func rowByName(rows []*models.Domain, name string) *models.Domain {
	for _, d := range rows {
		if d.Name == name {
			return d
		}
	}
	return nil
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// GH #1606 (follow-up): an importer that enumerates web dirs and DNS zones
// separately (Hestia, parsed.SeparateWebDNSLists) must recreate the source's
// resource TYPES, not blanket web+DNS. johnnyq's Hestia account migrated a web
// domain (DNS hosted off-box) and a DNS-only zone (parked, no web); the union
// fix carried both over but as full-facet domains, so the web domain grew a
// stray managed zone and the parked zone grew a stray web vhost. This asserts
// each source facet lands as its own type.
func TestImportDomains_HestiaSeparateFacets(t *testing.T) {
	dir := t.TempDir()
	// A DNS-only zone on the source (present in ZoneFiles, absent from web dirs).
	dnsOnly := filepath.Join(dir, "parked.example.db")
	if err := os.WriteFile(dnsOnly, []byte("@ IN SOA ns admin ( 1 2 3 4 5 )\n@ IN A 192.0.2.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A full-facet domain: it has BOTH a source zone and a web dir.
	both := filepath.Join(dir, "full.example.db")
	if err := os.WriteFile(both, []byte("@ IN SOA ns admin ( 1 2 3 4 5 )\n@ IN A 192.0.2.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ag := &createRecordingAgent{}
	repo := &recordingDomainRepo{}
	parsed := &ParsedTarball{
		ExtractDir:          dir,
		SourceUser:          "carol",
		SeparateWebDNSLists: true,
		ZoneFiles:           []string{dnsOnly, both},
		// Web dirs: a web-only domain (no zone on the source) + the full-facet one.
		DomainNames: []string{"web.example", "full.example"},
	}

	res, err := ImportDomains(context.Background(), repo, ag,
		parsed, "01USERULID0000000000000000", "carol")
	if err != nil {
		t.Fatalf("ImportDomains hard error: %v", err)
	}
	if res.Created != 3 {
		t.Fatalf("res.Created = %d, want 3", res.Created)
	}

	// web-only: web facet on, DNS facet off, docroot present, vhost built.
	if d := rowByName(repo.created, "web.example"); d == nil {
		t.Fatal("web.example row missing")
	} else {
		if d.WebDisabled {
			t.Error("web.example: WebDisabled=true, want false (has web dir)")
		}
		if !d.DNSDisabled {
			t.Error("web.example: DNSDisabled=false, want true (no source zone)")
		}
		if d.DocRoot == "" {
			t.Error("web.example: empty DocRoot, want a docroot for a web domain")
		}
		if !contains(ag.created, "web.example") {
			t.Error("web.example: no domain.create call, want vhost built for a web domain")
		}
	}

	// DNS-only: web facet off, DNS facet on, no docroot, NO vhost built.
	if d := rowByName(repo.created, "parked.example"); d == nil {
		t.Fatal("parked.example row missing")
	} else {
		if !d.WebDisabled {
			t.Error("parked.example: WebDisabled=false, want true (no web dir)")
		}
		if d.DNSDisabled {
			t.Error("parked.example: DNSDisabled=true, want false (has source zone)")
		}
		if d.DocRoot != "" {
			t.Errorf("parked.example: DocRoot=%q, want empty for a DNS-only zone", d.DocRoot)
		}
		if contains(ag.created, "parked.example") {
			t.Error("parked.example: domain.create was called, want NO vhost for a DNS-only zone")
		}
	}

	// both: full-facet.
	if d := rowByName(repo.created, "full.example"); d == nil {
		t.Fatal("full.example row missing")
	} else {
		if d.WebDisabled || d.DNSDisabled {
			t.Errorf("full.example: WebDisabled=%v DNSDisabled=%v, want both false", d.WebDisabled, d.DNSDisabled)
		}
		if !contains(ag.created, "full.example") {
			t.Error("full.example: no domain.create call, want vhost built")
		}
	}
}

// Regression guard: an importer that does NOT separate the lists (cpanel, DA,
// Plesk, CloudPanel — SeparateWebDNSLists=false) keeps the historical
// full-facet behavior even for a name that appears in only one source list. The
// union fix must not silently downgrade those to web-only/DNS-only.
func TestImportDomains_NonSeparateStaysFullFacet(t *testing.T) {
	dir := t.TempDir()
	zoned := filepath.Join(dir, "site.example.db")
	if err := os.WriteFile(zoned, []byte("@ IN SOA ns admin ( 1 2 3 4 5 )\n@ IN A 192.0.2.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ag := &createRecordingAgent{}
	repo := &recordingDomainRepo{}
	parsed := &ParsedTarball{
		ExtractDir: dir,
		SourceUser: "dave",
		// SeparateWebDNSLists left false. A web-only name (DomainNames, no zone).
		ZoneFiles:   []string{zoned},
		DomainNames: []string{"webonly.example"},
	}

	if _, err := ImportDomains(context.Background(), repo, ag,
		parsed, "01USERULID0000000000000000", "dave"); err != nil {
		t.Fatalf("ImportDomains hard error: %v", err)
	}

	for _, name := range []string{"site.example", "webonly.example"} {
		d := rowByName(repo.created, name)
		if d == nil {
			t.Fatalf("%s row missing", name)
		}
		if d.WebDisabled || d.DNSDisabled {
			t.Errorf("%s: WebDisabled=%v DNSDisabled=%v, want both false (importer does not separate lists)", name, d.WebDisabled, d.DNSDisabled)
		}
		if !contains(ag.created, name) {
			t.Errorf("%s: no domain.create call, want vhost built (full-facet)", name)
		}
	}
}
