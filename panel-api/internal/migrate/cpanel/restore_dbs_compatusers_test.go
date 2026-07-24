package cpanel

import (
	"context"
	"testing"
)

// GH #633: sources without a cpmove `mysql.sql` (HestiaCP) pre-populate
// parsed.CompatUsers from their own backup metadata. ImportDatabases must
// honour that slice exactly like the cpmove-parsed grants — recreated only
// when the operator opts into credential preservation.
func TestImportDatabases_UsesParsedCompatUsers(t *testing.T) {
	compat := []CompatUser{{
		Name: "alice_wp",
		Host: "localhost",
		Hash: "*0123456789ABCDEF0123456789ABCDEF01234567",
		Grant: []CompatGrant{{
			SourceDB: "alice_wp",
			Privs:    []string{"ALL"},
		}},
	}}

	for _, tc := range []struct {
		name       string
		preserve   bool
		wantCreate bool
	}{
		{"default: parsed compat user not recreated", false, false},
		{"opt-in: parsed compat user recreated", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ag := &recordingAgent{}
			// No cpmove mysql.sql on disk — the ONLY source of compat users is
			// parsed.CompatUsers (the Hestia adapter path).
			parsed := &ParsedTarball{ExtractDir: t.TempDir(), SourceUser: "alice", CompatUsers: compat}
			if _, err := ImportDatabases(context.Background(), &nopDBRepo{}, nil, nil, ag,
				parsed, "01USERULID0000000000000000", "alice", tc.preserve); err != nil {
				t.Fatalf("ImportDatabases: %v", err)
			}
			created := false
			for _, n := range ag.dbUserCreates {
				if n == "alice_wp" {
					created = true
				}
			}
			if created != tc.wantCreate {
				t.Fatalf("compat user alice_wp created=%v, want %v (calls=%v)", created, tc.wantCreate, ag.dbUserCreates)
			}
		})
	}
}
