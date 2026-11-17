package main

import (
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/cpanel"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// A DirectAdmin backup restored offline decides the owner mailbox in its own
// parser: an empty OwnerEmail there means "not carried" (the domain had no
// mail on the source). The cPanel-style default (<source user>@<primary
// domain>) must not refill it, or the import creates a mailbox, and turns
// mail on, for a domain whose mail the source never served.
func TestDefaultOwnerEmail(t *testing.T) {
	cases := []struct {
		name, kind, preset, primary, want string
	}{
		{"cpanel archive gets the default", models.MigrationSourceCpanel, "", "example.com", "alice@example.com"},
		{"directadmin pull (cpmove layout) gets the default", models.MigrationSourceDirectAdmin, "", "example.com", "alice@example.com"},
		{"directadmin native keeps the parser's empty owner", daNativeParseKind, "", "example.com", ""},
		{"directadmin native keeps the parser's own owner", daNativeParseKind, "alice@example.com", "example.com", "alice@example.com"},
		{"an address the parser set is kept", models.MigrationSourceCpanel, "bob@example.org", "example.com", "bob@example.org"},
		{"no primary domain leaves it empty", models.MigrationSourceCpanel, "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &cpanel.ParsedTarball{OwnerEmail: tc.preset}
			defaultOwnerEmail(p, tc.kind, "alice", tc.primary)
			if p.OwnerEmail != tc.want {
				t.Errorf("owner email %q, want %q", p.OwnerEmail, tc.want)
			}
		})
	}
	defaultOwnerEmail(nil, models.MigrationSourceCpanel, "alice", "example.com") // must not panic
}
