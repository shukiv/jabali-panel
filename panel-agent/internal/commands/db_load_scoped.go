package commands

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// JAB-239: tenant SQL restores used to pipe the dump straight into the
// mariadb client running as ROOT — every statement in a tenant-supplied
// dump executed with full privileges on both the database (cross-DB
// reads/writes, CREATE USER, GRANT) and the host (the client's \!
// shell escape gives a root shell). This file loads dumps through a
// db-scoped MariaDB account running as an unprivileged OS user instead:
//
//   - a per-database shadow account jb_s_<db>@localhost is (re)created
//     with a fresh random password, GRANTed ALL PRIVILEGES on exactly
//     the target database (no GRANT OPTION, no SUPER/SET_USER_ID), the
//     load runs as that account, and the password is then forgotten —
//     nobody can log in as it, it only exists to scope the load and to
//     resolve DEFINERs;
//   - the account is durable (not dropped after the load): panel dumps
//     carry --routines --triggers --events, and those objects execute
//     with their DEFINER's privileges — pointing them at a dropped
//     account would break the app at runtime (error 1449). db.drop
//     removes the shadow alongside the database;
//   - DEFINER=`u`@`h` clauses in the stream are rewritten to the
//     shadow account. The shadow has privileges on this database only,
//     so a SQL SECURITY DEFINER object can't exceed tenant scope, and
//     cross-database views die at execution time — exactly what a
//     tenant-scoped restore should do. (cPanel-style restores rewrite
//     DEFINER for the same reason.)
//   - the client runs with the target OS user's UID/GID set directly
//     on the child process (SysProcAttr.Credential — no sudo, which
//     would scrub MYSQL_PWD under env_reset), so \! and any
//     client-side file access land on an account that can touch
//     nothing. This is the same privilege-drop idea as the postgres
//     restore branch's `sudo -u postgres` in backup_restore.go.

// scopedRestoreOSUser is the OS account the mariadb client runs as.
const scopedRestoreOSUser = "nobody"

// definerClauseRegex matches ` DEFINER=`user`@`host`` including the
// versioned-comment form mysqldump/mariadb-dump emit for triggers
// (/*!50017 DEFINER=... */). Case-insensitive: dumps write it
// upper-case, hand-made dumps may not.
var definerClauseRegex = regexp.MustCompile("(?i)[ \\t]+DEFINER[ \\t]*=[ \\t]*`[^`]*`@`[^`]*`")

// scopedShadowUser derives the durable per-database shadow account
// name. 5 + 64 chars max — comfortably under MariaDB's 80-char limit.
func scopedShadowUser(dbName string) string {
	return "jb_s_" + dbName
}

// definerRewritingReader wraps a dump stream and rewrites DEFINER
// clauses to the shadow account line by line. Dump files are
// line-oriented and mariadb-dump never splits a DEFINER clause across
// lines, so a line filter is exact for real-world dumps.
type definerRewritingReader struct {
	r    *bufio.Reader
	repl []byte
	buf  []byte // pending output of the current (rewritten) line
	err  error  // sticky terminal error (io.EOF included)
}

func newDefinerRewritingReader(r io.Reader, shadowUser string) *definerRewritingReader {
	return &definerRewritingReader{
		r:    bufio.NewReaderSize(r, 256*1024),
		repl: []byte(" DEFINER=`" + shadowUser + "`@`localhost`"),
	}
}

func (s *definerRewritingReader) Read(p []byte) (int, error) {
	for len(s.buf) == 0 && s.err == nil {
		line, err := s.r.ReadBytes('\n')
		if len(line) > 0 {
			s.buf = definerClauseRegex.ReplaceAll(line, s.repl)
		}
		if err != nil {
			s.err = err
		}
	}
	if len(s.buf) == 0 {
		// Nothing left to emit; report the sticky error (io.EOF on a
		// clean end of stream).
		if s.err != nil {
			return 0, s.err
		}
		return 0, io.EOF
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}

// mariadbRoot runs a statement as root over the unix socket. Only used
// for the static, operator-side provisioning statements — dump content
// never passes through here.
func mariadbRoot(ctx context.Context, stmt string) error {
	cmd := exec.CommandContext(ctx, "mariadb",
		"--protocol=socket", "--socket=/run/mysqld/mysqld.sock",
		"-e", stmt)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// loadMariaDBDumpScoped streams dump into dbName through the db-scoped
// shadow account as an unprivileged OS user. dbName must already be
// validated by the caller (the dbRestoreNameRegex shape) — it is
// interpolated into the GRANT statement and the shadow account name.
// The target database must exist; the callers' reset/create preludes
// handle that.
func loadMariaDBDumpScoped(ctx context.Context, dbName string, dump io.Reader) error {
	shadow := scopedShadowUser(dbName)
	pb := make([]byte, 18)
	if _, err := rand.Read(pb); err != nil {
		return fmt.Errorf("mint scoped restore password: %w", err)
	}
	pwd := hex.EncodeToString(pb)
	// Hex-only password, regex-validated db name — the interpolated
	// statements can't break out of their quoting. ALTER re-randomizes
	// the password on every restore, so even a captured credential is
	// single-use. A concurrent restore of the same database only
	// re-randomizes the password again — already-established sessions
	// are unaffected, so concurrent loads don't kill each other.
	prov := fmt.Sprintf(
		"CREATE USER IF NOT EXISTS '%s'@'localhost' IDENTIFIED BY '%s'; "+
			"ALTER USER '%s'@'localhost' IDENTIFIED BY '%s'; "+
			"GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'localhost';",
		shadow, pwd, shadow, pwd, dbName, shadow)
	if err := mariadbRoot(ctx, prov); err != nil {
		return fmt.Errorf("provision scoped restore account: %w", err)
	}

	// Resolve the unprivileged OS account the client runs as.
	osUser, err := user.Lookup(scopedRestoreOSUser)
	if err != nil {
		return fmt.Errorf("lookup %s: %w", scopedRestoreOSUser, err)
	}
	uid, err := strconv.ParseUint(osUser.Uid, 10, 32)
	if err != nil {
		return fmt.Errorf("parse %s uid: %w", scopedRestoreOSUser, err)
	}
	gid, err := strconv.ParseUint(osUser.Gid, 10, 32)
	if err != nil {
		return fmt.Errorf("parse %s gid: %w", scopedRestoreOSUser, err)
	}

	cmd := exec.CommandContext(ctx, "mariadb",
		"--protocol=socket", "--socket=/run/mysqld/mysqld.sock",
		"--user="+shadow,
		"--database="+dbName)
	// Drop to the unprivileged OS user directly on the child — sudo is
	// deliberately NOT used: env_reset would scrub MYSQL_PWD off the
	// environment (found live during the JAB-239 box verification:
	// "Access denied ... (using password: NO)").
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)},
	}
	// MYSQL_PWD keeps the password off argv (visible in ps to every
	// tenant on the box). /proc/<pid>/environ is readable only by the
	// process owner and root, and the credential is single-use and
	// db-scoped regardless.
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+pwd)
	cmd.Stdin = newDefinerRewritingReader(dump, shadow)

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
