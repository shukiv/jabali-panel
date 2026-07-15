package plesk

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
)

// BackupUserTimeout bounds the source-side metadata build. The tarball is
// SMALL (meta + DB dumps only — docroots and Maildirs are manifested for
// separate rsync, never bundled), so this is generous.
const BackupUserTimeout = 60 * time.Minute

// BackupUser synthesises a cpanel-cpmove-shaped METADATA tarball on the
// Plesk source so the existing cpanel restore writers (migration.import_run)
// consume it unchanged — the same approach the directadmin importer uses.
//
// CRITICAL: the homedir (docroots) and Maildirs are NOT bundled. Bundling a
// multi-GB production account into /tmp would batter a live source box. We
// emit `domains-paths.txt` (docroot manifest) + `mail-paths.txt` (Maildir
// manifest); the restore stage rsyncs those directly source→dest so a
// transient failure resumes instead of re-tarring gigabytes.
//
// Produced layout: /tmp/cpmove-<slug>.tar.gz  (a few KB — metadata only)
//
//	cpmove-<slug>/
//	  cp/<slug>            CONTACTEMAIL / USER / DNS meta
//	  databases.txt        <db name> per line (dumped + streamed at restore)
//	  domains-paths.txt    <domain>\t<abs docroot>  (rsynced at restore)
//	  mail-paths.txt       <address>\t<abs Maildir> (rsynced at restore)
//	  dnszones/<dom>.db    stub per domain (ImportDomains needs the name)
//
// DB dumps are NOT bundled: a live production database can be many GB (a
// real box carried a 6.9 GB WordPress DB), so bundling a mysqldump into a
// /tmp tarball would batter the source and blow its disk. The restore stage
// streams each dump directly (mysqldump | ssh dest). Read access needs root
// (Plesk admin DB creds live at /etc/psa/.psa.shadow).
func (d *Discoverer) BackupUser(ctx context.Context, raw interface{}, account string) (string, error) {
	s, ok := raw.(*session)
	if !ok || s == nil {
		return "", errors.New("BackupUser: bad session")
	}
	if !looksLikePleskSubscription(account) {
		return "", fmt.Errorf("BackupUser: invalid subscription %q", account)
	}

	subctx, cancel := context.WithTimeout(ctx, BackupUserTimeout)
	defer cancel()

	acct := shellQuote(account)
	slug := pleskSlug(account)
	script := fmt.Sprintf(`set -e
SUB=%s
SLUG=%s
TMP=/tmp/jabali-migrate-plesk-$SLUG
OUT=/tmp/cpmove-$SLUG.tar.gz
VHOST=/var/www/vhosts/$SUB
MAILROOT=/var/qmail/mailnames/$SUB

rm -rf "$TMP" "$OUT"
mkdir -p "$TMP/cpmove-$SLUG/cp" "$TMP/cpmove-$SLUG/dnszones"

# 1. cp/<slug> — meta the cpanel PeekAccountMeta reader expects.
{
  echo "USER=$SLUG"
  echo "DNS=$SUB"
  OWNER_EMAIL=$(plesk db -Ne "SELECT c.email FROM domains d JOIN clients c ON d.cl_id=c.id WHERE d.name='$SUB' LIMIT 1" 2>/dev/null || true)
  [ -n "$OWNER_EMAIL" ] && echo "CONTACTEMAIL=$OWNER_EMAIL"
} > "$TMP/cpmove-$SLUG/cp/$SLUG"

# 2. databases.txt — MANIFEST only (one db name per line). The dumps are
#    streamed directly at restore (mysqldump | ssh dest); bundling a
#    multi-GB dump into this /tmp tarball would batter the source. We only
#    validate that admin DB creds resolve so restore won't surprise-fail.
if [ -r /etc/psa/.psa.shadow ] && MYSQL_PWD="$(cat /etc/psa/.psa.shadow)" mysql -uadmin -BN -e "SELECT 1" >/dev/null 2>&1; then
  :
elif mysql -uroot -BN -e "SELECT 1" >/dev/null 2>&1; then
  :
else
  echo "WARN: no MySQL admin creds (tried /etc/psa/.psa.shadow, root) — restore DB dumps will be skipped" >&2
fi
plesk db -Ne "SELECT db.name FROM data_bases db JOIN domains d ON db.dom_id=d.id WHERE d.name='$SUB'" 2>/dev/null \
| awk 'NF' > "$TMP/cpmove-$SLUG/databases.txt" || true

# 3. domains-paths.txt — every domain of the subscription + its docroot,
#    for restore-time rsync (NOT bundled here).
plesk db -Ne "SELECT name FROM domains WHERE name='$SUB' OR webspace_id=(SELECT id FROM domains WHERE name='$SUB' LIMIT 1)" 2>/dev/null \
| while read -r DOM; do
    [ -z "$DOM" ] && continue
    DR="/var/www/vhosts/$DOM/httpdocs"
    [ -d "$DR" ] && printf '%%s\t%%s\n' "$DOM" "$DR" >> "$TMP/cpmove-$SLUG/domains-paths.txt"
    touch "$TMP/cpmove-$SLUG/dnszones/$DOM.db"
  done

# 4. mail-paths.txt — every mailbox Maildir, for restore-time rsync +
#    migration.import_mailboxes (NOT bundled here).
if [ -d "$MAILROOT" ]; then
  for LP in "$MAILROOT"/*; do
    [ -d "$LP/Maildir" ] || continue
    LOCAL=$(basename "$LP")
    printf '%%s\t%%s\n' "$LOCAL@$SUB" "$LP/Maildir" >> "$TMP/cpmove-$SLUG/mail-paths.txt"
  done
fi

# 5. tar the METADATA only (small).
tar -czf "$OUT" -C "$TMP" "cpmove-$SLUG"
rm -rf "$TMP"
echo "$OUT"
`, acct, shellQuote(slug))

	out, err := s.run(subctx, BackupUserTimeout, script)
	if err != nil {
		return "", fmt.Errorf("plesk synthesize cpmove: %w (stdout=%q)", err, truncForLog(string(out), 1024))
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	path := strings.TrimSpace(lines[len(lines)-1])
	if path == "" {
		return "", errors.New("plesk synthesize cpmove: empty path")
	}
	return path, nil
}

// PullFile streams a source-side file to a local path via the shared SSH
// helper (mirrors directadmin/cpanel PullFile).
func (d *Discoverer) PullFile(ctx context.Context, raw interface{}, remotePath, localPath string) (int64, error) {
	s, ok := raw.(*session)
	if !ok || s == nil {
		return 0, errors.New("PullFile: bad session")
	}
	return migrate.PullFileViaSSH(ctx, s.client, remotePath, localPath)
}

// RemoveRemote deletes the source-side backup archive after it's pulled.
// Allowlist: only /tmp/cpmove-* with no traversal — a metadata backup
// (DB dumps, contact email) must not linger on the source.
func (d *Discoverer) RemoveRemote(ctx context.Context, raw interface{}, path string) error {
	s, ok := raw.(*session)
	if !ok || s == nil {
		return errors.New("RemoveRemote: bad session")
	}
	if !strings.HasPrefix(path, "/tmp/cpmove-") || strings.Contains(path, "..") {
		return fmt.Errorf("RemoveRemote: refuses non-cpmove path %q", path)
	}
	_, err := s.run(ctx, 30*time.Second, "rm -f "+shellQuote(path))
	return err
}

// looksLikePleskSubscription accepts a hostname-shaped subscription name
// (letters, digits, dot, hyphen). Rejects anything that could break out of
// the shell interpolation even though it is single-quoted downstream.
func looksLikePleskSubscription(s string) bool {
	if len(s) < 1 || len(s) > 253 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

// pleskSlug turns a subscription domain into a filesystem-safe slug for the
// cpmove archive name (dots/hyphens → underscore).
func pleskSlug(sub string) string {
	repl := strings.NewReplacer(".", "_", "-", "_")
	return repl.Replace(strings.ToLower(sub))
}

// truncForLog bounds surfaced stdout/stderr in error envelopes.
func truncForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...[truncated]"
}

// Slug exposes the subscription→archive-slug mapping to the pull/import
// wiring (which locates cpmove-<slug>/ inside the extracted tree).
func Slug(sub string) string { return pleskSlug(sub) }
