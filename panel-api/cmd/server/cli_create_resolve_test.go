package main

import "testing"

func TestResolveCreateIdentity(t *testing.T) {
	sp := func(s string) *string { return &s }
	eq := func(a, b *string) bool {
		if a == nil || b == nil {
			return a == b
		}
		return *a == *b
	}
	cases := []struct {
		name, username, email string
		host                  string
		wantUser              *string
		wantEmail             string
		wantErr               bool
	}{
		{"username only synthesizes email", "alice", "", "panel.example.com", sp("alice"), "alice@panel.example.com", false},
		{"email only derives username", "", "bob@x.com", "panel.example.com", sp("bob"), "bob@x.com", false},
		{"username + email both kept", "carol", "carol@x.com", "panel.example.com", sp("carol"), "carol@x.com", false},
		{"neither is an error", "", "", "panel.example.com", nil, "", true},
		{"invalid username is an error", "Bad User", "", "h", nil, "", true},
		// GH #1938: an admin resolves the same way; it needs a username too (the
		// login identifier; NOT NULL since migration 000164).
		{"admin email only derives username", "", "ops@x.com", "h", sp("ops"), "ops@x.com", false},
		{"admin username kept", "ops2", "", "panel.example.com", sp("ops2"), "ops2@panel.example.com", false},
		{"empty host falls back to localhost", "dan", "", "", sp("dan"), "dan@localhost.localdomain", false},
	}
	for _, c := range cases {
		u, e, err := resolveCreateIdentity(c.username, c.email, c.host)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", c.name, err, c.wantErr)
			continue
		}
		if c.wantErr {
			continue
		}
		if !eq(u, c.wantUser) || e != c.wantEmail {
			t.Errorf("%s: got (%v,%q) want (%v,%q)", c.name, u, e, c.wantUser, c.wantEmail)
		}
	}
}
