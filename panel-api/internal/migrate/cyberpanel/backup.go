package cyberpanel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
)

// BackupUserTimeout caps the source-side synthesize. Dominated by the
// mysqldump(s); 60 min matches the other sources for very large databases.
const BackupUserTimeout = 60 * time.Minute

// BackupUser synthesizes a cpanel-cpmove-shaped tarball on the CyberPanel
// source so the shared cpanel restore writers consume it unchanged — the same
// approach cloudpanel/directadmin take. This first step covers the web + data
// subset (DNS zones + mailboxes land in a follow-up step):
//
//	/tmp/cpmove-<domain>.tar.gz
//	└── cpmove-<domain>/
//	    ├── cp/<domain>           (USER + CONTACTEMAIL + primary-domain DNS line)
//	    ├── mysql/<db>.sql         (one per site database)
//	    ├── cron/<domain>          (the externalApp user's crontab)
//	    ├── domains-paths.txt      (<domain>\t<docroot> for the restore rsync)
//	    └── homedir/.ssh/authorized_keys
//
// cp/ is archived FIRST so cpanel.ParseTarball detects the cpmove-<domain>/
// wrapper before any area entry (its wrapper detection is lazy on the first
// cp/ segment; an area seen earlier would be misclassified + silently dropped).
//
// The CyberPanel account IS the primary domain (the Linux user that owns
// /home/<domain>/ is externalApp). Inventory is read from the CyberPanel MySQL
// database (read-only SELECTs); the only write-shaped command is mysqldump to a
// scratch file. Requires a root SSH user (MySQL access + read of
// /home/<domain>/ + the externalApp crontab).
func (d *Discoverer) BackupUser(ctx context.Context, raw interface{}, account string) (string, error) {
	s, ok := raw.(*session)
	if !ok || s == nil {
		return "", errors.New("BackupUser: bad session")
	}
	if !domainRe.MatchString(account) {
		return "", fmt.Errorf("BackupUser: invalid account %q", account)
	}

	subctx, cancel := context.WithTimeout(ctx, BackupUserTimeout)
	defer cancel()

	acct := shellSingleQuote(account)
	dbn := shellSingleQuote(s.dbName)
	// One inline script (single ssh exec). `set -e` aborts on hard failure so
	// partial tarballs don't leak; every conditional uses explicit if/fi (never
	// a bare `[ ] && cmd`, which aborts the whole script when the test is false
	// under set -e). externalApp is re-validated in-shell against the same
	// allow-list before it reaches `crontab -u`.
	script := fmt.Sprintf(`set -e
ACCT=%s
DBN=%s
TMP=/tmp/jabali-migrate-cyber-$ACCT
OUT=/tmp/cpmove-$ACCT.tar.gz
MYQ(){ mysql -N -B "$DBN" -e "$1"; }

rm -rf "$TMP" "$OUT"
BASE="$TMP/cpmove-$ACCT"
mkdir -p "$BASE/cp" "$BASE/mysql" "$BASE/cron" "$BASE/homedir/.ssh"

# Resolve the website's id (for db/domain queries) + externalApp (Linux user
# that owns /home/<domain> + holds the crontab).
WID=$(MYQ "SELECT id FROM websiteFunctions_websites WHERE domain='$ACCT'")
EXTAPP=$(MYQ "SELECT externalApp FROM websiteFunctions_websites WHERE domain='$ACCT'")
ADMINEMAIL=$(MYQ "SELECT adminEmail FROM websiteFunctions_websites WHERE domain='$ACCT'" 2>/dev/null || true)
if [ -z "$WID" ]; then echo "ERROR: no website row for $ACCT" >&2; exit 3; fi

# 1. cp/<domain> — USER + CONTACTEMAIL + primary-domain DNS line.
echo "USER=$ACCT" > "$BASE/cp/$ACCT"
echo "DNS=$ACCT" >> "$BASE/cp/$ACCT"
if [ -n "$ADMINEMAIL" ]; then echo "CONTACTEMAIL=$ADMINEMAIL" >> "$BASE/cp/$ACCT"; fi

# 2. mysql/<db>.sql — dump every database owned by this website. CyberPanel's
#    root MySQL auth is ambient for the SSH root user (same as discover's mysql
#    queries), so no credential args are passed.
MYSQL_OK=""
if mysql -N -e "SELECT 1" >/dev/null 2>&1; then MYSQL_OK=1; fi
if [ -z "$MYSQL_OK" ]; then echo "WARN: no MySQL access — skipping DB dumps" >&2; fi
MYQ "SELECT dbName FROM databases_databases WHERE website_id=$WID" | while read -r db; do
  if [ -z "$db" ]; then continue; fi
  if [ -n "$MYSQL_OK" ]; then
    if ! mysqldump --skip-lock-tables --single-transaction "$db" > "$BASE/mysql/$db.sql" 2>/dev/null; then
      echo "WARN: mysqldump $db failed" >&2
      rm -f "$BASE/mysql/$db.sql"
    fi
  fi
done

# 3. domains-paths.txt — <domain>\t<docroot> for the restore-stage rsync.
#    Primary + child + alias domains; CyberPanel docroot = /home/<domain>/public_html.
printf '%%s\t/home/%%s/public_html\n' "$ACCT" "$ACCT" >> "$BASE/domains-paths.txt"
MYQ "SELECT domain FROM websiteFunctions_childdomains WHERE master_id=$WID" | while read -r cd; do
  if [ -z "$cd" ]; then continue; fi
  printf '%%s\t/home/%%s/public_html\n' "$cd" "$ACCT" >> "$BASE/domains-paths.txt"
done
MYQ "SELECT aliasDomain FROM websiteFunctions_aliasdomains WHERE master_id=$WID" | while read -r ad; do
  if [ -z "$ad" ]; then continue; fi
  printf '%%s\t/home/%%s/public_html\n' "$ad" "$ACCT" >> "$BASE/domains-paths.txt"
done

# 4. cron/<domain> — the externalApp user's crontab. Guard externalApp against
#    the same allow-list the Go side enforces before it reaches crontab -u.
if printf '%%s' "$EXTAPP" | grep -Eq '^[a-z_][a-z0-9._-]{0,63}$'; then
  crontab -u "$EXTAPP" -l > "$BASE/cron/$ACCT" 2>/dev/null || true
  if [ ! -s "$BASE/cron/$ACCT" ]; then rm -f "$BASE/cron/$ACCT"; fi
fi

# 5. homedir/.ssh/authorized_keys — CyberPanel keeps the site home at
#    /home/<domain>/; copy the account's authorized_keys if present.
if [ -r "/home/$ACCT/.ssh/authorized_keys" ]; then
  cp "/home/$ACCT/.ssh/authorized_keys" "$BASE/homedir/.ssh/authorized_keys"
fi
if [ ! -s "$BASE/homedir/.ssh/authorized_keys" ]; then rm -f "$BASE/homedir/.ssh/authorized_keys"; fi

# 6. tar it up. cp/ FIRST (ParseTarball wrapper detection — see above), then
#    the areas that exist. Last stdout line = the path the caller reads.
TARARGS="cpmove-$ACCT/cp"
for d in mysql cron homedir domains-paths.txt; do
  if [ -e "$BASE/$d" ]; then TARARGS="$TARARGS cpmove-$ACCT/$d"; fi
done
tar -czf "$OUT" -C "$TMP" $TARARGS
rm -rf "$TMP"
echo "$OUT"
`, acct, dbn)

	out, err := s.run(subctx, BackupUserTimeout, script)
	if err != nil {
		return "", fmt.Errorf("cyberpanel synthesize cpmove: %w (stdout=%q)", err, truncForLog(string(out), 1024))
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	path := strings.TrimSpace(lines[len(lines)-1])
	if path == "" {
		return "", errors.New("cyberpanel synthesize cpmove: empty tarball path")
	}
	return path, nil
}

// PullFile streams a source-side file to a local path over the session's SSH
// client (shared migrate helper).
func (d *Discoverer) PullFile(ctx context.Context, raw interface{}, remotePath, localPath string) (int64, error) {
	s, ok := raw.(*session)
	if !ok || s == nil {
		return 0, errors.New("PullFile: bad session")
	}
	return migrate.PullFileViaSSH(ctx, s.client, remotePath, localPath)
}

// RemoveRemote deletes the synthesized source-side tarball after it's pulled
// (JAB-50). Allowlist: only /tmp/cpmove-* with no .. traversal — a full account
// archive must not linger on a possibly-shared source host.
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

// truncForLog bounds stderr/stdout surfaced in error messages.
func truncForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...[truncated]"
}
