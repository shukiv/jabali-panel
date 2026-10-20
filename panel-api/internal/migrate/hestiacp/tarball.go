package hestiacp

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/cpanel"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
)

// HestiaParsedTarball indexes a v-backup-user tar. Layout:
//
//	web/<dom>/public_html/...
//	web/<dom>/private/...
//	mail/<dom>/<local>/...                  (Maildir tree)
//	db/<dbname>.sql                         (per-DB dump)
//	user.conf
//	ssh_keys                                (sometimes)
//	conf/cron/                              (varies by Hestia minor)
//
// Hestia tarballs may or may not be gzipped depending on
// /usr/local/hestia/conf/hestia.conf BACKUP_GZIP. ParseHestiaTarball
// auto-detects via the .gz magic bytes.
//
// **STATUS:** Coded against documented Hestia v-backup-user output.
// NOT validated against a live Hestia tar.
type HestiaParsedTarball struct {
	ExtractDir string
	WebRoot    string   // <root>/web
	MailRoot   string   // <root>/mail
	MySQLDumps []string // absolute paths to extracted db/<name>.sql files
	UserConf   string   // <root>/user.conf
	SSHKeys    string   // authorized_keys: <root>/ssh_keys OR user_dir/.ssh/authorized_keys (JAB-30)
	CronFile   string   // <root>/conf/cron/<user> (when present)
	// ZoneFiles lists BIND-format zone files from dns/<domain>/conf/<domain>.db
	// (JAB-28). Fed to cpanel.ImportDNS via the adapter so source DNS records are
	// migrated instead of only the generic bootstrap zone.
	ZoneFiles []string
	// DomainDirs maps each domain name to its source-side
	// <WebRoot>/<dom>/ absolute path. Populated post-extract by
	// scanning the dir; M35.4 consumer feeds the cpanel ImportDomains
	// writer via the Hestia adapter branch in migrate_run_cmd.go.
	DomainDirs map[string]string
	Skipped    []string
	// TopLevel (GH #327 diag) is the set of top-level path segments seen in the
	// tar — used to explain an empty import (e.g. tar wraps everything under a
	// <timestamp>/ or backup/ dir, so classifyHestia's web/mail/db keys miss).
	TopLevel []string
}

func ParseHestiaTarball(tarballPath, extractDir string) (*HestiaParsedTarball, error) {
	if tarballPath == "" || extractDir == "" {
		return nil, errors.New("ParseHestiaTarball: empty path")
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

	buf := make([]byte, 2)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, fmt.Errorf("read tarball magic: %w", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	var src io.Reader = f
	if buf[0] == 0x1f && buf[1] == 0x8b {
		gz, gerr := gzip.NewReader(f)
		if gerr != nil {
			return nil, fmt.Errorf("gunzip: %w", gerr)
		}
		defer gz.Close()
		src = gz
	}
	tr := tar.NewReader(src)
	out := &HestiaParsedTarball{ExtractDir: extractDir}
	topSeen := map[string]bool{}

	const maxEntrySize = 100 << 30

	budget := cpanel.NewExtractBudget()
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar read: %w", err)
		}
		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") || filepath.IsAbs(clean) {
			out.Skipped = append(out.Skipped, "path_escape:"+hdr.Name)
			continue
		}
		if seg := strings.SplitN(clean, string(filepath.Separator), 2)[0]; seg != "" && seg != "." {
			topSeen[seg] = true
		}
		dest := filepath.Join(extractDir, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, 0o750); err != nil {
				return nil, fmt.Errorf("mkdir %s: %w", dest, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if hdr.Size > maxEntrySize {
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
			if _, err := budget.Charge(w, tr); err != nil {
				_ = w.Close()
				return nil, fmt.Errorf("copy %s: %w", dest, err)
			}
			if err := w.Close(); err != nil {
				return nil, fmt.Errorf("close %s: %w", dest, err)
			}
			classifyHestia(out, clean, dest)
		case tar.TypeSymlink, tar.TypeLink:
			out.Skipped = append(out.Skipped, "link:"+clean)
		default:
			out.Skipped = append(out.Skipped, fmt.Sprintf("typeflag:%c:%s", hdr.Typeflag, clean))
		}
	}
	for seg := range topSeen {
		out.TopLevel = append(out.TopLevel, seg)
	}
	out.WebRoot = filepath.Join(extractDir, "web")
	out.MailRoot = filepath.Join(extractDir, "mail")
	// GH #327: newer HestiaCP zstd-wraps everything — the per-domain site files
	// live in web/<d>/domain_data.tar.zst, mailboxes in mail/<d>/accounts.tar.zst,
	// and DB dumps are <name>.mysql.sql.zst. Decompress/extract them in place so
	// the WebRoot scan + MySQLDumps below see real content (the old plain-tar
	// layout still works — these globs simply match nothing on it).
	hydrateHestiaZstd(extractDir, out)
	// Post-extract scan: every immediate child of <WebRoot> names a
	// hosted domain. Feeds DomainNames+DocRoots in the adapter so
	// cpanel.ImportDomains can create panel rows + nginx vhosts
	// without a BIND zone (Hestia tarballs don't ship zones).
	if entries, derr := os.ReadDir(out.WebRoot); derr == nil {
		out.DomainDirs = make(map[string]string, len(entries))
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if name == "" || strings.HasPrefix(name, ".") {
				continue
			}
			out.DomainDirs[name] = filepath.Join(out.WebRoot, name)
		}
	}
	return out, nil
}

