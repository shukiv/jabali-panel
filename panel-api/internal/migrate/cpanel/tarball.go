package cpanel

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
)

// ParsedTarball is the index produced by parsing a cpmove-<user>.tar.gz.
// File paths are absolute, pointing into the staging dir under
// /var/lib/jabali-migrations/<job>/extracted/. Restore-stage writers
// consume each slice (MySQLDumps → mariadb-import; ZoneFiles → pdns
// upsert; HomeDir → cp -a into /home/<target>; etc.).
type ParsedTarball struct {
	// SourceUser is the cPanel-side username taken from the tarball's
	// top-level directory (cp/<user>/...). Restore stage maps this
	// onto the operator-supplied target jabali username.
	SourceUser string
	// ExtractDir is the absolute root the entries were written under.
	ExtractDir string
	// HomeDir is .../homedir, the verbatim user home tree from the
	// source. Empty when the tarball didn't include a homedir.
	HomeDir string
	// MailRoot is the absolute path to the per-domain Maildir
	// parent. cpanel sets this to <HomeDir>/mail; DA sets it to
	// <HomeDir>/email (DA's layout). Empty defaults to
	// <HomeDir>/mail at ImportMailboxes time so cpanel callers
	// don't need to set it.
	MailRoot string
	// MySQLDumps lists every per-database SQL file. cPanel layout:
	// cp/<user>/mysql/<dbname>.sql.
	MySQLDumps []string
	// ZoneFiles lists BIND-format zone files from cp/<user>/dnszones/.
	ZoneFiles []string
	// DomainNames is the fallback domain list when ZoneFiles is empty
	// (DA + Hestia tarballs don't ship BIND zones). ImportDomains
	// iterates DomainNames in that case and creates panel domain rows
	// with synthesized docroots — DNS records are NOT auto-imported;
	// operator must hand-import zones or accept the empty panel
	// default zone. Populated by per-importer adapters (DA scans
	// <HomeDir>/domains/*; Hestia scans <HomeDir>/web/*).
	DomainNames []string
	// DocRoots optionally overrides the default docroot for a name in
	// DomainNames. DA: <HomeDir>/domains/<dom>/public_html on source
	// → /home/<target>/domains/<dom>/public_html in dest. Empty map
	// or missing key falls back to /home/<target>/public_html/<dom>.
	DocRoots map[string]string
	// OwnerEmail is the cpanel-owner's default mailbox address
	// (<sourceUser>@<primaryDomain>). When set, ImportMailboxes
	// passes it through to the agent so the mail/{cur,new,tmp,...}
	// tree at the top of the homedir mail/ root imports under this
	// address (cpanel's default-mailbox convention).
	OwnerEmail string
	// CronFiles lists per-user crontab files (cp/<user>/cron/<user>).
	CronFiles []string
	// SSHAuthorized lists authorized_keys files found under the
	// home tree (.ssh/authorized_keys). Restore upserts each line
	// after fingerprint dedup.
	SSHAuthorized []string
	// Skipped records areas the parser ignored (postgres dumps, mail
	// not in scope here, etc.). Used to populate the manifest's
	// Warnings slice.
	Skipped []string
	// CompatUsers, when set by a source adapter, are the ORIGINAL source
	// MySQL users to recreate with their native password hash (preserve-gated
	// in ImportDatabases). cPanel/WHM/CloudPanel/Plesk read these from the
	// cpmove `mysql.sql` grants inline; HestiaCP has no mysql.sql, so its
	// adapter pre-populates this from the backup's per-DB db.conf MD5= hashes
	// (GH #633).
	CompatUsers []CompatUser
}

// Limits parser behaviour. Tarballs from a malicious source could
// claim a 1 EB file; bound the writes so a runaway entry can't fill
// the staging volume. The cap is per-entry — total size grows with
// the source account, which is expected.
const (
	MaxEntrySize = 100 << 30 // 100 GiB per file in the tarball
)

