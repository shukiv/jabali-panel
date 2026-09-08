package cpanel

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// createRecordingAgent records the domain name of every domain.create it
// receives, always succeeding, so ImportDomains reaches the row insert.
type createRecordingAgent struct{ created []string }

func (a *createRecordingAgent) Call(_ context.Context, command string, params any) (json.RawMessage, error) {
	if command == "domain.create" {
		if m, ok := params.(map[string]any); ok {
			if v, ok := m["domain"].(string); ok {
				a.created = append(a.created, v)
			}
		}
	}
	return json.RawMessage(`{}`), nil
}

// newDomainRepo: nothing exists yet, every Create succeeds.
type newDomainRepo struct{ repository.DomainRepository }

func (newDomainRepo) FindByName(context.Context, string) (*models.Domain, error) {
	return nil, repository.ErrNotFound
}
func (newDomainRepo) Create(context.Context, *models.Domain) error { return nil }

// GH #1606: a web domain WITHOUT a source DNS zone must still be created. The
// bug: ImportDomains iterated ZoneFiles OR DomainNames (DomainNames only as a
// fallback when ZoneFiles was empty), so any web-only domain was dropped once
// the account had at least one zone — johnnyq's Hestia user had zoned domains
// plus web-only subdomains whose DNS was hosted off-box, and the latter never
// migrated. The fix creates the UNION of both.
func TestImportDomains_UnionOfZonesAndWebDomains(t *testing.T) {
	dir := t.TempDir()
	// One source zone (a domain with DNS configured on the source box)…
	zoned := filepath.Join(dir, "domain.org.db")
	if err := os.WriteFile(zoned, []byte("@ IN SOA ns admin ( 1 2 3 4 5 )\n@ IN A 192.0.2.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ag := &createRecordingAgent{}
	parsed := &ParsedTarball{
		ExtractDir: dir,
		SourceUser: "alice",
		ZoneFiles:  []string{zoned},
		// …plus a web-only domain (no zone on the source) — must still migrate.
		DomainNames: []string{"sub.domain.co.uk"},
	}

	res, err := ImportDomains(context.Background(), newDomainRepo{}, ag,
		parsed, "01USERULID0000000000000000", "alice")
	if err != nil {
		t.Fatalf("ImportDomains hard error: %v", err)
	}

	sort.Strings(ag.created)
	got := ag.created
	want := []string{"domain.org", "sub.domain.co.uk"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("domain.create for %v, want %v (web-only domain must not be dropped)", ag.created, want)
	}
	if res.Created != 2 {
		t.Fatalf("res.Created = %d, want 2", res.Created)
	}
}

// A domain present in BOTH the zone list and the web list is created once, not
// twice (the union dedupes by name).
func TestImportDomains_UnionDedupes(t *testing.T) {
	dir := t.TempDir()
	zoned := filepath.Join(dir, "example.com.db")
	if err := os.WriteFile(zoned, []byte("@ IN SOA ns admin ( 1 2 3 4 5 )\n@ IN A 192.0.2.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ag := &createRecordingAgent{}
	parsed := &ParsedTarball{
		ExtractDir:  dir,
		SourceUser:  "bob",
		ZoneFiles:   []string{zoned},
		DomainNames: []string{"example.com"},
	}

	res, err := ImportDomains(context.Background(), newDomainRepo{}, ag,
		parsed, "01USERULID0000000000000000", "bob")
	if err != nil {
		t.Fatalf("ImportDomains hard error: %v", err)
	}
	if len(ag.created) != 1 || ag.created[0] != "example.com" {
		t.Fatalf("domain.create = %v, want exactly [example.com]", ag.created)
	}
	if res.Created != 1 {
		t.Fatalf("res.Created = %d, want 1", res.Created)
	}
}
