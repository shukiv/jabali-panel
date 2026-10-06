package commands

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// TestDBVerbs_RefuseThisServersOwnAccountsAndDatabases: the database verbs
// never touch MariaDB's or jabali's own accounts and databases, whatever the
// caller sends. The panel's checks are the first line; this one holds when a
// name gets past them (an admin-only name typed into the CLI, a backup
// restore driven by an uploaded file — GH #1993). Refused before any command
// runs.
func TestDBVerbs_RefuseThisServersOwnAccountsAndDatabases(t *testing.T) {
	type call struct {
		name    string
		handler func(context.Context, json.RawMessage) (any, error)
		params  map[string]any
	}
	const hash = "*0123456789ABCDEF0123456789ABCDEF01234567"
	var calls []call
	for _, u := range []string{"root", "mysql", "crowdsec", "jabali", "jabali_panel", "jabali_kratos", "Jabali_Pdns", "jb_s_alice_wp"} {
		calls = append(calls,
			call{"db_user.create " + u, dbUserCreateHandler, map[string]any{"db_user_name": u, "password_hash": hash}},
			call{"db_user.create (password) " + u, dbUserCreateHandler, map[string]any{"db_user_name": u, "password": "Secret123!x"}},
			call{"db_user.grant to " + u, dbUserGrantHandler, map[string]any{"db_name": "alice_wp", "db_user_name": u, "privileges": []string{"ALL"}}},
			call{"db_user.revoke from " + u, dbUserRevokeHandler, map[string]any{"db_name": "alice_wp", "db_user_name": u, "privileges": []string{"ALL"}}},
			call{"db_user.drop " + u, dbUserDropHandler, map[string]any{"db_user_name": u}},
			call{"db_user.rotate_password " + u, dbUserRotatePasswordHandler, map[string]any{"db_user_name": u, "new_password": "Secret123!x"}},
		)
	}
	for _, db := range []string{"mysql", "sys", "crowdsec", "jabali", "jabali_panel", "JABALI_kratos"} {
		calls = append(calls,
			call{"db_user.grant on " + db, dbUserGrantHandler, map[string]any{"db_name": db, "db_user_name": "alice_u", "privileges": []string{"ALL"}}},
			call{"db_user.revoke on " + db, dbUserRevokeHandler, map[string]any{"db_name": db, "db_user_name": "alice_u", "privileges": []string{"ALL"}}},
			call{"db.create " + db, dbCreateHandler, map[string]any{"db_name": db}},
			call{"db.drop " + db, dbDropHandler, map[string]any{"db_name": db}},
		)
	}
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			ran := recordExec(t)
			raw, _ := json.Marshal(c.params)
			_, err := c.handler(context.Background(), raw)
			var ae *agentwire.AgentError
			if !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument {
				t.Fatalf("want an invalid-argument refusal, got %v", err)
			}
			if len(*ran) != 0 {
				t.Fatalf("refused call still ran %v", *ran)
			}
		})
	}
}

// TestDBVerbs_AccountNamesStillWork: the guard leaves account names alone,
// including ones that merely start like a reserved one.
func TestDBVerbs_AccountNamesStillWork(t *testing.T) {
	for _, u := range []string{"alice_u", "jabalidemo_wp", "rootkit_db", "mysqlfan_u"} {
		t.Run(u, func(t *testing.T) {
			ran := recordExec(t)
			raw, _ := json.Marshal(map[string]any{"db_user_name": u, "password": "Secret123!x"})
			if _, err := dbUserCreateHandler(context.Background(), raw); err != nil {
				t.Fatalf("db_user.create %s: %v", u, err)
			}
			if len(*ran) == 0 {
				t.Fatal("db_user.create ran nothing")
			}
		})
	}
	for name, c := range map[string]struct {
		handler func(context.Context, json.RawMessage) (any, error)
		params  map[string]any
	}{
		"db_user.grant":           {dbUserGrantHandler, map[string]any{"db_name": "jabalidemo_wp", "db_user_name": "jabalidemo_u", "privileges": []string{"ALL"}}},
		"db_user.revoke":          {dbUserRevokeHandler, map[string]any{"db_name": "jabalidemo_wp", "db_user_name": "jabalidemo_u", "privileges": []string{"ALL"}}},
		"db_user.rotate_password": {dbUserRotatePasswordHandler, map[string]any{"db_user_name": "jabalidemo_u", "new_password": "Secret123!x"}},
	} {
		ran := recordExec(t)
		raw, _ := json.Marshal(c.params)
		if _, err := c.handler(context.Background(), raw); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(*ran) == 0 {
			t.Fatalf("%s ran nothing", name)
		}
	}
}
