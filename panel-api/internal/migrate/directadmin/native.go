// native.go — DirectAdmin's own user backup, as `da admin-backup` and the DA
// backup pages write it (DA 1.7x, checked against a real 1.714 archive):
//
//	backup/user.conf                 key=value; username= is the source account
//	backup/.shadow                   the account's crypt(3) hash
//	backup/crontab.conf              MAILTO=/PATH=/SHELL= plus <id>=<cron line>
//	backup/<db>.sql, backup/<db>.conf  one dump per database (flat), and its users
//	backup/home.tar.zst              the home directory outside domains/ (nested)
//	backup/<dom>/domain.conf         one directory per domain
//	backup/<dom>/<dom>.db            its BIND zone
//	backup/<dom>/domain.{cert,key,cacert}
//	backup/<dom>/email/{passwd,aliases,...}
//	domains/<dom>/{public_html,public_ftp,logs,stats}/...
//	imap/<dom>/<local>/Maildir/...
//
// The archive is .tar.zst (DA's default, backup_gzip=2), .tar.gz or plain .tar;
// ParseNativeBackup tells them apart by their magic bytes. It extracts into
// a layout the cPanel restore writers read (ToCpanelParsed):
//
//	<extractDir>/home/          home.tar.zst + domains/<dom>/{public_html,public_ftp}
//	                            = the jabali home layout, copied as one tree
//	<extractDir>/imap/          imap/<dom>/<local>/Maildir, plus the account's own
//	                            Maildir at the top (its owner mailbox)
//	<extractDir>/mysql/<db>.sql
//	<extractDir>/backup/...     the small metadata files
//	<extractDir>/apache_tls/<dom>/{certificates,key}
//	<extractDir>/etc/<dom>/aliases   (ImportForwarders)
//	<extractDir>/cron/<user>
package directadmin

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/cpanel"
)

// NativeBackup is a parsed DirectAdmin user backup.
type NativeBackup struct {
	ExtractDir    string
	SourceUser    string // backup/user.conf username=
	Email         string // backup/user.conf email=
	PrimaryDomain string // backup/user.conf domain=
	ShadowHash    string // backup/.shadow
	HomeDir       string // <extractDir>/home
	MailRoot      string // <extractDir>/imap
	// OwnerMaildir: the account's own Maildir (from home.tar.zst) was staged at
	// the top of MailRoot, where the importer reads the owner mailbox.
	OwnerMaildir bool
	Domains      []string // primary first, then by name
	Subdomains   map[string]string
	ZoneFiles    []string
	MySQLDumps   []string
	CompatUsers  []cpanel.CompatUser
	CronFile     string
	SSHKeys      string
	// MailShadowDir holds <domain>/shadow, copied from backup/<domain>/email/
	// passwd: the mailbox password hashes (carried with --preserve-source-state).
	MailShadowDir string
	// AppRoots: per domain, the directories under public_html, other than
	// public_html itself, that hold an application's config file (a second
	// WordPress in a subdirectory). Relative to public_html.
	AppRoots map[string][]string
	// MailDomains are the domains that keep mail service: mail accounts
	// (email/passwd), forwarders, or mailboxes under imap/<dom>/ on the source.
	MailDomains []string
	Skipped     []string

	ownerFiles int // files in the account's own Maildir
}

// nativeMaxMetaBytes bounds a backup/ metadata file other than a database
// dump or the home archive. The real ones are a few KiB.
const nativeMaxMetaBytes = 16 << 20

