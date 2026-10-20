package cpanel

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// recordingAgent records every command it receives.
type recordingAgent struct {
	dbUserCreates []string // db_user_name values passed to db_user.create
}

func (a *recordingAgent) Call(_ context.Context, command string, params any) (json.RawMessage, error) {
	if command == "db_user.create" {
		if m, ok := params.(map[string]any); ok {
			if n, ok := m["db_user_name"].(string); ok {
				a.dbUserCreates = append(a.dbUserCreates, n)
			}
		}
	}
	return json.RawMessage(`{}`), nil
}

type nopDBRepo struct{ repository.DatabaseRepository }

// JAB-48: the source's original MySQL users (with their native password hashes)
// must NOT be recreated by default — only when the operator opts into
// compatibility mode. Uses an empty MySQLDumps set so only the compat-user loop
// runs (the per-DB primary user lives inside the dump loop).
func TestImportDatabases_CompatUsersInertByDefault(t *testing.T) {
	extract := t.TempDir()
	etc := filepath.Join(extract, "cpmove-alice")
	if err := os.MkdirAll(etc, 0o755); err != nil {
		t.Fatal(err)
	}
	// One source MySQL user with a native (mysql_native_password) hash.
	grants := "GRANT USAGE ON *.* TO 'alice_old'@'localhost' " +
		"IDENTIFIED BY PASSWORD '*0123456789ABCDEF0123456789ABCDEF01234567';\n"
	if err := os.WriteFile(filepath.Join(etc, "mysql.sql"), []byte(grants), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name       string
		preserve   bool
		wantCreate bool
	}{
		{"default: compat user not recreated", false, false},
		{"opt-in: compat user recreated", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ag := &recordingAgent{}
			parsed := &ParsedTarball{ExtractDir: extract, SourceUser: "alice"} // no MySQLDumps
			if _, err := ImportDatabases(context.Background(), &nopDBRepo{}, gh723DBUserRepo{}, nil, ag,
				parsed, "01USERULID0000000000000000", "alice", tc.preserve); err != nil {
				t.Fatalf("ImportDatabases: %v", err)
			}
			created := false
			for _, n := range ag.dbUserCreates {
				if n == "alice_old" {
					created = true
				}
			}
			if created != tc.wantCreate {
				t.Fatalf("compat user alice_old created=%v, want %v (calls=%v)", created, tc.wantCreate, ag.dbUserCreates)
			}
		})
	}
}
