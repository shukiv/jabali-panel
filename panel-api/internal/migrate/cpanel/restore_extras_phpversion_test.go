package cpanel

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// realFPMYaml is a verbatim cpanel <domain>.php-fpm.yaml, copied from a live
// source box. The point of pinning it here is what it does NOT contain: the
// key detection used to look for. 0 of 143 such files on that box carried
// `phpversion`, while 141 plain userdata files did — which is why detection
// silently returned nothing and every migrated domain inherited the
// destination default (JAB-218).
const realFPMYaml = `---
_is_present: 1
pm_max_children: 5
pm_max_requests: 20
pm_process_idle_timeout: 10
`

// writeUserdata builds a userdata dir mirroring a real extracted cpmove tree,
// including the sidecars and fixed-name files that must not be mistaken for
// domains. versions maps domain -> cpanel phpversion value ("" writes the
// domain file with no phpversion key at all).
func writeUserdata(t *testing.T, versions map[string]string) *ParsedTarball {
	t.Helper()
	extract := t.TempDir()
	root := filepath.Join(extract, "cpmove-someuser", "userdata")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Fixed-name entries cpanel keeps alongside the per-domain files.
	for name, body := range map[string]string{
		"main":                  "main_domain: example.com\n",
		"cache.json":            `{"x":1}`,
		"applications.json":     `{}`,
		"cpanel_redirects.json": `[]`,
		"cpanel_password_protected_directories.json": `{}`,
		"ruby-on-rails-rewrites.db":                  "",
		"scope":                                      "",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	for domain, ver := range versions {
		body := "documentroot: /home/someuser/public_html\n"
		if ver != "" {
			body += "phpversion: " + ver + "\n"
		}
		if err := os.WriteFile(filepath.Join(root, domain), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", domain, err)
		}
		// The sidecars a real tree carries. Neither must be read as a domain,
		// and the yaml must not be mistaken for a version source.
		if err := os.WriteFile(filepath.Join(root, domain+".php-fpm.yaml"), []byte(realFPMYaml), 0o644); err != nil {
			t.Fatalf("write yaml: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, domain+"_SSL"), []byte(body), 0o644); err != nil {
			t.Fatalf("write _SSL: %v", err)
		}
	}
	return &ParsedTarball{ExtractDir: extract, SourceUser: "someuser"}
}

// The regression itself: a real tree where phpversion lives only in the plain
// userdata file. Before the fix this returned an empty map.
func TestDetectPHPVersionsPerDomain_ReadsPlainUserdataNotFPMYaml(t *testing.T) {
	parsed := writeUserdata(t, map[string]string{
		"otzma-p.co.il":    "ea-php74",
		"old.arizot-e.com": "ea-php81",
		"third.example":    "ea-php74",
	})

	got := detectPHPVersionsPerDomain(parsed)

	want := map[string][]string{
		"7.4": {"otzma-p.co.il", "third.example"},
		"8.1": {"old.arizot-e.com"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("detect mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

// _SSL duplicates the http vhost's settings; counting it would double every
// domain and make the spread summary lie.
func TestDetectPHPVersionsPerDomain_SSLSidecarNotDoubleCounted(t *testing.T) {
	parsed := writeUserdata(t, map[string]string{"solo.example": "ea-php83"})

	got := detectPHPVersionsPerDomain(parsed)
	if len(got["8.3"]) != 1 {
		t.Fatalf("want 1 domain on 8.3, got %v", got["8.3"])
	}
}

// The fixed-name files (main, cache.json, scope, …) are not domains.
func TestDetectPHPVersionsPerDomain_IgnoresNonDomainFiles(t *testing.T) {
	parsed := writeUserdata(t, map[string]string{"real.example": "ea-php82"})

	for _, doms := range detectPHPVersionsPerDomain(parsed) {
		for _, d := range doms {
			if d != "real.example" {
				t.Errorf("non-domain file treated as a domain: %q", d)
			}
		}
	}
}

// A source that genuinely records nothing must be distinguishable from one we
// simply failed to parse — the caller turns this into a loud summary line.
func TestDetectPHPVersionsPerDomain_NoVersionsIsEmpty(t *testing.T) {
	parsed := writeUserdata(t, map[string]string{"nover.example": ""})

	if got := detectPHPVersionsPerDomain(parsed); len(got) != 0 {
		t.Fatalf("want empty, got %#v", got)
	}
}

// Legacy safety net: if some cpanel build DOES write phpversion into the
// sidecar, we still pick it up.
func TestDetectPHPVersionsPerDomain_FallsBackToFPMYaml(t *testing.T) {
	parsed := writeUserdata(t, map[string]string{"legacy.example": ""})
	root := filepath.Join(parsed.ExtractDir, "cpmove-someuser", "userdata")
	if err := os.WriteFile(filepath.Join(root, "legacy.example.php-fpm.yaml"),
		[]byte(realFPMYaml+"phpversion: ea-php80\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := detectPHPVersionsPerDomain(parsed)
	if len(got["8.0"]) != 1 || got["8.0"][0] != "legacy.example" {
		t.Fatalf("sidecar fallback failed: %#v", got)
	}
}

// detectPHPVersion shares the same scanner, so it must see the same data.
func TestDetectPHPVersion_PrimaryIsMostFrequent(t *testing.T) {
	parsed := writeUserdata(t, map[string]string{
		"a.example": "ea-php74",
		"b.example": "ea-php74",
		"c.example": "ea-php81",
	})

	primary, others := detectPHPVersion(parsed)
	if primary != "7.4" {
		t.Errorf("primary = %q, want 7.4", primary)
	}
	if !reflect.DeepEqual(others, []string{"8.1"}) {
		t.Errorf("others = %#v, want [8.1]", others)
	}
}

func TestSummarisePHPVersionSpread(t *testing.T) {
	tests := []struct {
		name string
		in   map[string][]string
		want string
	}{
		{"empty is explicit", map[string][]string{}, "none"},
		{"single", map[string][]string{"8.1": {"a"}}, "8.1x1"},
		{
			"sorted and counted",
			map[string][]string{"8.3": {"a", "b"}, "7.4": {"c"}, "8.1": {"d", "e", "f"}},
			"7.4x1,8.1x3,8.3x2",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := summarisePHPVersionSpread(tc.in); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestNormalisePHPVersion_CpanelForms(t *testing.T) {
	for in, want := range map[string]string{
		"ea-php74": "7.4",
		"ea-php81": "8.1",
		"ea-php83": "8.3",
		"8.2":      "8.2",
		"":         "",
	} {
		if got := normalisePHPVersion(in); got != want {
			t.Errorf("normalisePHPVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