// ParseTarball opens a cpmove-<user>.tar.gz and streams every entry
// into extractDir. Returns a populated ParsedTarball on success;
// caller is responsible for cleaning extractDir up on terminal job
// state (the restore-stage runner does this after success/failure).
func ParseTarball(tarballPath, extractDir string) (*ParsedTarball, error) {
	if tarballPath == "" || extractDir == "" {
		return nil, errors.New("ParseTarball: empty path")
	}
	if err := os.MkdirAll(extractDir, 0o750); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", extractDir, err)
	}
	if err := migrate.CheckExtractDiskSpace(tarballPath, extractDir); err != nil {
		return nil, err
	}

	f, err := os.Open(tarballPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", tarballPath, err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("gunzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	out := &ParsedTarball{ExtractDir: extractDir}

	// wrapperPrefix is the leading component(s) before `cp/`,
	// detected on the first cp/-containing entry. Some cPanel
	// backup tooling (operator wrapping a cpmove inside a dated
	// snapshot dir; older WHM scheduled-backup runs) emits
	// `<wrapper>/cp/<user>/...` instead of canonical
	// `cp/<user>/...`. We strip the wrapper for classification +
	// SourceUser detection so the per-area slices populate
	// regardless of wrapper depth. Real-world example surfaced in
	// QA: backup-1.22.2026_18-11-23_<user>/cp/<user>/...
	wrapperPrefix := ""
	wrapperDetected := false
	budget := NewExtractBudget()

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar read: %w", err)
		}

		// Refuse path-escape attempts. cpmove tarballs are well-
		// behaved but we treat them as untrusted: a compromised
		// source panel could embed `../../etc/passwd` to overwrite
		// host files outside extractDir.
		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") || filepath.IsAbs(clean) {
			out.Skipped = append(out.Skipped, "path_escape:"+hdr.Name)
			continue
		}

		// Detect wrapper prefix on first entry containing a `cp/`
		// segment. canonicalRel is the path the classifier sees +
		// SourceUser detection uses; on canonical tarballs it
		// equals `clean`; on wrapped tarballs it strips the
		// wrapper. Extraction still uses `clean` so disk layout
		// preserves the original tar structure.
		if !wrapperDetected {
			parts := strings.Split(clean, string(filepath.Separator))
			for i := 0; i+1 < len(parts); i++ {
				if parts[i] == "cp" {
					wrapperPrefix = strings.Join(parts[:i], string(filepath.Separator))
					if wrapperPrefix != "" {
						wrapperPrefix += string(filepath.Separator)
					}
					wrapperDetected = true
					break
				}
			}
		}
		canonicalRel := clean
		if wrapperPrefix != "" {
			canonicalRel = strings.TrimPrefix(clean, wrapperPrefix)
		}

		// Glean source user from canonical `cp/<user>/...` entry.
		if out.SourceUser == "" {
			parts := strings.Split(canonicalRel, string(filepath.Separator))
			if len(parts) >= 2 && parts[0] == "cp" {
				out.SourceUser = parts[1]
			}
		}

		dest := filepath.Join(extractDir, clean)

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, 0o750); err != nil {
				return nil, fmt.Errorf("mkdir %s: %w", dest, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if hdr.Size > MaxEntrySize {
				out.Skipped = append(out.Skipped, fmt.Sprintf("oversize:%s:%d", hdr.Name, hdr.Size))
				continue
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
				return nil, fmt.Errorf("mkdir parent of %s: %w", dest, err)
			}
			w, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
			if err != nil {
				return nil, fmt.Errorf("open %s: %w", dest, err)
			}
			// io.LimitReader doubles as the size enforcement —
			// even a header lying about Size can't write more
			// than MaxEntrySize bytes.
			if _, err := budget.Charge(w, tr); err != nil {
				_ = w.Close()
				return nil, fmt.Errorf("copy %s: %w", dest, err)
			}
			if err := w.Close(); err != nil {
				return nil, fmt.Errorf("close %s: %w", dest, err)
			}
			// classify uses canonicalRel (wrapper-stripped) so the
			// switch on parts[0]=="cp" matches when the tar
			// nests cp/ inside a wrapper dir. dest stays the
			// on-disk path (preserves wrapper layout) so the
			// per-area writers operate on the actual extracted
			// files.
			classify(out, canonicalRel, dest)
		case tar.TypeSymlink, tar.TypeLink:
			// cPanel tarballs occasionally hard-link inside the
			// homedir (legitimate). Skip both link types in v1 —
			// restore-stage will rebuild from materialised regular
			// files. Symlinks under .ssh/, .config/ etc are
			// uncommon and the panel's own user template is the
			// source of truth on those paths.
			out.Skipped = append(out.Skipped, "link:"+clean)
		default:
			// devices, fifos, chars — never legitimate inside cp/.
			out.Skipped = append(out.Skipped, fmt.Sprintf("typeflag:%c:%s", hdr.Typeflag, clean))
		}
	}

	if out.SourceUser == "" {
		return nil, errors.New("ParseTarball: no cp/<user>/ top-level — not a cpmove tarball?")
	}
	// HomeDir lookup respects the detected wrapper prefix + the
	// two cPanel layouts: pkgacct cpmove (cp/<user>/homedir/) and
	// full-backup wizard (homedir/ at top, cp/<user> as a file).
	candidates := []string{
		filepath.Join("cp", out.SourceUser, "homedir"),
		"homedir",
	}
	for _, rel := range candidates {
		if wrapperPrefix != "" {
			rel = filepath.Join(strings.TrimRight(wrapperPrefix, string(filepath.Separator)), rel)
		}
		if root := filepath.Join(extractDir, rel); existsDir(root) {
			out.HomeDir = root
			out.MailRoot = filepath.Join(root, "mail")
			break
		}
	}

	// OwnerEmail = <sourceUser>@<primaryDomain>. The cPanel "default"
	// / system account's mail lives in the TOP-LEVEL Maildir
	// (mail/cur, mail/new) rather than a per-domain subdir, and the
	// agent's migration.import_mailboxes only imports it when
	// OwnerEmail is non-empty (it keys the owner INBOX off this
	// address). Without this the owner's mail — every message a
	// customer using the default address ever received — is silently
	// dropped AND the manifest under-reports messages_found=0
	// (GH bug: 99 real messages in mail/cur, manifest said 0). The
	// primary domain comes from the cp/<user> account-config file's
	// DNS= field (same source peek.go uses for the pre-flight); the
	// userdata fallbacks mirror peek's lookup order.
	if out.SourceUser != "" {
		prefixed := func(rel string) string {
			if wrapperPrefix != "" {
				rel = filepath.Join(strings.TrimRight(wrapperPrefix, string(filepath.Separator)), rel)
			}
			return filepath.Join(extractDir, rel)
		}
		for _, candidate := range []string{
			prefixed(filepath.Join("cp", out.SourceUser)),
			prefixed("userdata"),
			prefixed(filepath.Join("userdata", "main")),
		} {
			if dom := extractKV(candidate, "DNS"); dom != "" {
				out.OwnerEmail = out.SourceUser + "@" + dom
				break
			}
		}
	}
	return out, nil
}

