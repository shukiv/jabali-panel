package cpanel

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// addonMainManifest is the shape of a live cPanel <userdata>/main for an
// account with an addon domain. cPanel serves the addon (shop.example) through
// a subdomain of the main domain (shop.main.example); this map is the only
// place the pair is written down.
const addonMainManifest = `---
addon_domains:
  shop.example: shop.main.example
main_domain: main.example
parked_domains: []
sub_domains:
  - shop.main.example
`

// writeAddonAccount builds an extracted cpmove tree for one main domain plus
// one addon site. The addon's userdata file is named after, and carries the
// servername of, its backing subdomain — exactly what cPanel writes. zones
// lists the account's zone files (an addon has its own zone on cPanel).
func writeAddonAccount(t *testing.T, zones ...string) *ParsedTarball {
	t.Helper()
	extract := t.TempDir()
	root := filepath.Join(extract, "cpmove-acct", "userdata")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	files := map[string]string{
		"main": addonMainManifest,
		"shop.main.example": "---\ndocumentroot: /home/acct/shop.example\n" +
			"phpversion: ea-php74\nservername: shop.main.example\n",
		"main.example": "---\ndocumentroot: /home/acct/public_html\n" +
			"phpversion: ea-php83\nservername: main.example\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	p := &ParsedTarball{ExtractDir: extract, SourceUser: "acct"}
	for _, z := range zones {
		p.ZoneFiles = append(p.ZoneFiles, filepath.Join(extract, "cpmove-acct", "dnszones", z+".db"))
	}
	return p
}

func docrootsByName(rows []domainSrc) map[string]string {
	out := map[string]string{}
	for _, r := range rows {
		out[r.Name] = r.SourceRel
	}
	return out
}

// The bug: the offline home split keyed every site by its userdata
// servername, so the addon's files were copied into
// domains/shop.main.example/public_html and shop.example's own docroot stayed
// empty. The addon site must be keyed by the addon domain.
func TestParseUserdataDocroots_AddonSiteKeyedByAddonDomain(t *testing.T) {
	parsed := writeAddonAccount(t, "main.example", "shop.example")

	got := docrootsByName(parseUserdataDocroots(parsed))
	want := map[string]string{
		"main.example": "public_html",
		"shop.example": "shop.example",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("docroots\n got: %#v\nwant: %#v", got, want)
	}
}

// An addon this import does not create (no zone, not a parsed web domain)
// must not be remapped: its files would land in a docroot nothing serves.
func TestParseUserdataDocroots_AddonNotImportedKeepsSubdomain(t *testing.T) {
	parsed := writeAddonAccount(t, "main.example")

	got := docrootsByName(parseUserdataDocroots(parsed))
	if _, ok := got["shop.main.example"]; !ok {
		t.Fatalf("subdomain row lost when its addon is not imported: %#v", got)
	}
	if _, ok := got["shop.example"]; ok {
		t.Fatalf("remapped to an addon this import does not create: %#v", got)
	}
}

// The addon must keep its source PHP version. Keyed by the userdata file
// name, the version went to the backing subdomain and the addon ran the box
// default.
func TestDetectPHPVersionsPerDomain_AddonGetsItsSourceVersion(t *testing.T) {
	parsed := writeAddonAccount(t, "main.example", "shop.example")

	got := detectPHPVersionsPerDomain(parsed)
	want := map[string][]string{
		"7.4": {"shop.example"},
		"8.3": {"main.example"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("versions\n got: %#v\nwant: %#v", got, want)
	}
}

// subCreateDomainRepo reports every name as new and records the rows created.
type subCreateDomainRepo struct {
	repository.DomainRepository
	created []models.Domain
}

func (r *subCreateDomainRepo) FindByName(context.Context, string) (*models.Domain, error) {
	return nil, repository.ErrNotFound
}

func (r *subCreateDomainRepo) Create(_ context.Context, d *models.Domain) error {
	r.created = append(r.created, *d)
	return nil
}

// domainCreateAgent records every domain.create dispatch.
type domainCreateAgent struct {
	creates []map[string]any
}

func (a *domainCreateAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	if cmd == "domain.create" {
		if m, ok := params.(map[string]any); ok {
			a.creates = append(a.creates, m)
		}
	}
	return json.RawMessage(`{}`), nil
}

// On cPanel the backing subdomain served the same site as the addon. Created
// as a plain subdomain on an empty docroot it serves a blank page; it must be
// a 301 to the addon, on the row AND in the vhost rendered at create time.
func TestImportExtras_AddonBackingSubdomainRedirectsToAddon(t *testing.T) {
	parsed := writeAddonAccount(t, "main.example", "shop.example")
	repo := &subCreateDomainRepo{}
	ag := &domainCreateAgent{}

	if _, err := ImportExtras(context.Background(), repo, nil, nil, nil, nil, nil, ag,
		parsed, "user-1", "acct", false); err != nil {
		t.Fatalf("ImportExtras: %v", err)
	}

	if len(repo.created) != 1 || repo.created[0].Name != "shop.main.example" {
		t.Fatalf("want one subdomain row shop.main.example, got %#v", repo.created)
	}
	row := repo.created[0]
	if row.RedirectAllTo == nil || *row.RedirectAllTo != "https://shop.example/" {
		t.Fatalf("redirect_all_to = %v, want https://shop.example/", row.RedirectAllTo)
	}
	if row.RedirectAllType == nil || *row.RedirectAllType != "301" {
		t.Fatalf("redirect_all_type = %v, want 301", row.RedirectAllType)
	}

	if len(ag.creates) != 1 {
		t.Fatalf("want one domain.create, got %d", len(ag.creates))
	}
	dir, _ := ag.creates[0]["redirect_directives"].(string)
	if !strings.Contains(dir, `return 301 "https://shop.example/";`) {
		t.Fatalf("vhost rendered without the 301: redirect_directives = %q", dir)
	}
}

// A subdomain that backs no addon is created exactly as before: no redirect
// on the row and no redirect_directives in the dispatch.
func TestImportExtras_PlainSubdomainHasNoRedirect(t *testing.T) {
	parsed := writeMain(t, realMainManifest)
	repo := &subCreateDomainRepo{}
	ag := &domainCreateAgent{}

	if _, err := ImportExtras(context.Background(), repo, nil, nil, nil, nil, nil, ag,
		parsed, "user-1", "someuser", false); err != nil {
		t.Fatalf("ImportExtras: %v", err)
	}
	if len(repo.created) != 1 || repo.created[0].RedirectAllTo != nil || repo.created[0].RedirectAllType != nil {
		t.Fatalf("plain subdomain got a redirect: %#v", repo.created)
	}
	if _, ok := ag.creates[0]["redirect_directives"]; ok {
		t.Fatalf("plain subdomain dispatch carries redirect_directives: %#v", ag.creates[0])
	}
}
