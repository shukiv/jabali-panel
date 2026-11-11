package cpanel

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestParseYAMLStringMap_BlockMapping(t *testing.T) {
	p := filepath.Join(t.TempDir(), "main")
	writeFile(t, p, `---
addon_domains:
  one.example: one.main.example

  "two.example": 'two.main.example'
main_domain: main.example
parked_domains:
  - parked.example
`)
	got := parseYAMLStringMap(p, "addon_domains")
	want := [][2]string{{"one.example", "one.main.example"}, {"two.example", "two.main.example"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

// `addon_domains: {}` is cPanel's answer for an account with no addons.
func TestParseYAMLStringMap_InlineEmptyYieldsNothing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "main")
	writeFile(t, p, realMainManifest)
	if got := parseYAMLStringMap(p, "addon_domains"); len(got) != 0 {
		t.Fatalf("want none, got %#v", got)
	}
}

func TestParseYAMLStringMap_MissingFileYieldsNothing(t *testing.T) {
	if got := parseYAMLStringMap(filepath.Join(t.TempDir(), "main"), "addon_domains"); got != nil {
		t.Fatalf("want nil, got %#v", got)
	}
}

// writeAddonMain writes a userdata/main with the given addon_domains body and
// returns a ParsedTarball whose zone files are zones.
func writeAddonMain(t *testing.T, addonBlock string, zones ...string) *ParsedTarball {
	t.Helper()
	extract := t.TempDir()
	writeFile(t, filepath.Join(extract, "cpmove-u", "userdata", "main"),
		"---\naddon_domains:\n"+addonBlock+"main_domain: main.example\n")
	p := &ParsedTarball{ExtractDir: extract, SourceUser: "u"}
	for _, z := range zones {
		p.ZoneFiles = append(p.ZoneFiles, filepath.Join(extract, "dnszones", z+".db"))
	}
	return p
}

func TestAddonDomainsBySubdomain_KeepsOnlyImportedValidUnambiguousPairs(t *testing.T) {
	parsed := writeAddonMain(t, ""+
		"  Good.Example: GOOD.main.example\n"+ // kept, lower-cased
		"  web.example: web.main.example\n"+ // addon only a parsed web domain: kept
		"  gone.example: gone.main.example\n"+ // addon not imported
		"  bad_name!: bad.main.example\n"+ // invalid addon
		"  evil.example: ../../etc\n"+ // invalid subdomain
		"  dup1.example: shared.main.example\n"+ // two addons, one subdomain
		"  dup2.example: shared.main.example\n"+
		"  self.example: main.example\n", // subdomain is itself imported
		"main.example", "good.example", "dup1.example", "dup2.example", "self.example", "evil.example")
	parsed.DomainNames = []string{"web.example"}

	got, warnings := AddonDomainsBySubdomain(parsed)
	want := map[string]string{
		"good.main.example": "good.example",
		"web.main.example":  "web.example",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("map\n got: %#v\nwant: %#v", got, want)
	}

	wantDropped := []string{"bad_name!", "dup1.example", "dup2.example", "evil.example", "gone.example", "self.example"}
	var dropped []string
	for _, w := range warnings {
		if !strings.HasPrefix(w, "addon_domain_skip:") {
			t.Fatalf("warning without the skip prefix: %q", w)
		}
		addon, _, _ := strings.Cut(strings.TrimPrefix(w, "addon_domain_skip:"), "→")
		dropped = append(dropped, addon)
	}
	sort.Strings(dropped)
	if !reflect.DeepEqual(dropped, wantDropped) {
		t.Fatalf("dropped\n got: %#v\nwant: %#v\nwarnings: %#v", dropped, wantDropped, warnings)
	}
}

func TestAddonDomainsBySubdomain_NoAddonsNoWarnings(t *testing.T) {
	m, w := AddonDomainsBySubdomain(writeMain(t, realMainManifest))
	if len(m) != 0 || len(w) != 0 {
		t.Fatalf("want empty, got %#v %#v", m, w)
	}
	m, w = AddonDomainsBySubdomain(&ParsedTarball{ExtractDir: t.TempDir(), SourceUser: "u"})
	if len(m) != 0 || len(w) != 0 {
		t.Fatalf("no main file: want empty, got %#v %#v", m, w)
	}
	if m, w = AddonDomainsBySubdomain(nil); len(m) != 0 || len(w) != 0 {
		t.Fatalf("nil parsed: want empty, got %#v %#v", m, w)
	}
}

func TestSiteDomain(t *testing.T) {
	m := map[string]string{"shop.main.example": "shop.example"}
	for in, want := range map[string]string{
		"shop.main.example": "shop.example",
		"SHOP.main.example": "shop.example",
		"main.example":      "main.example",
		"Other.Example":     "Other.Example", // not an addon's subdomain: unchanged
	} {
		if got := SiteDomain(m, in); got != want {
			t.Errorf("SiteDomain(%q) = %q, want %q", in, got, want)
		}
	}
	if got := SiteDomain(nil, "a.example"); got != "a.example" {
		t.Errorf("nil map: got %q", got)
	}
}

// UserdataSiteDocroots feeds the SSH-pull home copy. The addon site must be
// keyed by the addon domain; everything else exactly as the userdata says.
func TestUserdataSiteDocroots_AddonKeyedByAddonDomain(t *testing.T) {
	parsed := writeAddonAccount(t, "main.example", "shop.example")

	got := map[string]string{}
	for _, s := range UserdataSiteDocroots(parsed) {
		got[s.Domain] = s.DocRoot
	}
	want := map[string]string{
		"main.example": "/home/acct/public_html",
		"shop.example": "/home/acct/shop.example",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("site docroots\n got: %#v\nwant: %#v", got, want)
	}
}

// The cp/<user>/userdata root, quoted values, a file with no servername line
// (keyed by its file name) and a file with no documentroot (skipped), as the
// copy loop this replaced handled them.
func TestUserdataSiteDocroots_CpRootAndFallbacks(t *testing.T) {
	extract := t.TempDir()
	root := filepath.Join(extract, "cp", "u", "userdata")
	writeFile(t, filepath.Join(root, "main"), realMainNoSubs)
	writeFile(t, filepath.Join(root, "a.example"), "documentroot: '/home/u/public_html'\nservername: \"a.example\"\n")
	writeFile(t, filepath.Join(root, "b.example"), "documentroot: /home/u/b\n")
	writeFile(t, filepath.Join(root, "c.example"), "servername: c.example\n")
	writeFile(t, filepath.Join(root, "a.example_SSL"), "documentroot: /home/u/ssl\nservername: a.example\n")
	writeFile(t, filepath.Join(root, "a.example.php-fpm.yaml"), realFPMYaml)

	got := UserdataSiteDocroots(&ParsedTarball{ExtractDir: extract, SourceUser: "u"})
	sort.Slice(got, func(i, j int) bool { return got[i].Domain < got[j].Domain })
	want := []SiteDocroot{
		{Domain: "a.example", DocRoot: "/home/u/public_html"},
		{Domain: "b.example", DocRoot: "/home/u/b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
}
