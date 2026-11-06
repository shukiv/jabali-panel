package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// sqlExec is one exec the recorder saw: the program, its argv, and the SQL it
// was given (its -e argument, else its stdin).
type sqlExec struct {
	name string
	args []string
	sql  string
}

// sqlRecorder hands out client commands that save the SQL they are given (the
// -e argument, else stdin) to a file and exit 0, or exit 1 with a rule's
// message when the SQL contains that rule's text. The SQL of a statement on
// stdin is only known once the command runs, so it is read back afterwards.
type sqlRecorder struct {
	dir   string
	mu    sync.Mutex
	execs []sqlExec
	files []string
}

func newSQLRecorder(t *testing.T) *sqlRecorder {
	t.Helper()
	return &sqlRecorder{dir: t.TempDir()}
}

const sqlRecorderScript = `f=$1; shift; cat >"$f"
while [ $# -ge 2 ]; do
	if grep -qF -- "$1" "$f"; then printf '%s\n' "$2" >&2; exit 1; fi
	shift 2
done
exit 0`

// cmd records one exec of name/args and returns the command to run for it.
func (r *sqlRecorder) cmd(ctx context.Context, name string, args []string, rules ...[2]string) *exec.Cmd {
	r.mu.Lock()
	file := filepath.Join(r.dir, fmt.Sprintf("%04d.sql", len(r.files)))
	r.execs = append(r.execs, sqlExec{name: name, args: append([]string(nil), args...)})
	r.files = append(r.files, file)
	r.mu.Unlock()
	shArgs := []string{"-c", sqlRecorderScript, "sh", file}
	for _, rule := range rules {
		shArgs = append(shArgs, rule[0], rule[1])
	}
	cmd := exec.CommandContext(ctx, "sh", shArgs...)
	if sql := dashE(args); sql != "" {
		cmd.Stdin = strings.NewReader(sql)
	}
	return cmd
}

// all lists the execs so far with their SQL.
func (r *sqlRecorder) all() []sqlExec {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]sqlExec, len(r.execs))
	for i, e := range r.execs {
		b, _ := os.ReadFile(r.files[i])
		e.sql = string(b)
		out[i] = e
	}
	return out
}

// sqls lists the SQL of the execs so far.
func (r *sqlRecorder) sqls() []string {
	execs := r.all()
	out := make([]string, len(execs))
	for i, e := range execs {
		out[i] = e.sql
	}
	return out
}

// dashE returns the argument after -e, or "".
func dashE(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-e" {
			return args[i+1]
		}
	}
	return ""
}

// recordSQLExecs swaps the exec seam for the test so every exec goes through a
// sqlRecorder with rules, and returns its listing.
func recordSQLExecs(t *testing.T, rules ...[2]string) func() []sqlExec {
	t.Helper()
	rec := newSQLRecorder(t)
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return rec.cmd(ctx, name, args, rules...)
	}
	t.Cleanup(func() { execCommandContext = prev })
	return rec.all
}

// requireSecretOnStdinOnly fails when secret is on any recorded argv, or when
// no recorded client received it.
func requireSecretOnStdinOnly(t *testing.T, execs []sqlExec, secret string) {
	t.Helper()
	sent := false
	for _, e := range execs {
		for _, a := range e.args {
			if strings.Contains(a, secret) {
				t.Fatalf("%s argv carries the secret: %q", e.name, e.args)
			}
		}
		if strings.Contains(e.sql, secret) {
			sent = true
		}
	}
	if !sent {
		t.Fatalf("no client received the secret; execs: %+v", execs)
	}
}

// Every statement that carries a password, a password hash or key material
// goes to the database client on stdin, never on its command line.
func TestSQLWithSecrets_GoesOnStdin(t *testing.T) {
	ctx := context.Background()

	t.Run("db_user.create", func(t *testing.T) {
		execs := recordSQLExecs(t)
		raw, _ := json.Marshal(dbUserCreateParams{DBUserName: "alice_app", Password: "Mk-create-7f3a"})
		if _, err := dbUserCreateHandler(ctx, raw); err != nil {
			t.Fatalf("create: %v", err)
		}
		requireSecretOnStdinOnly(t, execs(), "Mk-create-7f3a")
	})

	t.Run("db_user.create create_only with a hash", func(t *testing.T) {
		execs := recordSQLExecs(t)
		hash := "*" + strings.Repeat("A1", 20)
		raw, _ := json.Marshal(dbUserCreateParams{DBUserName: "alice_app", PasswordHash: hash, CreateOnly: true})
		if _, err := dbUserCreateHandler(ctx, raw); err != nil {
			t.Fatalf("create_only: %v", err)
		}
		requireSecretOnStdinOnly(t, execs(), hash)
	})

	t.Run("db_user.rotate_password", func(t *testing.T) {
		execs := recordSQLExecs(t)
		raw, _ := json.Marshal(dbUserRotatePasswordParams{DBUserName: "alice_app", NewPassword: "Mk-rotate-91c2"})
		if _, err := dbUserRotatePasswordHandler(ctx, raw); err != nil {
			t.Fatalf("rotate: %v", err)
		}
		requireSecretOnStdinOnly(t, execs(), "Mk-rotate-91c2")
	})

	t.Run("db.root.set_password", func(t *testing.T) {
		// The ALTER fails here, so the handler stops before it would write
		// the secret file.
		execs := recordSQLExecs(t, [2]string{"PASSWORD(", "ERROR 1045: refused in test"})
		raw, _ := json.Marshal(dbRootPasswordParams{NewPassword: "Mk-root-55d0aa"})
		_, _ = dbRootSetPasswordHandler(ctx, raw)
		requireSecretOnStdinOnly(t, execs(), "Mk-root-55d0aa")
	})

	t.Run("db.pma_admin.ensure", func(t *testing.T) {
		// The password is generated inside; fail the client so nothing is
		// written, and check that the IDENTIFIED BY clause is not on argv.
		execs := recordSQLExecs(t, [2]string{"jabali_pma_admin", "ERROR 1045: refused in test"})
		_, _ = dbPmaAdminEnsureHandler(ctx, nil)
		requireSecretOnStdinOnly(t, execs(), "IDENTIFIED BY")
	})

	t.Run("db.mysqladmin.ensure", func(t *testing.T) {
		execs := recordSQLExecs(t)
		raw, _ := json.Marshal(dbMysqladminEnsureParams{PanelUsername: "alice"})
		out, err := dbMysqladminEnsureHandler(ctx, raw)
		if err != nil {
			t.Fatalf("ensure: %v", err)
		}
		requireSecretOnStdinOnly(t, execs(), out.(dbMysqladminEnsureResponse).Password)
	})

	t.Run("scoped restore account", func(t *testing.T) {
		execs := recordSQLExecs(t)
		if err := provisionScopedShadow(ctx, "alice_shop", "jb_s_alice_shop", "Mk-shadow-0c1d"); err != nil {
			t.Fatalf("provision: %v", err)
		}
		requireSecretOnStdinOnly(t, execs(), "Mk-shadow-0c1d")
	})

	t.Run("system restore statement", func(t *testing.T) {
		execs := recordSQLExecs(t)
		stmt := "INSERT IGNORE INTO jabali_kratos.identity_credentials (config) VALUES ('Mk-hash-6e2b')"
		if err := runMariaDBStmt(ctx, stmt); err != nil {
			t.Fatalf("run: %v", err)
		}
		requireSecretOnStdinOnly(t, execs(), "Mk-hash-6e2b")
	})
}