// classify slots a freshly-extracted file into the right area
// slice. Path is the cleaned tarball-relative path; abs is the
// on-disk extracted path.
//
// Two cPanel backup layouts handled:
//
//	pkgacct cpmove format (cp/<user>/...):
//	  cp/<user>/mysql/<db>.sql
//	  cp/<user>/dnszones/<dom>.db
//	  cp/<user>/cron/<user>
//	  cp/<user>/homedir/.ssh/authorized_keys
//	  cp/<user>/homedir/...
//
//	Full-backup wizard format (mysql/, dnszones/, homedir/ at root,
//	cp/<user> as a FILE not a directory):
//	  mysql/<db>.sql
//	  dnszones/<dom>.db
//	  cron (top-level file)
//	  homedir/.ssh/authorized_keys
//	  homedir/...
//	  cp/<user>     (single file with account config)
//
// Both formats classify identically — area name + extension. cpmove
// path uses parts[2:] (skip cp/<user>); full-backup uses parts[0:].
func classify(p *ParsedTarball, path, abs string) {
	parts := strings.Split(path, string(filepath.Separator))
	if len(parts) == 0 {
		return
	}

	var rest []string
	switch {
	case len(parts) >= 3 && parts[0] == "cp":
		user := parts[1]
		if user != p.SourceUser && p.SourceUser != "" {
			p.Skipped = append(p.Skipped, "foreign_user:"+path)
			return
		}
		rest = parts[2:]
	case isFullBackupAreaTop(parts[0]):
		rest = parts
	default:
		return
	}

	if len(rest) == 0 {
		return
	}
	switch rest[0] {
	case "mysql":
		if len(rest) >= 2 && strings.HasSuffix(rest[len(rest)-1], ".sql") {
			p.MySQLDumps = append(p.MySQLDumps, abs)
		}
	case "psql", "postgres":
		p.Skipped = append(p.Skipped, "postgres_unsupported:"+path)
	case "dnszones":
		if strings.HasSuffix(rest[len(rest)-1], ".db") {
			p.ZoneFiles = append(p.ZoneFiles, abs)
		}
	case "cron":
		p.CronFiles = append(p.CronFiles, abs)
	case "homedir":
		if len(rest) >= 3 && rest[len(rest)-2] == ".ssh" && rest[len(rest)-1] == "authorized_keys" {
			p.SSHAuthorized = append(p.SSHAuthorized, abs)
		}
	}
}

// isFullBackupAreaTop reports whether `top` is one of the area dir
// names cPanel's full-backup wizard places at the tarball root
// (vs cpmove which nests under cp/<user>/).
func isFullBackupAreaTop(top string) bool {
	switch top {
	case "mysql", "dnszones", "homedir", "cron", "psql", "postgres":
		return true
	}
	return false
}

func existsDir(p string) bool {
	st, err := os.Stat(p)
	if err != nil {
		return false
	}
	return st.IsDir()
}
