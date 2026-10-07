package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// stubMySQLResult makes every command print out and exit with code, and
// records its argv.
func stubMySQLResult(t *testing.T, out string, code int) *[][]string {
	t.Helper()
	var calls [][]string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		script := `printf '%s' "$0"; exit ` + map[bool]string{true: "1", false: "0"}[code != 0]
		return exec.CommandContext(ctx, "/bin/sh", "-c", script, out)
	}
	t.Cleanup(func() { execCommandContext = prev })
	return &calls
}

// GH #1993: a backup restore creates a database user's MariaDB account with
// create_only, which never changes an account that already exists — that
// account is not the restored row's to take over, whoever holds it.
func TestDBUserCreate_CreateOnlyNeverChangesAnExistingAccount(t *testing.T) {
	for name, p := range map[string]map[string]any{
		"hash":     {"db_user_name": "alice_u", "password_hash": "*2470C0C06DEE42FD1618BB99005ADCA2EC9D1E19", "create_only": true},
		"password": {"db_user_name": "alice_u", "password": "Secret123!x", "create_only": true},
	} {
		t.Run(name, func(t *testing.T) {
			calls := stubMySQLResult(t, "", 0)
			raw, _ := json.Marshal(p)
			if _, err := dbUserCreateHandler(context.Background(), raw); err != nil {
				t.Fatal(err)
			}
			sql := strings.Join((*calls)[0], " ")
			if !strings.Contains(sql, "CREATE USER 'alice_u'@'localhost'") || strings.Contains(sql, "IF NOT EXISTS") || strings.Contains(sql, "ALTER USER") {
				t.Fatalf("create_only ran %q: want a plain CREATE USER", sql)
			}
		})
	}
}

func TestDBUserCreate_CreateOnlyReportsAnExistingAccount(t *testing.T) {
	stubMySQLResult(t, "ERROR 1396 (HY000) at line 1: Operation CREATE USER failed for 'alice_u'@'localhost'", 1)
	raw, _ := json.Marshal(map[string]any{"db_user_name": "alice_u", "password": "Secret123!x", "create_only": true})
	_, err := dbUserCreateHandler(context.Background(), raw)
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeAlreadyExists {
		t.Fatalf("got %v, want already_exists", err)
	}

	stubMySQLResult(t, "ERROR 2002 (HY000): Can't connect", 1)
	if _, err = dbUserCreateHandler(context.Background(), raw); !errors.As(err, &ae) || ae.Code != agentwire.CodeInternal {
		t.Fatalf("got %v, want internal for another failure", err)
	}
}

func TestAgentVersion_ReportsCreateOnly(t *testing.T) {
	v, _ := agentVersionHandler(context.Background(), nil)
	for _, c := range v.(agentVersionResponse).Capabilities {
		if c == "db_user_create_only" {
			return
		}
	}
	t.Fatal("agent.version must report db_user_create_only")
}
