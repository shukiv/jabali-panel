package commands

import (
	"context"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/dbreserve"
)

// enrichDatabaseUserAuth fills NativePasswordHash on each MariaDB database
// user in meta from MariaDB's own account table (GH #1993). The panel keeps
// only its own hash of the password, so without this a restore on another
// server rebuilt the database user's panel row but had nothing to recreate
// the MariaDB account with, and the site's database login failed. Same
// agent-side posture as enrichFtpCredentials: a user whose name fails
// validation, or whose account uses another auth plugin, is left without a
// hash (the restore then gives it a new password) rather than failing the
// backup.
func enrichDatabaseUserAuth(ctx context.Context, meta *backup.AccountMetadata) error {
	if meta == nil || len(meta.DatabaseUsers) == 0 {
		return nil
	}
	want := map[string]bool{}
	var literals []string
	for _, du := range meta.DatabaseUsers {
		if du.Engine != "" && du.Engine != "mariadb" {
			continue
		}
		if want[du.Username] || !dbUserNameRegex.MatchString(du.Username) || dbreserve.User(du.Username) {
			continue
		}
		lit, err := EscapeMariaDBLiteral(du.Username)
		if err != nil {
			continue
		}
		want[du.Username] = true
		literals = append(literals, lit)
	}
	if len(literals) == 0 {
		return nil
	}
	q := "SELECT User, authentication_string FROM mysql.user WHERE Host='localhost' " +
		"AND plugin IN ('mysql_native_password','') AND User IN (" + strings.Join(literals, ",") + ")"
	out, err := execCommandContext(ctx, "mysql", "-N", "-B", "-e", q).Output()
	if err != nil {
		return fmt.Errorf("read the MariaDB accounts: %w", err)
	}
	hashes := map[string]string{}
	for _, ln := range strings.Split(string(out), "\n") {
		user, hash, ok := strings.Cut(strings.TrimRight(ln, "\r"), "\t")
		if ok && want[user] && nativePwdHashRe.MatchString(hash) {
			hashes[user] = hash
		}
	}
	for i := range meta.DatabaseUsers {
		if h, ok := hashes[meta.DatabaseUsers[i].Username]; ok && want[meta.DatabaseUsers[i].Username] {
			meta.DatabaseUsers[i].NativePasswordHash = h
		}
	}
	return nil
}

// enrichPostgresRoleAuth fills PostgresPasswordVerifier on each PostgreSQL
// database user in meta from pg_authid, as enrichDatabaseUserAuth does for
// MariaDB (GH #1993). Only a SCRAM-SHA-256 verifier is kept: an md5 one is
// salted with the role's name, so it breaks on a restore that renames the
// role. The query names no role, so no SQL is built from the bundle's names.
func enrichPostgresRoleAuth(ctx context.Context, meta *backup.AccountMetadata) error {
	if meta == nil {
		return nil
	}
	want := map[string]bool{}
	for _, du := range meta.DatabaseUsers {
		if du.Engine == "postgres" && pgValidIdent(du.Username) && du.Username != "postgres" && !dbreserve.User(du.Username) {
			want[du.Username] = true
		}
	}
	if len(want) == 0 {
		return nil
	}
	out, err := execCommandContext(ctx, "sudo", "-u", "postgres", "psql", "-XAtq", "-F", "\t", "-c",
		"SELECT rolname, rolpassword FROM pg_authid WHERE rolcanlogin AND rolpassword IS NOT NULL").Output()
	if err != nil {
		return fmt.Errorf("read the PostgreSQL roles: %w", err)
	}
	verifiers := map[string]string{}
	for _, ln := range strings.Split(string(out), "\n") {
		role, v, ok := strings.Cut(strings.TrimRight(ln, "\r"), "\t")
		if ok && want[role] && backup.IsPostgresSCRAMVerifier(v) {
			verifiers[role] = v
		}
	}
	for i := range meta.DatabaseUsers {
		du := &meta.DatabaseUsers[i]
		if v, ok := verifiers[du.Username]; ok && du.Engine == "postgres" {
			du.PostgresPasswordVerifier = v
		}
	}
	return nil
}
