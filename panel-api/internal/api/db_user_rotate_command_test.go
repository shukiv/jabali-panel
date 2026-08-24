package api

import "testing"

// JAB-285: the MariaDB rotate verb's password field is `new_password`. The CLI
// had regressed to `password`, which the agent rejects ("new_password cannot be
// empty"), so CLI MariaDB rotation always failed. This guards the exact wire
// field for each engine so REST + CLI (both routed through this helper) can't
// drift again.
func TestDBUserRotateAgentCommand(t *testing.T) {
	t.Run("mariadb uses new_password", func(t *testing.T) {
		verb, params := DBUserRotateAgentCommand("mariadb", "alice_db", "s3cret")
		if verb != "db_user.rotate_password" {
			t.Fatalf("verb = %q, want db_user.rotate_password", verb)
		}
		if params["new_password"] != "s3cret" {
			t.Errorf("must send new_password=s3cret, got %v", params["new_password"])
		}
		if params["db_user_name"] != "alice_db" {
			t.Errorf("db_user_name = %v", params["db_user_name"])
		}
		if _, hasOld := params["password"]; hasOld {
			t.Error("must NOT send the legacy `password` field for MariaDB rotate")
		}
	})

	t.Run("empty engine defaults to the mariadb rotate", func(t *testing.T) {
		verb, params := DBUserRotateAgentCommand("", "bob_db", "pw")
		if verb != "db_user.rotate_password" || params["new_password"] != "pw" {
			t.Fatalf("non-postgres engine must use the mariadb rotate verb + new_password; got verb=%q params=%v", verb, params)
		}
	})

	t.Run("postgres uses create_role with password", func(t *testing.T) {
		verb, params := DBUserRotateAgentCommand("postgres", "carol_db", "pgpw")
		if verb != "db.postgres.create_role" {
			t.Fatalf("verb = %q, want db.postgres.create_role", verb)
		}
		if params["role"] != "carol_db" || params["password"] != "pgpw" {
			t.Errorf("postgres params wrong: %v", params)
		}
		if _, hasNew := params["new_password"]; hasNew {
			t.Error("postgres create_role must not carry new_password")
		}
	})
}