var (
	nativeDomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
	nativeLocalRe  = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,64}$`)
	nativeDBRe     = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	nativeSubRe    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// homeSkip: entries of home.tar.zst that are not worth carrying. Redis keeps
// a cache dump there and WP-CLI its download cache; the Imunify ids belong to
// the source server.
var homeSkip = []string{".redis", ".wp-cli/cache", ".imunify_patch_id", ".myimunify_id"}

// domainSkip: per-domain directories DA generates (access logs, statistics).
var domainSkip = map[string]bool{"logs": true, "stats": true}

// nativeStagedPrefix names a DirectAdmin user backup staged for an offline
// restore: <staging>/da-native.<user>.tar, whatever its compression.
const nativeStagedPrefix = "da-native."

// NativeStagedName is the file name `jabali migrate restore --directadmin`
// stages the backup under.
func NativeStagedName(user string) string { return nativeStagedPrefix + user + ".tar" }

// NativeStagedPath returns the staged DirectAdmin user backup in stagingDir,
// or "" when the job has none (an SSH pull stages a cpmove instead).
func NativeStagedPath(stagingDir, user string) string {
	if !looksLikeDAUsername(user) {
		return ""
	}
	p := filepath.Join(stagingDir, NativeStagedName(user))
	if !isRegular(p) {
		return ""
	}
	return p
}

// IsNativeArchive reports whether path is a DirectAdmin user backup: its first
// entry sits under backup/. It reads only the start of the archive.
func IsNativeArchive(path string) bool {
	r, closeFn, err := openArchive(path)
	if err != nil {
		return false
	}
	defer closeFn()
	hdr, err := tar.NewReader(r).Next()
	if err != nil {
		return false
	}
	name := strings.TrimPrefix(hdr.Name, "./")
	return name == "backup/" || strings.HasPrefix(name, "backup/")
}

// NativeMeta is what PeekNativeMeta reads off the start of the archive,
// before anything is extracted.
type NativeMeta struct {
	Username      string
	Email         string
	PrimaryDomain string
	ShadowHash    string
}

// PeekNativeMeta reads backup/user.conf and backup/.shadow. DA writes backup/
// first, so it stops at the first entry outside it.
func PeekNativeMeta(path string) (*NativeMeta, error) {
	r, closeFn, err := openArchive(path)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	tr := tar.NewReader(r)
	meta := &NativeMeta{}
	var sawConf bool
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if !strings.HasPrefix(name, "backup/") {
			break
		}
		switch name {
		case "backup/user.conf":
			kv, rerr := readKV(io.LimitReader(tr, 64<<10))
			if rerr != nil {
				return nil, rerr
			}
			meta.Username, meta.Email, meta.PrimaryDomain = kv["username"], kv["email"], strings.ToLower(kv["domain"])
			sawConf = true
		case "backup/.shadow":
			b, _ := io.ReadAll(io.LimitReader(tr, 4<<10))
			meta.ShadowHash = strings.TrimSpace(string(b))
		}
		if sawConf && meta.ShadowHash != "" {
			break
		}
	}
	if !sawConf {
		return nil, errors.New("no backup/user.conf: not a DirectAdmin user backup")
	}
	if !looksLikeDAUsername(meta.Username) {
		return nil, fmt.Errorf("backup/user.conf: invalid username %q", meta.Username)
	}
	if !nativeDomainRe.MatchString(meta.PrimaryDomain) {
		meta.PrimaryDomain = ""
	}
	return meta, nil
}

// MeasureNative sums the sizes of the archive's entries, nested home archive
// excluded (it is small next to domains/), for the disk preflight.
func MeasureNative(path string) (uint64, error) {
	r, closeFn, err := openArchive(path)
	if err != nil {
		return 0, err
	}
	defer closeFn()
	tr := tar.NewReader(r)
	var total uint64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return total, drain(r)
		}
		if err != nil {
			return 0, err
		}
		if hdr.Size > 0 {
			total += uint64(hdr.Size)
		}
	}
}

// CheckNativeDiskSpace refuses an extraction the destination can't hold:
// the measured contents, plus a tenth and 1 GiB for the home archive and
// filesystem overhead.
func CheckNativeDiskSpace(path, dest string) error {
	measured, err := MeasureNative(path)
	if err != nil {
		return fmt.Errorf("measure %s: %w", filepath.Base(path), err)
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(dest, &st); err != nil {
		return nil
	}
	free := st.Bavail * uint64(st.Bsize)
	needed := measured + measured/10 + 1<<30
	if free < needed {
		return fmt.Errorf("insufficient disk space to extract %s: contents measure %d MiB, need %d MiB with margin, only %d MiB free on %s",
			filepath.Base(path), measured>>20, needed>>20, free>>20, dest)
	}
	return nil
}

// ParseNativeBackup extracts a DirectAdmin user backup into extractDir and
// indexes it.
func ParseNativeBackup(path, extractDir string) (*NativeBackup, error) {
	if path == "" || extractDir == "" {
		return nil, errors.New("ParseNativeBackup: empty path")
	}
	if err := os.MkdirAll(extractDir, 0o750); err != nil {
		return nil, err
	}
	nb := &NativeBackup{
		ExtractDir: extractDir,
		HomeDir:    filepath.Join(extractDir, "home"),
		MailRoot:   filepath.Join(extractDir, "imap"),
		Subdomains: map[string]string{},
		AppRoots:   map[string][]string{},
	}
	r, closeFn, err := openArchive(path)
	if err != nil {
		return nil, err
	}
	defer closeFn()

	x := newExtractor(extractDir)
	tr := tar.NewReader(r)
	homeArchive := ""
	skippedLogs := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		parts, ok := cleanParts(name)
		if !ok {
			nb.Skipped = append(nb.Skipped, "path_escape:"+hdr.Name)
			continue
		}
		if len(parts) == 0 {
			continue
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA && hdr.Typeflag != tar.TypeDir {
			nb.Skipped = append(nb.Skipped, fmt.Sprintf("entry_type:%c:%s", hdr.Typeflag, name))
			continue
		}
		dir := hdr.Typeflag == tar.TypeDir
		var dest []string
		switch parts[0] {
		case "backup":
			dest = nb.routeBackup(parts, dir)
			if len(parts) == 2 && parts[1] == "home.tar.zst" && !dir {
				homeArchive = filepath.Join(extractDir, "backup", "home.tar.zst")
				dest = []string{"backup", "home.tar.zst"}
			} else if dest != nil && !dir && hdr.Size > nativeMaxMetaBytes && !isDumpPath(parts) {
				nb.Skipped = append(nb.Skipped, fmt.Sprintf("oversize_meta:%s:%d", name, hdr.Size))
				continue
			}
		case "domains":
			if len(parts) == 1 {
				continue
			}
			if !nativeDomainRe.MatchString(parts[1]) {
				nb.Skipped = append(nb.Skipped, "domain_dir_name:"+name)
				continue
			}
			if len(parts) >= 3 && domainSkip[parts[2]] {
				if !dir {
					skippedLogs++
				}
				continue
			}
			dest = append([]string{"home"}, parts...)
		case "imap":
			if len(parts) >= 2 && !nativeDomainRe.MatchString(parts[1]) {
				nb.Skipped = append(nb.Skipped, "imap_domain_name:"+name)
				continue
			}
			if len(parts) >= 3 && !nativeLocalRe.MatchString(parts[2]) {
				nb.Skipped = append(nb.Skipped, "imap_local_name:"+name)
				continue
			}
			dest = parts
		default:
			nb.Skipped = append(nb.Skipped, "unknown_top:"+parts[0])
			continue
		}
		if dest == nil {
			continue
		}
		if err := x.write(dest, hdr, tr); err != nil {
			return nil, err
		}
	}
	if err := drain(r); err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}
	if skippedLogs > 0 {
		nb.Skipped = append(nb.Skipped, fmt.Sprintf("da_generated: %d file(s) under domains/<domain>/logs and stats not carried", skippedLogs))
	}

	conf, err := readKVFile(filepath.Join(extractDir, "backup", "user.conf"))
	if err != nil {
		return nil, fmt.Errorf("backup/user.conf: %w (not a DirectAdmin user backup?)", err)
	}
	nb.SourceUser = conf["username"]
	if !looksLikeDAUsername(nb.SourceUser) {
		return nil, fmt.Errorf("backup/user.conf: invalid username %q", nb.SourceUser)
	}
	nb.Email = conf["email"]
	if d := strings.ToLower(conf["domain"]); nativeDomainRe.MatchString(d) {
		nb.PrimaryDomain = d
	}
	if b, err := os.ReadFile(filepath.Join(extractDir, "backup", ".shadow")); err == nil {
		nb.ShadowHash = strings.TrimSpace(string(b))
	}

	if homeArchive != "" {
		if err := nb.extractHome(homeArchive); err != nil {
			return nil, fmt.Errorf("backup/home.tar.zst: %w", err)
		}
		_ = os.Remove(homeArchive)
	}
	nb.indexDomains()
	nb.relocateSubdomains()
	nb.indexDatabases()
	nb.convertCron()
	nb.findAppRoots()
	if p := filepath.Join(nb.HomeDir, ".ssh", "authorized_keys"); isRegular(p) {
		nb.SSHKeys = p
	}
	return nb, nil
}

func isDumpPath(parts []string) bool {
	return len(parts) == 2 && strings.HasSuffix(parts[1], ".sql")
}

// routeBackup maps a backup/ entry to where it is extracted, or nil to leave
// it out. DA login keys and per-user runtime data are not carried.
func (nb *NativeBackup) routeBackup(parts []string, dir bool) []string {
	if len(parts) == 1 {
		return nil
	}
	switch parts[1] {
	case "login_keys", "email_data", "user.db", "ticket.conf", "bandwidth.tally", "user.usage", "home.tar.zst":
		return nil
	}
	if len(parts) == 2 && !dir && strings.HasSuffix(parts[1], ".sql") {
		db := strings.TrimSuffix(parts[1], ".sql")
		if !nativeDBRe.MatchString(db) {
			nb.Skipped = append(nb.Skipped, "db_dump_name:"+parts[1])
			return nil
		}
		return []string{"mysql", parts[1]}
	}
	if len(parts) >= 3 && !nativeDomainRe.MatchString(parts[1]) {
		// backup/<dom>/...: anything else with a subdirectory is unexpected.
		nb.Skipped = append(nb.Skipped, "backup_dir_name:"+strings.Join(parts, "/"))
		return nil
	}
	return parts
}

// extractHome unpacks the nested home archive into HomeDir, and the account's
// own Maildir to the top of MailRoot.
func (nb *NativeBackup) extractHome(archive string) error {
	r, closeFn, err := openArchive(archive)
	if err != nil {
		return err
	}
	defer closeFn()
	x := newExtractor(nb.ExtractDir)
	tr := tar.NewReader(r)
	skipped := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		parts, ok := cleanParts(strings.TrimPrefix(hdr.Name, "./"))
		if !ok {
			nb.Skipped = append(nb.Skipped, "home_path_escape:"+hdr.Name)
			continue
		}
		if len(parts) == 0 {
			continue
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA && hdr.Typeflag != tar.TypeDir {
			nb.Skipped = append(nb.Skipped, fmt.Sprintf("home_entry_type:%c:%s", hdr.Typeflag, hdr.Name))
			continue
		}
		rel := strings.Join(parts, "/")
		if homeSkipped(rel) {
			if hdr.Typeflag != tar.TypeDir {
				skipped++
			}
			continue
		}
		var dest []string
		switch parts[0] {
		case "Maildir":
			dest = append([]string{"imap"}, parts[1:]...)
			nb.OwnerMaildir = true
			if hdr.Typeflag != tar.TypeDir {
				nb.ownerFiles++
			}
		case "domains", "imap":
			// Carried from the top-level domains/ and imap/.
			continue
		default:
			dest = append([]string{"home"}, parts...)
		}
		if len(dest) == 1 && hdr.Typeflag == tar.TypeDir {
			continue
		}
		if err := x.write(dest, hdr, tr); err != nil {
			return err
		}
	}
	if err := drain(r); err != nil {
		return err
	}
	if skipped > 0 {
		nb.Skipped = append(nb.Skipped, fmt.Sprintf("home_cache: %d file(s) not carried (%s)", skipped, strings.Join(homeSkip, ", ")))
	}
	return nil
}

func homeSkipped(rel string) bool {
	for _, s := range homeSkip {
		if rel == s || strings.HasPrefix(rel, s+"/") {
			return true
		}
	}
	return false
}

// indexDomains reads backup/<dom>/: the domain list, zones, subdomains,
// certificates and forwarders.
func (nb *NativeBackup) indexDomains() {
	root := filepath.Join(nb.ExtractDir, "backup")
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if !e.IsDir() || !nativeDomainRe.MatchString(e.Name()) {
			continue
		}
		dom := e.Name()
		ddir := filepath.Join(root, dom)
		if !isRegular(filepath.Join(ddir, "domain.conf")) {
			continue
		}
		nb.Domains = append(nb.Domains, dom)
		if z := filepath.Join(ddir, dom+".db"); isRegular(z) {
			nb.ZoneFiles = append(nb.ZoneFiles, z)
		}
		if raw, err := os.ReadFile(filepath.Join(ddir, "subdomain.list")); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				sub := strings.ToLower(strings.TrimSpace(line))
				if sub == "" {
					continue
				}
				if !nativeSubRe.MatchString(sub) {
					nb.Skipped = append(nb.Skipped, fmt.Sprintf("subdomain_name:%s:%q", dom, sub))
					continue
				}
				nb.Subdomains[sub+"."+dom] = dom
			}
		}
		nb.stageCert(dom, ddir)
		accounts := nb.stageMailAccounts(dom, ddir)
		forwarders := nb.stageAliases(dom, ddir)
		nb.noteUncarried(dom, ddir)
		nb.noteUnread(dom, ddir)
		nb.settleMailDomain(dom, accounts > 0 || forwarders > 0)
	}
	sort.Slice(nb.Domains, func(i, j int) bool {
		pi, pj := nb.Domains[i] == nb.PrimaryDomain, nb.Domains[j] == nb.PrimaryDomain
		if pi != pj {
			return pi
		}
		return nb.Domains[i] < nb.Domains[j]
	})
	sort.Strings(nb.MailDomains)
	if nb.OwnerMaildir && !nb.isMailDomain(nb.PrimaryDomain) {
		// Carrying the account's own mailbox would turn mail on for a domain
		// whose mail the source didn't serve: this server would then accept
		// mail for it (the site's own messages included) instead of the server
		// its MX names.
		nb.OwnerMaildir = false
		nb.Skipped = append(nb.Skipped, fmt.Sprintf("owner_mailbox: %s@%s (%d file(s)) not carried: the domain has no mail accounts, forwarders or mailboxes on the source, so mail stays off for it", nb.SourceUser, nb.PrimaryDomain, nb.ownerFiles))
	}
}

// stageMailAccounts copies backup/<dom>/email/passwd (local:hash:...) to
// where the mail import reads it: mailshadow/<dom>/shadow for the hashes
// (MailShadowDir) and the local parts to imap/<dom>/conf/passwd, the account
// list, so an account that never received mail still gets its mailbox. It
// returns the number of accounts.
func (nb *NativeBackup) stageMailAccounts(dom, ddir string) int {
	raw, err := os.ReadFile(filepath.Join(ddir, "email", "passwd"))
	if err != nil {
		return 0
	}
	var b, locals strings.Builder
	n := 0
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if eq, colon := strings.IndexByte(line, '='), strings.IndexByte(line, ':'); eq > 0 && (colon < 0 || eq < colon) {
			// <local>=passwd=<crypt>&...: the shape DA writes ftp.passwd and
			// <db>.conf in. Not seen for mail yet, so read either.
			q, _ := url.ParseQuery(line[eq+1:])
			line = line[:eq] + ":" + q.Get("passwd")
		}
		local, _, _ := strings.Cut(line, ":")
		if !nativeLocalRe.MatchString(local) {
			nb.Skipped = append(nb.Skipped, fmt.Sprintf("mail_account_name:%s:%q", dom, local))
			continue
		}
		b.WriteString(line + "\n")
		locals.WriteString(local + "\n")
		n++
	}
	if n == 0 {
		return 0
	}
	shadowDir := filepath.Join(nb.ExtractDir, "mailshadow")
	if err := writeFileMode(filepath.Join(shadowDir, dom, "shadow"), []byte(b.String()), 0o600); err != nil {
		nb.Skipped = append(nb.Skipped, fmt.Sprintf("mail_passwd:%s:%v", dom, err))
		return 0
	}
	nb.MailShadowDir = shadowDir
	// The account list only: the hashes are read from MailShadowDir.
	if err := writeFileMode(filepath.Join(nb.MailRoot, dom, "conf", "passwd"), []byte(locals.String()), 0o600); err != nil {
		nb.Skipped = append(nb.Skipped, fmt.Sprintf("mail_passwd:%s:%v", dom, err))
	}
	return n
}

// eximDirectives are alias values that are not addresses.
var eximDirectives = []string{"|", ":fail:", ":blackhole:", ":defer:", ":include:"}

// stageAliases writes backup/<dom>/email/aliases (exim lsearch,
// "local: target,target") to etc/<dom>/aliases for ImportForwarders, keeping
// only targets that are full addresses. A bare target is exim local delivery
// (DA's "<user>: <user>" routes the account's own address to its system
// mailbox), not a forwarder; pipes and :fail:/:blackhole: are not either.
// It returns the number of forwarders kept.
func (nb *NativeBackup) stageAliases(dom, ddir string) int {
	raw, err := os.ReadFile(filepath.Join(ddir, "email", "aliases"))
	if err != nil {
		return 0
	}
	var b strings.Builder
	n := 0
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		local, rest, ok := strings.Cut(line, ":")
		local, rest = strings.TrimSpace(local), strings.TrimSpace(rest)
		if !ok || !nativeLocalRe.MatchString(local) || rest == "" {
			nb.Skipped = append(nb.Skipped, fmt.Sprintf("alias_malformed:%s:%q", dom, line))
			continue
		}
		if directive(rest) {
			nb.Skipped = append(nb.Skipped, fmt.Sprintf("alias_directive:%s@%s:%s", local, dom, rest))
			continue
		}
		var keep []string
		for _, t := range strings.Split(rest, ",") {
			t = strings.TrimSpace(t)
			switch {
			case t == "":
			case strings.Contains(t, "@") && !strings.ContainsAny(t, " \t\"|") && len(t) <= 254:
				keep = append(keep, t)
			case local == nb.SourceUser && t == nb.SourceUser:
				// DA's own route for the account's address; nothing to carry.
			default:
				nb.Skipped = append(nb.Skipped, fmt.Sprintf("alias_local_target:%s@%s→%s (not a full address; not carried)", local, dom, t))
			}
		}
		if len(keep) == 0 {
			continue
		}
		b.WriteString(local + ": " + strings.Join(keep, ",") + "\n")
		n++
	}
	if n == 0 {
		return 0
	}
	if err := writeFileMode(filepath.Join(nb.ExtractDir, "etc", dom, "aliases"), []byte(b.String()), 0o640); err != nil {
		nb.Skipped = append(nb.Skipped, fmt.Sprintf("aliases:%s:%v", dom, err))
		return 0
	}
	return n
}

func directive(v string) bool {
	for _, d := range eximDirectives {
		if strings.HasPrefix(v, d) {
			return true
		}
	}
	return false
}

// noteUncarried reports per-domain settings the import does not recreate.
func (nb *NativeBackup) noteUncarried(dom, ddir string) {
	if raw, err := os.ReadFile(filepath.Join(ddir, "ftp.passwd")); err == nil {
		// <login>=passwd=<crypt>&path=<dir>&type=system|virtual; the system
		// login is the account itself, which comes across with the account.
		n := 0
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			u, rest, ok := strings.Cut(line, "=")
			if !ok || u == "" || strings.HasPrefix(u, "#") {
				continue
			}
			if q, err := url.ParseQuery(rest); u == nb.SourceUser || (err == nil && q.Get("type") == "system") {
				continue
			}
			n++
		}
		if n > 0 {
			nb.Skipped = append(nb.Skipped, fmt.Sprintf("ftp_accounts:%s: %d FTP account(s) not carried; recreate them in the panel", dom, n))
		}
	}
	for _, f := range []string{"autoresponder.conf", "vacation.conf"} {
		if b, err := os.ReadFile(filepath.Join(ddir, "email", f)); err == nil && len(bytes.TrimSpace(b)) > 0 {
			nb.Skipped = append(nb.Skipped, fmt.Sprintf("mail_%s:%s: not carried; set it up again in the panel", strings.TrimSuffix(f, ".conf"), dom))
		}
	}
}

// domainFilesRead are the backup/<dom>/ entries the import reads or reports
// on its own (noteUncarried); domain.usage and domain.ip_list are the source's
// bandwidth tally and IP assignment, and ftp.conf its FTP settings.
var domainFilesRead = map[string]bool{
	"domain.conf": true, "subdomain.list": true, "domain.cert": true, "domain.key": true,
	"domain.cacert": true, "ftp.passwd": true, "ftp.conf": true, "domain.usage": true,
	"domain.ip_list": true, "email/passwd": true, "email/aliases": true, "email/email.conf": true,
	"email/autoresponder.conf": true, "email/vacation.conf": true, "email/reply": true,
}

// noteUnread reports what else backup/<dom>/ holds (domain pointers, mailing
// lists, a catch-all), grouped by item, so nothing in it goes unmentioned.
func (nb *NativeBackup) noteUnread(dom, ddir string) {
	counts := map[string]int{}
	_ = filepath.WalkDir(ddir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(ddir, p)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		key := parts[0]
		if key == "email" && len(parts) > 1 {
			key += "/" + parts[1]
		}
		if !domainFilesRead[key] && key != dom+".db" {
			counts[key]++
		}
		return nil
	})
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		nb.Skipped = append(nb.Skipped, fmt.Sprintf("da_not_carried:%s: %s (%d file(s))", dom, k, counts[k]))
	}
	if kv, err := readKVFile(filepath.Join(ddir, "email", "email.conf")); err == nil {
		switch c := kv["catchall"]; c {
		case "", ":fail:", ":blackhole:":
		default:
			nb.Skipped = append(nb.Skipped, fmt.Sprintf("mail_catchall:%s: %q not carried; set it again in the panel", dom, c))
		}
	}
}

// settleMailDomain decides whether dom keeps mail service. DA writes
// imap/<dom>/ for every domain, and the import turns mail on for any domain
// with a directory under the mail root, so an empty one is removed: a domain
// with no accounts, forwarders or mailboxes on the source stays without mail
// (GH #327). A domain with forwarders but no mailbox gets the directory.
func (nb *NativeBackup) settleMailDomain(dom string, mail bool) {
	d := filepath.Join(nb.MailRoot, dom)
	if !mail {
		if err := os.Remove(d); err == nil || errors.Is(err, fs.ErrNotExist) {
			return
		}
		// Not empty: mailboxes on disk without an entry in email/passwd.
	} else if err := os.MkdirAll(d, 0o750); err != nil {
		nb.Skipped = append(nb.Skipped, fmt.Sprintf("mail_dir:%s:%v", dom, err))
		return
	}
	nb.MailDomains = append(nb.MailDomains, dom)
}

func (nb *NativeBackup) isMailDomain(dom string) bool {
	for _, d := range nb.MailDomains {
		if d == dom {
			return true
		}
	}
	return false
}

// relocateSubdomains moves each DA subdomain's directory out of its parent's
// docroot (DA serves sub.<dom> from domains/<dom>/public_html/<sub>) into
// domains/<sub>.<dom>/public_html: a jabali domain's docroot stays inside its
// own domains/<name>/ tree, as the cPanel per-domain split does.
func (nb *NativeBackup) relocateSubdomains() {
	for _, sub := range nb.subdomainNames() {
		parent := nb.Subdomains[sub]
		label := strings.TrimSuffix(sub, "."+parent)
		src := filepath.Join(nb.HomeDir, "domains", parent, "public_html", label)
		dst := filepath.Join(nb.HomeDir, "domains", sub, "public_html")
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			nb.Skipped = append(nb.Skipped, fmt.Sprintf("subdomain %s: %v", sub, err))
			continue
		}
		if fi, err := os.Lstat(src); err == nil && fi.IsDir() {
			move := os.Rename
			if _, err := os.Lstat(dst); err == nil {
				// A resumed job re-extracts into the same directory: dst holds
				// the previous run's copy of the same files.
				move = mergeDir
			}
			if err := move(src, dst); err != nil {
				nb.Skipped = append(nb.Skipped, fmt.Sprintf("subdomain %s: %v", sub, err))
			}
			continue
		}
		_ = os.MkdirAll(dst, 0o750)
	}
}

// mergeDir moves src's entries into dst, replacing the files dst already has,
// then removes src. Only directories this parser extracted reach it, and the
// extractor writes no symlinks.
func mergeDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		s, d := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		if e.IsDir() {
			if fi, err := os.Lstat(d); err == nil && fi.IsDir() {
				if err := mergeDir(s, d); err != nil {
					return err
				}
				continue
			}
		}
		if err := os.Rename(s, d); err != nil {
			return err
		}
	}
	return os.Remove(src)
}

func (nb *NativeBackup) subdomainNames() []string {
	subs := make([]string, 0, len(nb.Subdomains))
	for s := range nb.Subdomains {
		subs = append(subs, s)
	}
	sort.Strings(subs)
	return subs
}

// stageCert writes backup/<dom>/domain.{cert,cacert,key} as
// apache_tls/<dom>/{certificates,key}, the directory layout cpanel.ImportSSL
// reads. It installs them only with --preserve-source-state.
func (nb *NativeBackup) stageCert(dom, ddir string) {
	cert, cerr := os.ReadFile(filepath.Join(ddir, "domain.cert"))
	key, kerr := os.ReadFile(filepath.Join(ddir, "domain.key"))
	if cerr != nil || kerr != nil || len(bytes.TrimSpace(cert)) == 0 || len(bytes.TrimSpace(key)) == 0 {
		return
	}
	chain := bytes.TrimSpace(cert)
	if ca, err := os.ReadFile(filepath.Join(ddir, "domain.cacert")); err == nil && len(bytes.TrimSpace(ca)) > 0 {
		chain = append(append(chain, '\n'), bytes.TrimSpace(ca)...)
	}
	dst := filepath.Join(nb.ExtractDir, "apache_tls", dom)
	if err := os.MkdirAll(dst, 0o750); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dst, "certificates"), append(chain, '\n'), 0o600)
	_ = os.WriteFile(filepath.Join(dst, "key"), append(bytes.TrimSpace(key), '\n'), 0o600)
}

// indexDatabases lists the dumps and reads each <db>.conf for the source's
// database users (kept only with --preserve-source-state, see ImportDatabases).
func (nb *NativeBackup) indexDatabases() {
	dumps, _ := filepath.Glob(filepath.Join(nb.ExtractDir, "mysql", "*.sql"))
	sort.Strings(dumps)
	nb.MySQLDumps = dumps
	byUser := map[string]*cpanel.CompatUser{}
	var order []string
	for _, dump := range dumps {
		db := strings.TrimSuffix(filepath.Base(dump), ".sql")
		conf, err := os.ReadFile(filepath.Join(nb.ExtractDir, "backup", db+".conf"))
		if err != nil {
			continue
		}
		for _, u := range parseDBConf(db, conf, &nb.Skipped) {
			if cu, ok := byUser[u.Name]; ok {
				if cu.Hash == u.Hash {
					cu.Grant = append(cu.Grant, u.Grant...)
				}
				continue
			}
			uu := u
			byUser[u.Name] = &uu
			order = append(order, u.Name)
		}
	}
	for _, n := range order {
		nb.CompatUsers = append(nb.CompatUsers, *byUser[n])
	}
}

// parseDBConf reads a DA <db>.conf:
//
//	accesshosts=0=localhost
//	<dbuser>=accesshosts=localhost&...&passwd=*<40 hex>&plugin=mysql_native_password&...
//	db_collation=DEFAULT_CHARACTER_SET_NAME=...&SCHEMA_NAME=<db>
func parseDBConf(db string, raw []byte, skipped *[]string) []cpanel.CompatUser {
	var out []cpanel.CompatUser
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		k, v, ok := strings.Cut(line, "=")
		if !ok || k == "accesshosts" || k == "db_collation" {
			continue
		}
		if !nativeDBRe.MatchString(k) {
			*skipped = append(*skipped, fmt.Sprintf("db_user_name:%s:%q", db, k))
			continue
		}
		q, err := url.ParseQuery(v)
		if err != nil {
			*skipped = append(*skipped, fmt.Sprintf("db_user_conf:%s:%s", db, k))
			continue
		}
		hash := strings.ToUpper(q.Get("passwd"))
		if plugin := q.Get("plugin"); (plugin != "" && plugin != "mysql_native_password") || !cpanel.IsNativePasswordHash(hash) {
			*skipped = append(*skipped, fmt.Sprintf("db_user %s: no mysql_native_password hash; not recreated", k))
			continue
		}
		out = append(out, cpanel.CompatUser{
			Name:  k,
			Host:  "localhost",
			Hash:  hash,
			Grant: []cpanel.CompatGrant{{SourceDB: db, Privs: []string{"ALL"}}},
		})
	}
	return out
}

// convertCron turns crontab.conf into a crontab. DA keeps the environment as
// MAILTO=/PATH=/SHELL= and each job as <id>=<schedule> <command>. PATH and
// SHELL name the source server's PHP and jail shell, so only MAILTO carries.
func (nb *NativeBackup) convertCron() {
	kv, err := readKVFile(filepath.Join(nb.ExtractDir, "backup", "crontab.conf"))
	if err != nil {
		return
	}
	var jobs []string
	ids := make([]string, 0, len(kv))
	for k := range kv {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	for _, k := range ids {
		if strings.Trim(k, "0123456789") != "" {
			continue
		}
		line := strings.TrimSpace(kv[k])
		if len(strings.Fields(line)) < 6 && !strings.HasPrefix(line, "@") {
			nb.Skipped = append(nb.Skipped, fmt.Sprintf("cron_line:%s", k))
			continue
		}
		jobs = append(jobs, line)
	}
	if len(jobs) == 0 {
		return
	}
	var b strings.Builder
	if m := strings.TrimSpace(kv["MAILTO"]); m != "" && !strings.ContainsAny(m, "\n\r ") {
		fmt.Fprintf(&b, "MAILTO=%s\n", m)
	}
	for _, j := range jobs {
		b.WriteString(j + "\n")
	}
	p := filepath.Join(nb.ExtractDir, "cron", nb.SourceUser)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o640); err == nil {
		nb.CronFile = p
	}
}

// appConfigNames are the files that mark an application ImportAppConfigs
// rewrites to the new database user.
var appConfigNames = []string{"wp-config.php", "configuration.php"}

// appRootSkip are directories no application root sits inside.
var appRootSkip = map[string]bool{
	"wp-content": true, "wp-includes": true, "wp-admin": true, "node_modules": true,
	"vendor": true, ".git": true, "cache": true, ".well-known": true,
}

// findAppRoots looks for an application installed in a subdirectory of a
// docroot (a shop at public_html/<dir>/), up to three levels down.
func (nb *NativeBackup) findAppRoots() {
	for _, dom := range append(append([]string(nil), nb.Domains...), nb.subdomainNames()...) {
		root := filepath.Join(nb.HomeDir, "domains", dom, "public_html")
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			if rel == "." {
				return nil
			}
			if appRootSkip[d.Name()] || strings.Count(rel, string(filepath.Separator)) >= 3 {
				return filepath.SkipDir
			}
			for _, n := range appConfigNames {
				if isRegular(filepath.Join(p, n)) {
					nb.AppRoots[dom] = append(nb.AppRoots[dom], filepath.ToSlash(rel))
					break
				}
			}
			return nil
		})
	}
}

// ToCpanelParsed adapts the backup for the cPanel restore writers.
// targetUsername names the jabali account it lands in.
func (nb *NativeBackup) ToCpanelParsed(targetUsername string) *cpanel.ParsedTarball {
	p := &cpanel.ParsedTarball{
		SourceUser:  nb.SourceUser,
		ExtractDir:  nb.ExtractDir,
		HomeDir:     nb.HomeDir,
		MailRoot:    nb.MailRoot,
		MySQLDumps:  nb.MySQLDumps,
		ZoneFiles:   nb.ZoneFiles,
		CompatUsers: nb.CompatUsers,
		DocRoots:    map[string]string{},
		Skipped:     append([]string(nil), nb.Skipped...),
		// DA keeps a zone per domain (ZoneFiles); a subdomain's records live
		// in its parent's zone. So a subdomain is a web-only domain here, not
		// a zone of its own.
		SeparateWebDNSLists: true,
	}
	for _, dom := range append(append([]string(nil), nb.Domains...), nb.subdomainNames()...) {
		docroot := filepath.Join("/home", targetUsername, "domains", dom, "public_html")
		p.DomainNames = append(p.DomainNames, dom)
		p.DocRoots[dom] = docroot
		for _, rel := range nb.AppRoots[dom] {
			p.ExtraAppRoots = append(p.ExtraAppRoots, filepath.Join(docroot, filepath.FromSlash(rel)))
		}
	}
	if nb.OwnerMaildir && nb.PrimaryDomain != "" {
		p.OwnerEmail = nb.SourceUser + "@" + nb.PrimaryDomain
	}
	if nb.CronFile != "" {
		p.CronFiles = []string{nb.CronFile}
	}
	p.MailShadowDir = nb.MailShadowDir
	if nb.SSHKeys != "" {
		p.SSHAuthorized = []string{nb.SSHKeys}
	}
	return p
}

// --- archive plumbing ---

// openArchive returns the tar stream inside path: zstd (through the zstd
// binary, which install.sh installs), gzip or plain, by magic bytes.
func openArchive(path string) (io.Reader, func() error, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	magic := make([]byte, 4)
	n, _ := io.ReadFull(f, magic)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, nil, err
	}
	switch {
	case n >= 4 && magic[0] == 0x28 && magic[1] == 0xb5 && magic[2] == 0x2f && magic[3] == 0xfd:
		f.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cmd := exec.CommandContext(ctx, "zstd", "-dcq", "--", path)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.StdoutPipe()
		if err != nil {
			cancel()
			return nil, nil, err
		}
		if err := cmd.Start(); err != nil {
			cancel()
			return nil, nil, fmt.Errorf("zstd: %w", err)
		}
		zr := &zstdReader{r: out, cmd: cmd, stderr: &stderr}
		return bufio.NewReaderSize(zr, 1<<20), func() error {
			cancel() // stops zstd when the caller didn't read to the end
			_ = zr.wait()
			return nil
		}, nil
	case n >= 2 && magic[0] == 0x1f && magic[1] == 0x8b:
		gz, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			return nil, nil, fmt.Errorf("gunzip: %w", err)
		}
		return gz, func() error { gz.Close(); return f.Close() }, nil
	default:
		return bufio.NewReaderSize(f, 1<<20), f.Close, nil
	}
}

// zstdReader reads zstd's output and, at its end, its exit status: a zstd
// that failed part way (a truncated or corrupt archive) is an error, not the
// clean end of a shorter archive.
type zstdReader struct {
	r      io.Reader
	cmd    *exec.Cmd
	stderr *bytes.Buffer
	once   sync.Once
	err    error
	final  error // what every Read returns once zstd's output ended
}

func (z *zstdReader) wait() error {
	z.once.Do(func() { z.err = z.cmd.Wait() })
	return z.err
}

func (z *zstdReader) Read(p []byte) (int, error) {
	if z.final != nil {
		return 0, z.final
	}
	n, err := z.r.Read(p)
	if err == io.EOF {
		if werr := z.wait(); werr != nil {
			err = fmt.Errorf("zstd: %w: %s", werr, strings.TrimSpace(z.stderr.String()))
		}
		z.final = err
	}
	return n, err
}

// drain reads the stream past the tar end-of-archive marker, so a
// decompressor that failed after it still fails the parse (zstd's exit
// status, gzip's checksum).
func drain(r io.Reader) error {
	_, err := io.Copy(io.Discard, r)
	return err
}

// cleanParts splits an archive path, refusing an absolute or escaping one.
func cleanParts(name string) ([]string, bool) {
	if name == "" || strings.HasPrefix(name, "/") {
		return nil, name == ""
	}
	clean := filepath.Clean(name)
	if clean == "." {
		return nil, true
	}
	parts := strings.Split(clean, "/")
	for _, p := range parts {
		if p == ".." || p == "" {
			return nil, false
		}
	}
	return parts, true
}

type extractor struct {
	root   string
	budget *cpanel.ExtractBudget
}

func newExtractor(root string) *extractor {
	return &extractor{root: root, budget: cpanel.NewExtractBudget()}
}

// write creates one entry under root. Files never follow a symlink, and the
// archive carries none.
func (x *extractor) write(parts []string, hdr *tar.Header, r io.Reader) error {
	dest := filepath.Join(append([]string{x.root}, parts...)...)
	if rel, err := filepath.Rel(x.root, dest); err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return fmt.Errorf("refusing %s outside %s", dest, x.root)
	}
	if hdr.Typeflag == tar.TypeDir {
		return os.MkdirAll(dest, 0o750)
	}
	if hdr.Size > cpanel.MaxEntrySize {
		return fmt.Errorf("%s: %d bytes is over the per-file limit", hdr.Name, hdr.Size)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return err
	}
	mode := os.FileMode(0o640)
	if hdr.Mode&0o111 != 0 {
		mode = 0o750
	}
	w, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return err
	}
	if _, err := x.budget.Charge(w, r); err != nil {
		w.Close()
		return fmt.Errorf("write %s: %w", dest, err)
	}
	return w.Close()
}

func readKV(r io.Reader) (map[string]string, error) {
	kv := map[string]string{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimRight(sc.Text(), "\r"), "=")
		if ok && k != "" {
			kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return kv, sc.Err()
}

func readKVFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readKV(io.LimitReader(f, nativeMaxMetaBytes))
}

func isRegular(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode().IsRegular()
}

func writeFileMode(dst string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	return os.WriteFile(dst, b, mode)
}
