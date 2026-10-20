package commands

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// A panel username may hold "-", so a tenant's database names and users
// (<panel username>_<name>) and its shadow-admins do too. Every db_user.*
// command and the shadow-admin commands accept them, and the name reaches SQL
// only inside a quoted literal or identifier: "--" there is text, not a
// comment.
func TestDBUserCommands_AcceptHyphenatedNames(t *testing.T) {
	for _, c := range []struct {
		name    string
		handler func(context.Context, json.RawMessage) (any, error)
		params  any
		want    string
	}{
		{"create", dbUserCreateHandler,
			dbUserCreateParams{DBUserName: "alice-x_app", Password: "Secret123"},
			"CREATE USER IF NOT EXISTS 'alice-x_app'@'localhost'"},
		{"create with --", dbUserCreateHandler,
			dbUserCreateParams{DBUserName: "a--b_app", Password: "Secret123"},
			"'a--b_app'@'localhost' IDENTIFIED BY"},
		{"drop", dbUserDropHandler,
			dbUserDropParams{DBUserName: "alice-x_app"},
			"DROP USER IF EXISTS 'alice-x_app'@'localhost'"},
		{"grant", dbUserGrantHandler,
			dbUserGrantParams{DBName: "alice-x_shop", DBUserName: "alice-x_app", Privileges: []string{"ALL"}},
			"ON `alice-x\\_shop`.* TO 'alice-x_app'@'localhost'"},
		{"revoke", dbUserRevokeHandler,
			dbUserRevokeParams{DBName: "alice-x_shop", DBUserName: "alice-x_app", Privileges: []string{"ALL"}},
			"ON `alice-x\\_shop`.* FROM 'alice-x_app'@'localhost'"},
		{"rotate_password", dbUserRotatePasswordHandler,
			dbUserRotatePasswordParams{DBUserName: "alice-x_app", NewPassword: "NewSecret123"},
			"ALTER USER 'alice-x_app'@'localhost'"},
		{"mysqladmin.ensure", dbMysqladminEnsureHandler,
			dbMysqladminEnsureParams{PanelUsername: "alice-x"},
			"CREATE USER IF NOT EXISTS 'alice-x_mysqladmin'@'localhost'"},
		{"mysqladmin.sync_grants", dbMysqladminSyncGrantsHandler,
			dbMysqladminSyncGrantsParams{PanelUsername: "alice-x", DBNames: []string{"alice-x_shop"}},
			"GRANT ALL PRIVILEGES ON `alice-x\\_shop`.* TO 'alice-x_mysqladmin'@'localhost';"},
	} {
		t.Run(c.name, func(t *testing.T) {
			execs := shadowMySQL(t, nil, "")
			raw, _ := json.Marshal(c.params)
			if _, err := c.handler(context.Background(), raw); err != nil {
				t.Fatalf("%s refused a hyphenated name: %v", c.name, err)
			}
			if all := strings.Join(*execs, "\n"); !strings.Contains(all, c.want) {
				t.Fatalf("want %q in:\n%s", c.want, all)
			}
		})
	}
}

// The Postgres shadow-admin role of a hyphenated panel username is made, and
// its name is a double-quoted identifier.
func TestPostgresShadowadmin_AcceptsHyphenatedUsername(t *testing.T) {
	var sqls []string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "-c" {
				sqls = append(sqls, args[i+1])
			}
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })

	raw, _ := json.Marshal(dbPostgresShadowadminParams{PanelUsername: "alice-x"})
	if _, err := dbPostgresShadowadminEnsureHandler(context.Background(), raw); err != nil {
		t.Fatalf("ensure refused a hyphenated username: %v", err)
	}
	if all := strings.Join(sqls, "\n"); !strings.Contains(all, `CREATE ROLE "alice-x_pgadmin" LOGIN`) {
		t.Fatalf("want a quoted alice-x_pgadmin role in:\n%s", all)
	}
}
