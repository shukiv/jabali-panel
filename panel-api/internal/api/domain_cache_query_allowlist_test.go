package api

import "testing"

func TestNormalizeCacheQueryAllowlist(t *testing.T) {
	cases := []struct {
		name    string
		in      []string
		want    string
		wantErr bool
	}{
		{"empty clears", []string{}, "", false},
		{"blanks dropped", []string{"", "  "}, "", false},
		{"single", []string{"paged"}, "paged", false},
		{"lowercased + sorted", []string{"S", "paged"}, "paged,s", false},
		{"deduped", []string{"paged", "PAGED", "paged"}, "paged", false},
		{"underscore + digits ok", []string{"jet_page2"}, "jet_page2", false},
		{"reject metachar", []string{"pa.ged"}, "", true},
		{"reject slash", []string{"../x"}, "", true},
		{"reject space", []string{"a b"}, "", true},
		{"reject too long", []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, "", true}, // 33 chars
		{"reject too many", []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}, "", true},
	}
	for _, tc := range cases {
		got, err := normalizeCacheQueryAllowlist(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: expected error, got %q", tc.name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
