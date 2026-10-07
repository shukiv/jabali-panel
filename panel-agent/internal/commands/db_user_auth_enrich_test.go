package commands

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// stubMySQL makes every command print out and records its argv.
func stubMySQL(t *testing.T, out string) *[][]string {
	t.Helper()
	var calls [][]string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		return exec.CommandContext(ctx, "/bin/sh", "-c", `printf '%s' "$0"`, out)
	}
	t.Cleanup(func() { execCommandContext = prev })
	return &calls
}

// TestEnrichDatabaseUserAuth_CarriesEachMariaDBUsersPasswordHash: the backup
// bundle carries each MariaDB database user's password hash, so a restore on
// another server can recreate the account with the same password (GH #1993).
func TestEnrichDatabaseUserAuth_CarriesEachMariaDBUsersPasswordHash(t *testing.T) {
	const aliceHash = "*2470C0C06DEE42FD1618BB99005ADCA2EC9D1E19"
	calls := stubMySQL(t,
		"alice_u\t"+aliceHash+"\n"+
			"alice_ro\tnot-a-native-hash\n"+ // another plugin's string: ignored
			"bob_u\t*0123456789ABCDEF0123456789ABCDEF01234567\n") // not asked for: ignored
	meta := &backup.AccountMetadata{DatabaseUsers: []backup.MetadataDatabaseUser{
		{Username: "alice_u", Engine: "mariadb"},
		{Username: "alice_ro"}, // a bundle row without an engine is MariaDB
		{Username: "alice_pg", Engine: "postgres"},
		{Username: "root"},
		{Username: "alice'; DROP USER x; --"},
	}}
	if err := enrichDatabaseUserAuth(context.Background(), meta); err != nil {
		t.Fatal(err)
	}
	if got := meta.DatabaseUsers[0].NativePasswordHash; got != aliceHash {
		t.Fatalf("alice_u hash = %q, want %q", got, aliceHash)
	}
	for _, du := range meta.DatabaseUsers[1:] {
		if du.NativePasswordHash != "" {
			t.Fatalf("%s got hash %q", du.Username, du.NativePasswordHash)
		}
	}
	if len(*calls) != 1 {
		t.Fatalf("want one query, got %v", *calls)
	}
	q := strings.Join((*calls)[0], " ")
	if !strings.Contains(q, "'alice_u'") || !strings.Contains(q, "'alice_ro'") {
		t.Fatalf("query doesn't ask for the account's MariaDB users: %s", q)
	}
	for _, never := range []string{"alice_pg", "'root'", "DROP"} {
		if strings.Contains(q, never) {
			t.Fatalf("query asks for %s: %s", never, q)
		}
	}
}

func TestEnrichDatabaseUserAuth_NoMariaDBUsersRunsNothing(t *testing.T) {
	calls := stubMySQL(t, "")
	meta := &backup.AccountMetadata{DatabaseUsers: []backup.MetadataDatabaseUser{{Username: "alice_pg", Engine: "postgres"}}}
	if err := enrichDatabaseUserAuth(context.Background(), meta); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Fatalf("ran %v", *calls)
	}
}
