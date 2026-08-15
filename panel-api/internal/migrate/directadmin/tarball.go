package directadmin

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/cpanel"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// DAParsedTarball is the index produced by parsing a DA-format
// `system_backup_user` tar. Shape parallels cpanel.ParsedTarball
// but the per-area paths reflect DA's tar layout:
//
//	<user>/backup.conf
//	<user>/domains/<dom>/public_html/...
//	<user>/databases/<dbname>.sql            (DA exports each DB
//	                                          as a flat .sql file)
//	<user>/email/<dom>/<localpart>/Maildir/...
//	<user>/.ssh/authorized_keys
//
// **STATUS:** Coded against documented DA backup tar shapes
// (DA admin docs + community wiki). NOT validated against a live
// DA backup tar. Path-prefix detection is loose enough to handle
// minor variations (some DA versions wrap the tree in
// 'admin_<user>/...' instead of '<user>/...').
type DAParsedTarball struct {
	SourceUser    string
	ExtractDir    string
	HomeDir       string   // <user>/ root inside extractDir; the cpanel-equivalent of homedir
	MySQLDumps    []string // absolute paths to extracted .sql files
	CronFile      string   // absolute path to extracted cron file (if present)
	SSHAuthorized string   // absolute path to authorized_keys (if present)
	MailRoot      string   // <user>/email/ — Maildir parent
	// DomainDirs maps each domain name to its source-side
	// <HomeDir>/domains/<dom>/ absolute path. Populated post-extract
	// by scanning the directory; consumed by ToCpanelParsed to seed
	// ParsedTarball.DomainNames + DocRoots so the cpanel
	// ImportDomains writer can create panel rows without a BIND zone.
	DomainDirs map[string]string
	Skipped    []string
}

// ParseDATarball streams a DA system-backup-user tar (.tar or
// .tar.gz) into extractDir + classifies entries by area. Same
// hardening as cpanel.ParseTarball: path-escape rejection +
// per-entry size cap.
func ParseDATarball(tarballPath, extractDir string) (*DAParsedTarball, error) {
	if tarballPath == "" || extractDir == "" {
		return nil, errors.New("ParseDATarball: empty path")
	}
	if err := os.MkdirAll(extractDir, 0o750); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", extractDir, err)
	}
	f, err := os.Open(tarballPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", tarballPath, err)
	}
	defer f.Close()

	// DA tars may or may not be gzipped — peek the magic.
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
	out := &DAParsedTarball{ExtractDir: extractDir}

	const maxEntrySize = 100 << 30 // 100 GiB

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
		// Glean source user from top-level <user>/ dir. Some DA
		// versions wrap the tree in 'admin_<user>/...' — strip
		// 'admin_' prefix when present.
		if out.SourceUser == "" {
			parts := strings.Split(clean, string(filepath.Separator))
			if len(parts) >= 1 && parts[0] != "" {
				top := parts[0]
				out.SourceUser = strings.TrimPrefix(top, "admin_")
			}
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
			classifyDA(out, clean, dest)
		case tar.TypeSymlink, tar.TypeLink:
			out.Skipped = append(out.Skipped, "link:"+clean)
		default:
			out.Skipped = append(out.Skipped, fmt.Sprintf("typeflag:%c:%s", hdr.Typeflag, clean))
		}
	}
	if out.SourceUser == "" {
		return nil, errors.New("ParseDATarball: no top-level <user>/ — not a DA system-backup tarball?")
	}
	out.HomeDir = filepath.Join(extractDir, out.SourceUser)
	out.MailRoot = filepath.Join(out.HomeDir, "email")
	// Post-extract scan: every immediate child of <HomeDir>/domains/
	// names a hosted domain. Stash for ToCpanelParsed → ImportDomains.
	domainsRoot := filepath.Join(out.HomeDir, "domains")
	if entries, derr := os.ReadDir(domainsRoot); derr == nil {
		out.DomainDirs = make(map[string]string, len(entries))
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if name == "" || strings.HasPrefix(name, ".") {
				continue
			}
			out.DomainDirs[name] = filepath.Join(domainsRoot, name)
		}
	}
	return out, nil
}

// classifyDA slots an extracted file into the right area slice.
// DA layout:
//
//	<user>/databases/<dbname>.sql
//	<user>/.ssh/authorized_keys
//	<user>/cron OR <user>/cron.<user>
//	<user>/email/<dom>/<local>/Maildir/...   (left for Maildir walk)
//	<user>/domains/<dom>/...                 (homedir contents; rsync target)
func classifyDA(p *DAParsedTarball, path, abs string) {
	parts := strings.Split(path, string(filepath.Separator))
	if len(parts) < 2 {
		return
	}
	rest := parts[1:]
	if len(rest) == 0 {
		return
	}
	switch rest[0] {
	case "databases":
		if strings.HasSuffix(rest[len(rest)-1], ".sql") {
			p.MySQLDumps = append(p.MySQLDumps, abs)
		}
	case ".ssh":
		if len(rest) >= 2 && rest[1] == "authorized_keys" {
			p.SSHAuthorized = abs
		}
	case "cron":
		// DA stores the user's crontab as `<user>/cron` (single
		// file). Less common: `<user>/cron.<user>` on older
		// minors. Either way, slot the first match.
		if p.CronFile == "" {
			p.CronFile = abs
		}
	}
}