func classifyHestia(p *HestiaParsedTarball, path, abs string) {
	parts := strings.Split(path, string(filepath.Separator))
	if len(parts) == 0 {
		return
	}
	switch parts[0] {
	case "db":
		if isPostgresDump(path) {
			p.Skipped = append(p.Skipped, "postgres_unsupported:"+path)
		} else if strings.HasSuffix(path, ".sql") {
			p.MySQLDumps = append(p.MySQLDumps, abs)
		}
	case "user.conf":
		p.UserConf = abs
	case "ssh_keys":
		p.SSHKeys = abs
	case "conf":
		if len(parts) >= 3 && parts[1] == "cron" && p.CronFile == "" {
			p.CronFile = abs
		}
	case "dns":
		// JAB-28: dns/<domain>/conf/<domain>.db is the BIND zone with the
		// source records (A/CNAME/MX/TXT/SRV/CAA/DKIM/DMARC). The sibling
		// dns/<domain>/hestia/*.conf are Hestia metadata, not zone data.
		if len(parts) >= 4 && parts[2] == "conf" && strings.HasSuffix(path, ".db") {
			p.ZoneFiles = append(p.ZoneFiles, abs)
		}
	}
}

// hydrateHestiaZstd (GH #327) decompresses the nested zstd archives a modern
// HestiaCP v-backup-user tar contains, so the rest of the parser sees ordinary
// files. zstd is installed by install.sh. Best-effort per archive: a failure is
// recorded in Skipped, never fatal.
func hydrateHestiaZstd(extractDir string, out *HestiaParsedTarball) {
	// Web: web/<domain>/domain_data.tar.zst -> extract into web/<domain>/
	// (yields public_html/, private/, logs/, ...).
	webs, _ := filepath.Glob(filepath.Join(extractDir, "web", "*", "domain_data.tar.zst"))
	for _, a := range webs {
		if err := extractZstdTar(a, filepath.Dir(a)); err != nil {
			out.Skipped = append(out.Skipped, "zstd-web:"+a+":"+err.Error())
		}
	}
	// Mail: mail/<domain>/accounts.tar.zst -> extract into mail/<domain>/.
	mails, _ := filepath.Glob(filepath.Join(extractDir, "mail", "*", "accounts.tar.zst"))
	for _, a := range mails {
		if err := extractZstdTar(a, filepath.Dir(a)); err != nil {
			out.Skipped = append(out.Skipped, "zstd-mail:"+a+":"+err.Error())
		}
	}
	// DB: db/<name>/*.sql.zst -> decompress to *.sql + record for import.
	// A PostgreSQL database's dump (<name>.pgsql.sql.zst) is not restored.
	dbs, _ := filepath.Glob(filepath.Join(extractDir, "db", "*", "*.sql.zst"))
	for _, a := range dbs {
		if isPostgresDump(a) {
			rel, _ := filepath.Rel(extractDir, a)
			out.Skipped = append(out.Skipped, "postgres_unsupported:"+rel)
			continue
		}
		dst := strings.TrimSuffix(a, ".zst") // <name>.mysql.sql
		if err := decompressZstd(a, dst); err != nil {
			out.Skipped = append(out.Skipped, "zstd-db:"+a+":"+err.Error())
			continue
		}
		out.MySQLDumps = append(out.MySQLDumps, dst)
	}
	// SSH (JAB-30): modern Hestia stores authorized_keys in
	// user_dir/.ssh.tar.zst -> .ssh/authorized_keys, not the legacy top-level
	// ssh_keys file. Extract it and record the key file when the legacy one was
	// absent (classifyHestia already set SSHKeys if ssh_keys existed).
	sshArchive := filepath.Join(extractDir, "user_dir", ".ssh.tar.zst")
	if _, serr := os.Stat(sshArchive); serr == nil {
		if xerr := extractZstdTar(sshArchive, filepath.Dir(sshArchive)); xerr != nil {
			out.Skipped = append(out.Skipped, "zstd-ssh:"+sshArchive+":"+xerr.Error())
		} else if out.SSHKeys == "" {
			ak := filepath.Join(extractDir, "user_dir", ".ssh", "authorized_keys")
			if st, akerr := os.Stat(ak); akerr == nil && !st.IsDir() {
				out.SSHKeys = ak
			}
		}
	}
}

