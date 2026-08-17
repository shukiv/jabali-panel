package commands

// M46 Step 1 — root / superuser password *alongside* socket/peer auth
// (ADR-0097). The panel/agent path keeps using unix_socket (MariaDB) /
// peer (Postgres); this only adds a break-glass password. The MariaDB
// path is the dangerous one: `ALTER USER ... IDENTIFIED VIA X OR Y`
// REMOVES any method not re-stated, so the statement lists unix_socket
// FIRST (so the socket path keeps winning) and the handler hard-asserts
// socket auth survived before reporting success.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type dbRootPasswordParams struct {
	NewPassword string `json:"new_password"`
}

type dbRootPasswordResponse struct {
	OK bool `json:"ok"`
}

const (
	mysqlRootPasswordFile   = "/etc/jabali-panel/mysql-root.password"
	pgSuperuserPasswordFile = "/etc/jabali-panel/postgres.password"
	dbSecretFileMode        = 0o640

	// mariadbRootAlterPrefix is the ONLY acceptable MariaDB root
	// auth statement (ADR-0097): unix_socket FIRST so the panel's
	// socket path keeps winning, mysql_native_password as backup.
	// Single source of truth; guarded by db_root_password_test.go.
	mariadbRootAlterPrefix = "ALTER USER 'root'@'localhost' IDENTIFIED VIA unix_socket OR mysql_native_password USING PASSWORD("
)

// writeSecretFile writes content to path as root:<jabali> 0640 via a
// temp file in the same dir + atomic rename (ADR-0097 B11). The group
// is the panel service account so panel-api (running as jabali) can
// read the break-glass credential.
func writeSecretFile(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".jabali-secret-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Chmod(tmpName, dbSecretFileMode); err != nil {
		return fmt.Errorf("chmod temp: %w", err)
	}
	// Best-effort chown to root:<service group>. If the group is
	// missing (non-standard install) keep root:root rather than fail
	// the rotation — the credential is still protected at 0640.
	if g, gerr := user.LookupGroup(serviceGroupName()); gerr == nil {
		if gid, perr := strconv.Atoi(g.Gid); perr == nil {
			_ = os.Chown(tmpName, 0, gid)
		}
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}

// serviceGroupName resolves the panel service group, defaulting to
// "jabali" (install.sh default SERVICE_USER).
func serviceGroupName() string {
	if v := os.Getenv("JABALI_SERVICE_USER"); v != "" {
		return v
	}
	return "jabali"
}

// dbRootSetPasswordHandler — MariaDB. Sets/rotates root@localhost's
// password while PRESERVING unix_socket auth (ADR-0097 mandatory form).
func dbRootSetPasswordHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbRootPasswordParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to parse params: %v", err)}
	}
	if p.NewPassword == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "new_password cannot be empty"}
	}
	escapedPw, err := EscapeMariaDBLiteral(p.NewPassword)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid password"}
	}

	// MANDATORY grammar (ADR-0097, Context7-verified MariaDB 10.4+):
	// unix_socket listed first so the panel's socket path keeps
	// winning; mysql_native_password as the break-glass backup.
	// Omitting unix_socket here would silently delete it.
	alter := mariadbRootAlterPrefix + escapedPw + ")"
	if err := execCommandContext(ctx, "mysql", "-e", alter).Run(); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "failed to set root password"}
	}

	// Guard (a): SHOW CREATE USER must still list unix_socket.
	out, err := execCommandContext(ctx, "mysql", "-N", "-B", "-e",
		"SHOW CREATE USER 'root'@'localhost'").CombinedOutput()
	socketPreserved := err == nil && strings.Contains(string(out), "unix_socket")

	// Guard (b): root must still authenticate over the socket.
	socketReachable := execCommandContext(ctx, "mysql", "--protocol=socket",
		"-e", "SELECT 1").Run() == nil

	if !socketPreserved || !socketReachable {
		// Restore socket-only auth — the safe state the panel + every
		// root-over-socket consumer (install.sh:1660) depends on.
		_ = execCommandContext(ctx, "mysql", "-e",
			"ALTER USER 'root'@'localhost' IDENTIFIED VIA unix_socket").Run()
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeFailedPrecondition,
			Message: "root password not applied: unix_socket auth would have been lost; reverted to socket-only",
		}
	}

	if err := writeSecretFile(mysqlRootPasswordFile, p.NewPassword+"\n"); err != nil {
		// Auth change already succeeded + verified; a secret-file
		// write failure is non-fatal to DB reachability but the
		// operator must know the file is stale.
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "password set but secret file write failed: " + err.Error()}
	}
	return dbRootPasswordResponse{OK: true}, nil
}

// dbPostgresSuperuserSetPasswordHandler — PostgreSQL. Sets the
// `postgres` role password. Peer auth on the socket is untouched (no
// pg_hba edit), so the panel's peer path keeps working. Largely
// exposes rotation of the already-existing postgres.password file.
func dbPostgresSuperuserSetPasswordHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbRootPasswordParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to parse params: %v", err)}
	}
	if p.NewPassword == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "new_password cannot be empty"}
	}
	// PG string literal: double single quotes. The password is a
	// VALUE inside ALTER ROLE ... PASSWORD '<lit>', not an identifier.
	escaped := strings.ReplaceAll(p.NewPassword, "'", "''")
	if err := pgRunSQL(ctx, fmt.Sprintf("ALTER ROLE postgres WITH PASSWORD '%s'", escaped)); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "failed to set postgres password"}
	}
	if err := writeSecretFile(pgSuperuserPasswordFile, p.NewPassword+"\n"); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "password set but secret file write failed: " + err.Error()}
	}
	return dbRootPasswordResponse{OK: true}, nil
}

func init() {
	Default.Register("db.root.set_password", dbRootSetPasswordHandler)
	Default.Register("db.postgres.superuser.set_password", dbPostgresSuperuserSetPasswordHandler)
}
