package cpanel

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// realMainManifest is a verbatim cpanel <userdata>/main from a live account
// that has a subdomain. This is where sub_domains actually lives; the
// cp/<user> file the old code read has no SUB_DOMAINS key at all (JAB-220).
const realMainManifest = `---
addon_domains: {}

main_domain: bbestgun.co.il
parked_domains: []

sub_domains:
  - staging.bbestgun.co.il
`

// realMainNoSubs is the same manifest for an account with none — an inline
// empty list, which must yield nothing rather than a bogus entry.
const realMainNoSubs = `---
addon_domains: {}
main_domain: old.arizot-e.com
parked_domains: []
sub_domains: []
`

func writeMain(t *testing.T, body string) *ParsedTarball {
	t.Helper()
	extract := t.TempDir()
	root := filepath.Join(extract, "cpmove-someuser", "userdata")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "main"), []byte(body), 0o644); err != nil {
		t.Fatalf("write main: %v", err)
	}
	return &ParsedTarball{ExtractDir: extract, SourceUser: "someuser"}
}

// The regression: the real manifest must yield the subdomain.
func TestDetectSubDomains_ReadsRealMainManifest(t *testing.T) {
	got := detectSubDomains(writeMain(t, realMainManifest), "")
	if !reflect.DeepEqual(got, []string{"staging.bbestgun.co.il"}) {
		t.Fatalf("got %#v, want [staging.bbestgun.co.il]", got)
	}
}

// `sub_domains: []` is a real answer, not a parse failure.
func TestDetectSubDomains_InlineEmptyListYieldsNothing(t *testing.T) {
	if got := detectSubDomains(writeMain(t, realMainNoSubs), ""); len(got) != 0 {
		t.Fatalf("want none, got %#v", got)
	}
}

func TestDetectSubDomains_MultipleAndSortedAndDeduped(t *testing.T) {
	body := `---
main_domain: example.com
sub_domains:
  - shop.example.com
  - dev.example.com
  - shop.example.com
  - APP.example.com
parked_domains: []
`
	got := detectSubDomains(writeMain(t, body), "")
	want := []string{"app.example.com", "dev.example.com", "shop.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

// The block must stop at the next key, not swallow the rest of the file.
func TestDetectSubDomains_StopsAtNextKey(t *testing.T) {
	body := `---
sub_domains:
  - one.example.com
parked_domains:
  - parked.example.com
addon_domains: {}
`
	got := detectSubDomains(writeMain(t, body), "")
	if !reflect.DeepEqual(got, []string{"one.example.com"}) {
		t.Fatalf("leaked past the block: %#v", got)
	}
}

// Legacy fallback: honour a SUB_DOMAINS= key if some build emits one.
func TestDetectSubDomains_LegacyKeyFallback(t *testing.T) {
	parsed := writeMain(t, realMainNoSubs)
	legacy := filepath.Join(t.TempDir(), "cpuser")
	if err := os.WriteFile(legacy, []byte("USER=x\nSUB_DOMAINS=a.example.com,b.example.com\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := detectSubDomains(parsed, legacy)
	if !reflect.DeepEqual(got, []string{"a.example.com", "b.example.com"}) {
		t.Fatalf("legacy fallback failed: %#v", got)
	}
}

// The manifest wins over the legacy key when both are present, so a stale
// SUB_DOMAINS cannot override cpanel's own list.
func TestDetectSubDomains_ManifestBeatsLegacyKey(t *testing.T) {
	parsed := writeMain(t, realMainManifest)
	legacy := filepath.Join(t.TempDir(), "cpuser")
	if err := os.WriteFile(legacy, []byte("SUB_DOMAINS=stale.example.com\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := detectSubDomains(parsed, legacy)
	if !reflect.DeepEqual(got, []string{"staging.bbestgun.co.il"}) {
		t.Fatalf("legacy key overrode the manifest: %#v", got)
	}
}

// A missing manifest and no legacy file must be empty, not a panic.
func TestDetectSubDomains_NoSourcesIsEmpty(t *testing.T) {
	parsed := &ParsedTarball{ExtractDir: t.TempDir(), SourceUser: "nobody"}
	if got := detectSubDomains(parsed, ""); len(got) != 0 {
		t.Fatalf("want none, got %#v", got)
	}
}

func TestParseYAMLStringList_IgnoresOtherKeys(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "main")
	body := `---
other_list:
  - not.me
sub_domains:
  - yes.me
`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := parseYAMLStringList(p, "sub_domains"); !reflect.DeepEqual(got, []string{"yes.me"}) {
		t.Fatalf("got %#v", got)
	}
}