// isPostgresDump reports whether path is a PostgreSQL database's dump. Hestia
// names a database's dump <name>.<type>.sql, type mysql or pgsql, and
// zstd-compresses it on newer versions.
func isPostgresDump(path string) bool {
	return strings.HasSuffix(strings.TrimSuffix(path, ".zst"), ".pgsql.sql")
}

// Bounds for nested-archive hydration (untrusted-ish migration input): a ctx
// timeout stops a hung/huge decompress, and a total-bytes cap stops a zstd bomb
// (a tiny .zst that inflates to fill the disk). Per-entry cap mirrors the outer
// parser's maxEntrySize.
const (
	hydrateTimeout    = 30 * time.Minute
	hydrateMaxBytes   = int64(100) << 30 // 100 GiB total per nested archive
	hydrateEntryBytes = int64(100) << 30
)

// extractZstdTar decompresses `src` (a .tar.zst) via the zstd CLI and extracts
// the inner tar into destDir using Go's tar reader — with the SAME path-escape
// guard, per-entry + total size caps, and a timeout as the outer parser, so a
// malicious/corrupt nested archive cannot traverse out of destDir, fill the
// disk, or hang the import.
func extractZstdTar(src, destDir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), hydrateTimeout)
	defer cancel()
	z := exec.CommandContext(ctx, "zstd", "-dc", src)
	pipe, err := z.StdoutPipe()
	if err != nil {
		return err
	}
	if err := z.Start(); err != nil {
		return err
	}
	defer func() { _ = z.Wait() }()

	tr := tar.NewReader(pipe)
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("inner tar %s: %w", src, err)
		}
		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") || filepath.IsAbs(clean) {
			continue // path-escape guard
		}
		dest := filepath.Join(destDir, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			_ = os.MkdirAll(dest, 0o750)
		case tar.TypeReg:
			if hdr.Size > hydrateEntryBytes {
				continue
			}
			total += hdr.Size
			if total > hydrateMaxBytes {
				return fmt.Errorf("nested archive %s exceeds %d bytes (possible decompression bomb)", src, hydrateMaxBytes)
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
				return err
			}
			w, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
			if err != nil {
				return err
			}
			// Bounded copy — never read more than the declared entry size.
			_, cerr := io.CopyN(w, tr, hdr.Size)
			_ = w.Close()
			if cerr != nil && cerr != io.EOF {
				return cerr
			}
		default:
			// symlinks/hardlinks/devices skipped (defense-in-depth).
		}
	}
	return nil
}

// decompressZstd decompresses `src` to `dst` (a plain SQL dump), bounded by a
// timeout and a total-bytes cap to stop a decompression bomb.
func decompressZstd(src, dst string) error {
	ctx, cancel := context.WithTimeout(context.Background(), hydrateTimeout)
	defer cancel()
	z := exec.CommandContext(ctx, "zstd", "-dc", src)
	pipe, err := z.StdoutPipe()
	if err != nil {
		return err
	}
	if err := z.Start(); err != nil {
		return err
	}
	defer func() { _ = z.Wait() }()
	w, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	defer w.Close()
	// Cap the output; CopyN returns io.EOF when the source is smaller (normal).
	n, cerr := io.CopyN(w, pipe, hydrateMaxBytes)
	if cerr != nil && cerr != io.EOF {
		return cerr
	}
	if n >= hydrateMaxBytes {
		return fmt.Errorf("decompressed %s exceeds %d bytes (possible bomb)", src, hydrateMaxBytes)
	}
	return nil
}
