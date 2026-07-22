package cloudpanel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
)

// BackupUserTimeout caps the source-side synthesize. CloudPanel accounts are
// web-only (no mail tree to pack), so this is dominated by the mysqldump(s);
// 60 min matches the other sources for symmetry on very large databases.
const BackupUserTimeout = 60 * time.Minute

// BackupUser synthesizes a cpanel-cpmove-shaped tarball on the CloudPanel
// source so the shared cpanel restore writers (ImportDatabases, ImportCron,
// ImportSSHKeys, ImportDomains) consume it unchanged — the same approach the
// directadmin source takes (directadmin/backup.go). CloudPanel is web-only, so
// the produced layout is a strict subset of the cpanel one:
//
//	/tmp/cpmove-<user>.tar.gz
//	└── cpmove-<user>/
//	    ├── cp/<user>            (USER + primary-domain DNS line)
//	    ├── mysql/<db>.sql        (one per site database)
//	    ├── cron/<user>           (reconstructed crontab lines)
//	    ├── domains-paths.txt     (<domain>\t<docroot> for the restore rsync)
//	    └── homedir/.ssh/authorized_keys
//
// No dnszones/ or mail (CloudPanel has neither). The homedir file tree is NOT
// bundled — like directadmin, the restore stage rsyncs each docroot listed in
// domains-paths.txt directly source→dest so a retry resumes instead of
// re-pulling. Inventory is read from CloudPanel's SQLite config DB (read-only);
// the only mutating-looking command is mysqldump against a scratch dump file.
//
// Requires a root SSH user: read access to db.sq3 + MariaDB root-socket auth
// for mysqldump (CloudPanel installs MariaDB with unix_socket root, so
// `mysql -u root` over the local socket needs no password).
func (d *Discoverer) BackupUser(ctx context.Context, raw interface{}, account string) (string, error) {
	s, ok := raw.(*session)
	if !ok || s == nil {
		return "", errors.New("BackupUser: bad session")
	}
	if !siteUserRe.MatchString(account) {
		return "", fmt.Errorf("BackupUser: invalid account %q", account)
	}

	subctx, cancel := context.WithTimeout(ctx, BackupUserTimeout)
	defer cancel()

	acct := shellSingleQuote(account)
	dbp := shellSingleQuote(s.dbPath)
	// Single inline script (one ssh exec: one handshake, one tempdir).
	// `set -e` aborts on any hard failure so partial tarballs don't leak —
	// every conditional uses explicit if/fi (never a bare `[ ] && cmd`, which
	// would abort the whole script when the test is false under set -e).
	script := fmt.Sprintf(`set -e
ACCT=%s
DB=%s
TMP=/tmp/jabali-migrate-clp-$ACCT
OUT=/tmp/cpmove-$ACCT.tar.gz
Q(){ sqlite3 -readonly -separator '|' "$DB" "$1"; }

rm -rf "$TMP" "$OUT"
BASE="$TMP/cpmove-$ACCT"
mkdir -p "$BASE/cp" "$BASE/mysql" "$BASE/cron" "$BASE/homedir/.ssh"

# 1. cp/<user> — USER + primary-domain DNS line. CloudPanel has no per-site
#    contact email (site owner is a Linux user, not a panel user), so
#    CONTACTEMAIL is left to the operator's --target-email.
echo "USER=$ACCT" > "$BASE/cp/$ACCT"
PRIMARY=$(Q "SELECT domain_name FROM site WHERE user='$ACCT' ORDER BY id LIMIT 1")
if [ -n "$PRIMARY" ]; then echo "DNS=$PRIMARY" >> "$BASE/cp/$ACCT"; fi

# 2. mysql/<db>.sql — dump every database owned by the account's sites.
#    Root-socket auth (CloudPanel MariaDB uses unix_socket root).
MYSQL_AUTH=""
if mysql -u root -BN -e "SELECT 1" >/dev/null 2>&1; then MYSQL_AUTH="-u root"; fi
if [ -z "$MYSQL_AUTH" ]; then echo "WARN: no MariaDB root-socket auth — skipping DB dumps" >&2; fi
Q "SELECT d.name FROM \"database\" d JOIN site s ON d.site_id=s.id WHERE s.user='$ACCT'" | while read -r db; do
  if [ -z "$db" ]; then continue; fi
  if [ -n "$MYSQL_AUTH" ]; then
    if ! mysqldump $MYSQL_AUTH --skip-lock-tables --single-transaction "$db" > "$BASE/mysql/$db.sql" 2>/dev/null; then
      echo "WARN: mysqldump $db failed" >&2
      rm -f "$BASE/mysql/$db.sql"
    fi
  fi
done

# 3. domains-paths.txt — <domain>\t<docroot> so the restore stage rsyncs each
#    site's files. CloudPanel docroot = /home/<user>/htdocs/<root_directory>.
Q "SELECT domain_name, root_directory FROM site WHERE user='$ACCT' ORDER BY id" | while IFS='|' read -r dom root; do
  if [ -z "$dom" ]; then continue; fi
  printf '%%s\t/home/%%s/htdocs/%%s\n' "$dom" "$ACCT" "$root" >> "$BASE/domains-paths.txt"
done

# 4. cron/<user> — reconstruct crontab lines from cron_job (5 schedule fields
#    + command). cpanel.ImportCron reads cp/<user>/cron/<user>... but the
#    cpmove convention also accepts cron/<user>; keep it flat here.
Q "SELECT cj.minute||' '||cj.hour||' '||cj.day||' '||cj.month||' '||cj.weekday||' '||cj.command FROM cron_job cj JOIN site s ON cj.site_id=s.id WHERE s.user='$ACCT' ORDER BY cj.id" > "$BASE/cron/$ACCT" 2>/dev/null || true
if [ ! -s "$BASE/cron/$ACCT" ]; then rm -f "$BASE/cron/$ACCT"; fi

# 5. homedir/.ssh/authorized_keys — from ssh_user.ssh_keys (authorized_keys
#    blob). cpanel.ImportSSHKeys dedups by fingerprint on restore.
Q "SELECT su.ssh_keys FROM ssh_user su JOIN site s ON su.site_id=s.id WHERE s.user='$ACCT' AND su.ssh_keys IS NOT NULL AND su.ssh_keys != ''" > "$BASE/homedir/.ssh/authorized_keys" 2>/dev/null || true
if [ ! -s "$BASE/homedir/.ssh/authorized_keys" ]; then rm -f "$BASE/homedir/.ssh/authorized_keys"; fi

# 6. tar it up (last stdout line = the path the caller reads).
tar -czf "$OUT" -C "$TMP" "cpmove-$ACCT"
rm -rf "$TMP"
echo "$OUT"
`, acct, dbp)

	out, err := s.run(subctx, BackupUserTimeout, script)
	if err != nil {
		return "", fmt.Errorf("cloudpanel synthesize cpmove: %w (stdout=%q)", err, truncForLog(string(out), 1024))
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	path := strings.TrimSpace(lines[len(lines)-1])
	if path == "" {
		return "", errors.New("cloudpanel synthesize cpmove: empty tarball path")
	}
	return path, nil
}

// PullFile streams a source-side file to a local path over the session's SSH
// client (shared migrate helper). Same shape as the other sources' PullFile so
// the pull orchestrator branches on kind without special-casing args.
func (d *Discoverer) PullFile(ctx context.Context, raw interface{}, remotePath, localPath string) (int64, error) {
	s, ok := raw.(*session)
	if !ok || s == nil {
		return 0, errors.New("PullFile: bad session")
	}
	return migrate.PullFileViaSSH(ctx, s.client, remotePath, localPath)
}

// RemoveRemote deletes the synthesized source-side tarball after it's pulled
// (JAB-50). Allowlist: only /tmp/cpmove-* (what BackupUser writes) with no ..
// traversal — a full account archive (files list, DB dumps) must not linger on
// a possibly-shared source host.
func (d *Discoverer) RemoveRemote(ctx context.Context, raw interface{}, path string) error {
	s, ok := raw.(*session)
	if !ok || s == nil {
		return errors.New("RemoveRemote: bad session")
	}
	if !strings.HasPrefix(path, "/tmp/cpmove-") || strings.Contains(path, "..") {
		return fmt.Errorf("RemoveRemote: refuses non-cpmove path %q", path)
	}
	_, err := s.run(ctx, 30*time.Second, fmt.Sprintf("rm -f %s", shellSingleQuote(path)))
	return err
}

// truncForLog bounds stderr/stdout surfaced in error messages so a stuck
// mysqldump producing MB of output doesn't blow up the JSON envelope.
func truncForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...[truncated]"
}
