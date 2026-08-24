package api

import "testing"

func TestCanonicalDBGrant(t *testing.T) {
	cases := []struct {
		name      string
		privs     []string
		level     string
		wantCanon string
		wantLevel string
		wantErr   bool
	}{
		{"rw shortcut is ALL", nil, "rw", "ALL", "rw", false},
		{"ro shortcut is SELECT", nil, "ro", "SELECT", "ro", false},
		{"explicit ALL", []string{"ALL"}, "", "ALL", "rw", false},
		{"explicit SELECT only maps to ro", []string{"SELECT"}, "", "SELECT", "ro", false},
		{"custom subset stores as custom", []string{"SELECT", "INSERT"}, "", "SELECT,INSERT", "custom", false},
		{"privileges take precedence over level", []string{"SELECT"}, "rw", "SELECT", "ro", false},
		{"invalid level", nil, "admin", "", "", true},
		{"invalid privilege", []string{"NUKE"}, "", "", "", true},
		{"neither privileges nor level", nil, "", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			canon, level, err := CanonicalDBGrant(tc.privs, tc.level)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got canon=%q level=%q", canon, level)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if canon != tc.wantCanon || level != tc.wantLevel {
				t.Errorf("got (%q,%q), want (%q,%q)", canon, level, tc.wantCanon, tc.wantLevel)
			}
		})
	}
}
