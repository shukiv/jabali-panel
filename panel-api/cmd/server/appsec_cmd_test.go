package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadOperatorHeader(t *testing.T) {
	cases := []struct {
		name          string
		body          string
		wantMode      string
		wantCountries []string
	}{
		{"empty file → defaults", "", "", nil},
		{
			"present: mode=allow, countries=US,IL,GB",
			"# jabali-mode: allow\n# jabali-countries: US, IL, GB\nname: x\n",
			"allow", []string{"US", "IL", "GB"},
		},
		{
			"present: mode only",
			"# jabali-mode: deny\n# jabali-countries:\nname: x\n",
			"deny", nil,
		},
		{
			"header stops at first non-comment line",
			"name: x\n# jabali-mode: allow\n",
			"", nil, // mode line is below the first non-comment → ignored
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "jabali-appsec.yaml")
			if c.body != "" {
				if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			mode, countries := readOperatorHeader(path)
			if mode != c.wantMode {
				t.Errorf("mode = %q, want %q", mode, c.wantMode)
			}
			if !reflect.DeepEqual(countries, c.wantCountries) {
				t.Errorf("countries = %v, want %v", countries, c.wantCountries)
			}
		})
	}
}

func TestDetectInbandRules(t *testing.T) {
	dir := t.TempDir()
	// Empty dir → just the always-on patterns.
	got := detectInbandRules(dir)
	if len(got) != 2 || got[0] != "crowdsecurity/vpatch-*" || got[1] != "crowdsecurity/generic-*" {
		t.Errorf("empty dir got %v", got)
	}
	// Touch crs.yaml + base-config.yaml → include them.
	for _, f := range []string{"crs.yaml", "base-config.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte{}, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got = detectInbandRules(dir)
	want := []string{
		"crowdsecurity/vpatch-*",
		"crowdsecurity/generic-*",
		"crowdsecurity/base-config",
		"crowdsecurity/crs",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("after touch got %v, want %v", got, want)
	}
}
