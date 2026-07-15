package hestiacp

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// GH #327 (johnnyq): HestiaCP's user.conf carries the account's Contact Name.
// Carry it onto the created jabali user so the migrated account keeps its
// display name instead of a blank one.
//
// Three things the first passes got wrong (root-caused against johnnyq's real
// v-backup-user tarball on the test box):
//  1. The file lives at hestia/user.conf inside the archive, NOT the root.
//  2. HestiaCP writes the contact name as NAME='Full Name' — FNAME/LNAME are
//     only in the v-list-users JSON and are usually empty on disk.
//  3. The offline `migrate restore` path only STAGES the tarball; extraction
//     happens later in the import, AFTER the auto-create step — so reading the
//     extracted dir at create time saw nothing. PeekUserNameFromStaging reads
//     the name straight out of the staged tarball, before extraction.
var (
	hestiaNameRe  = regexp.MustCompile(`(?m)^\s*NAME=['"]?([^'"\n]*)['"]?`)
	hestiaFNameRe = regexp.MustCompile(`(?m)^\s*FNAME=['"]?([^'"\n]*)['"]?`)
	hestiaLNameRe = regexp.MustCompile(`(?m)^\s*LNAME=['"]?([^'"\n]*)['"]?`)
)

// parseHestiaContactName pulls the contact name out of a user.conf body.
// Prefers explicit FNAME/LNAME; else splits the single NAME field on the
// first space.
func parseHestiaContactName(b []byte) (first, last string) {
	if m := hestiaFNameRe.FindSubmatch(b); m != nil {
		first = string(m[1])
	}
	if m := hestiaLNameRe.FindSubmatch(b); m != nil {
		last = string(m[1])
	}
	if first == "" && last == "" {
		if m := hestiaNameRe.FindSubmatch(b); m != nil {
			full := strings.TrimSpace(string(m[1]))
			if i := strings.IndexByte(full, ' '); i > 0 {
				first, last = full[:i], strings.TrimSpace(full[i+1:])
			} else {
				first = full
			}
		}
	}
	return first, last
}

// PeekUserName reads the Hestia account's user.conf from an already-EXTRACTED
// tree (hestia/user.conf, root user.conf fallback). Best-effort → "","".
func PeekUserName(extractDir string) (first, last string) {
	if extractDir == "" {
		return "", ""
	}
	for _, p := range []string{
		filepath.Join(extractDir, "hestia", "user.conf"),
		filepath.Join(extractDir, "user.conf"),
	} {
		if b, err := os.ReadFile(p); err == nil {
			return parseHestiaContactName(b)
		}
	}
	return "", ""
}

// PeekUserNameFromStaging finds the staged v-backup-user tarball in stagingDir
// and reads hestia/user.conf straight out of it — used at auto-create time,
// before the import extracts the tarball. Best-effort → "","".
func PeekUserNameFromStaging(stagingDir, sourceUser string) (first, last string) {
	if stagingDir == "" {
		return "", ""
	}
	// Same discovery the Hestia parse case uses: user.<user>.tar.gz, then any
	// *.tar.gz / *.tar in the staging dir.
	var cands []string
	cands = append(cands, filepath.Join(stagingDir, "user."+sourceUser+".tar.gz"))
	for _, pat := range []string{"*.tar.gz", "*.tar"} {
		if m, _ := filepath.Glob(filepath.Join(stagingDir, pat)); len(m) > 0 {
			cands = append(cands, m...)
		}
	}
	for _, tarPath := range cands {
		if b, ok := readTarMember(tarPath, "hestia/user.conf"); ok {
			return parseHestiaContactName(b)
		}
	}
	return "", ""
}

// readTarMember returns the bytes of one member (matched by suffix, with or
// without a leading "./") from a .tar or .tar.gz archive.
func readTarMember(tarPath, member string) ([]byte, bool) {
	f, err := os.Open(tarPath)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	// Sniff the gzip magic (0x1f 0x8b) rather than trusting the extension:
	// the offline restore stages a plain .tar under a user.<u>.tar.gz name,
	// so the suffix lies.
	var magic [2]byte
	n, _ := io.ReadFull(f, magic[:])
	if _, serr := f.Seek(0, io.SeekStart); serr != nil {
		return nil, false
	}
	var r io.Reader = f
	if n == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, gerr := gzip.NewReader(f)
		if gerr != nil {
			return nil, false
		}
		defer gz.Close()
		r = gz
	}
	tr := tar.NewReader(r)
	for {
		h, nerr := tr.Next()
		if nerr != nil {
			return nil, false
		}
		name := strings.TrimPrefix(h.Name, "./")
		if name == member || strings.HasSuffix(name, "/"+member) {
			b, rerr := io.ReadAll(io.LimitReader(tr, 1<<20))
			if rerr != nil {
				return nil, false
			}
			return b, true
		}
	}
}
