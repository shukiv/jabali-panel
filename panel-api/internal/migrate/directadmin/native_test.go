package directadmin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The fixture follows the layout of a real DirectAdmin 1.714 user backup:
// backup/ first, then domains/, then imap/; DB dumps flat in
// backup/; the home directory as a nested backup/home.tar.zst whose paths
// are relative to the home; the account's own Maildir inside it.

type entry struct {
	name string
	body string
	typ  byte
}

func file(name, body string) entry { return entry{name: name, body: body, typ: tar.TypeReg} }
func dir(name string) entry        { return entry{name: name, typ: tar.TypeDir} }

func tarBytes(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: 0o644}
		switch e.typ {
		case tar.TypeDir:
			h.Mode = 0o755
		case tar.TypeSymlink:
			h.Linkname = e.body
		case tar.TypeReg:
			h.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zstdBytes(t *testing.T, raw []byte) []byte {
	t.Helper()
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd CLI not available")
	}
	cmd := exec.Command("zstd", "-q", "-c")
	cmd.Stdin = bytes.NewReader(raw)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("zstd: %v", err)
	}
	return out
}

func gzipBytes(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const (
	wpHash   = "*ABCDEF0123456789ABCDEF0123456789ABCDEF01"
	shopHash = "*FEDCBA9876543210FEDCBA9876543210FEDCBA98"
	certPEM  = "-----BEGIN CERTIFICATE-----\nLEAF\n-----END CERTIFICATE-----"
	caPEM    = "-----BEGIN CERTIFICATE-----\nCHAIN\n-----END CERTIFICATE-----"
	keyPEM   = "-----BEGIN PRIVATE KEY-----\nKEY\n-----END PRIVATE KEY-----"
	shopDir  = "parts_shop"
)

func homeArchive(t *testing.T) []byte {
	return zstdBytes(t, tarBytes(t, []entry{
		file(".bashrc", "# bashrc\n"),
		dir(".redis/"), dir(".redis/db/"),
		file(".redis/db/dump.rdb", "REDIS"),
		dir(".wp-cli/"), dir(".wp-cli/cache/"), dir(".wp-cli/cache/core/"),
		file(".wp-cli/cache/core/wordpress.zip", "ZIP"),
		file(".wp-cli/config.yml", "path: x\n"),
		file(".imunify_patch_id", "id"),
		dir(".ssh/"),
		file(".ssh/authorized_keys", "ssh-ed25519 AAAA test\n"),
		dir("Maildir/"), dir("Maildir/new/"), dir("Maildir/.INBOX.spam/"), dir("Maildir/.INBOX.spam/cur/"),
		file("Maildir/new/1794769494.M676395P435978.srchost", "Subject: owner\n\nhi\n"),
		file("Maildir/.INBOX.spam/cur/2:2,S", "Subject: spam\n\nx\n"),
		dir("domains/"),
		file("domains/stray.txt", "not carried from the home archive"),
		file("../escape-home", "x"),
	}))
}

func daArchive(t *testing.T) []byte { return tarBytes(t, daEntries(t)) }

// daArchiveWithout leaves out the entries whose name matches drop.
func daArchiveWithout(t *testing.T, drop func(string) bool) []byte {
	var kept []entry
	for _, e := range daEntries(t) {
		if !drop(e.name) {
			kept = append(kept, e)
		}
	}
	return tarBytes(t, kept)
}

func daEntries(t *testing.T) []entry {
	return []entry{
		dir("backup/"),
		dir("backup/main.example/"),
		dir("backup/main.example/email/"),
		file("backup/main.example/email/passwd", "info:$6$salt$hash:1001:1001::/home/acct/imap/main.example/info:/bin/false\n"),
		file("backup/main.example/email/aliases", "acct: acct\nsales: x@example.com\nsupport: info, y@example.org\nbounce: :fail: no such user\npipe: |/usr/local/bin/x\nbad line\n"),
		file("backup/main.example/email/autoresponder.conf", "info=1\n"),
		file("backup/main.example/email/vacation.conf", ""),
		file("backup/main.example/email/email.conf", "catchall=:fail:\n"),
		file("backup/main.example/domain.pointers", "alias.example=type=alias\n"),
		dir("backup/main.example/email/majordomo/"),
		file("backup/main.example/email/majordomo/list", "x"),
		file("backup/main.example/email/majordomo/list.info", "x"),
		file("backup/main.example/domain.usage", "bandwidth=1\n"),
		file("backup/other.example/email/email.conf", "catchall=postmaster@other.example\n"),
		// DA 1.714's real shape (from a real archive): <login>=<url-query>.
		file("backup/main.example/ftp.passwd", "acct=passwd=$6$x$y&path=/home/acct/&type=system\nuploads@main.example=passwd=$6$z$w&path=/home/acct/domains/main.example/public_ftp/&type=virtual\n"),
		file("backup/main.example/subdomain.list", "shop\n\nBad_Label!\n"),
		file("backup/main.example/domain.conf", "domain=main.example\nusername=acct\n"),
		file("backup/main.example/domain.cacert", caPEM),
		file("backup/main.example/domain.cert", certPEM),
		file("backup/main.example/domain.key", keyPEM),
		file("backup/main.example/main.example.db", "$TTL 3600\nmain.example. IN A 192.0.2.10\n"),
		dir("backup/other.example/"),
		file("backup/other.example/domain.conf", "domain=other.example\n"),
		file("backup/other.example/other.example.db", "$TTL 3600\nother.example. IN A 192.0.2.10\n"),
		file("backup/other.example/ftp.passwd", "acct=passwd=$6$x$y&path=/home/acct/&type=system\n"),
		dir("backup/email_data/"),
		file("backup/user_limit", "0"),
		file("backup/acct_shop.conf", "accesshosts=0=localhost\nacct_shop=accesshosts=localhost&alter_priv=Y&passwd="+strings.ToLower(shopHash)+"&plugin=mysql_native_password&select_priv=Y\ndb_collation=DEFAULT_CHARACTER_SET_NAME=latin1&SCHEMA_NAME=acct_shop\n"),
		file("backup/acct_shop.sql", "-- shop dump\n"),
		file("backup/acct_wp.conf", "accesshosts=0=localhost\nacct_wp=accesshosts=localhost&passwd="+wpHash+"&plugin=mysql_native_password\nacct_ro=accesshosts=localhost&passwd=$A$005$x&plugin=caching_sha2_password\nacct_ed=accesshosts=localhost&passwd="+wpHash+"&plugin=ed25519\ndb_collation=DEFAULT_CHARACTER_SET_NAME=latin1&SCHEMA_NAME=acct_wp\n"),
		file("backup/acct_wp.sql", "/*M!999999\\- enable the sandbox mode */\n-- wp dump\n"),
		dir("backup/php/"),
		file("backup/user.conf", "account=ON\ndomain=main.example\nemail=owner@example.net\nname=acct\nusername=acct\nusertype=user\n"),
		file("backup/crontab.conf", "MAILTO=\nPATH=/usr/local/php81/bin:/usr/bin\nSHELL=/usr/bin/jailshell\n1=*/5 * * * * /usr/local/bin/php /home/acct/domains/main.example/public_html/wp-cron.php\n2=broken\n"),
		file("backup/.shadow", "$6$sourcesalt$sourcehash\n"),
		dir("backup/login_keys/"), dir("backup/login_keys/rootauth/"),
		file("backup/login_keys/rootauth/key.conf", "SECRET"),
		file("backup/user.db", "SQLite format 3"),
		file("backup/backup_options.list", "database\n"),
		file("backup/home.tar.zst", string(homeArchive(t))),
		dir("domains/"),
		dir("domains/main.example/"),
		dir("domains/main.example/public_html/"),
		file("domains/main.example/public_html/index.php", "<?php // main"),
		file("domains/main.example/public_html/wp-config.php", "define('DB_NAME', 'acct_wp');"),
		dir("domains/main.example/public_html/" + shopDir + "/"),
		file("domains/main.example/public_html/"+shopDir+"/wp-config.php", "define('DB_NAME', 'acct_shop');"),
		dir("domains/main.example/public_html/" + shopDir + "/wp-content/"),
		dir("domains/main.example/public_html/" + shopDir + "/wp-content/x/"),
		file("domains/main.example/public_html/"+shopDir+"/wp-content/x/wp-config.php", "decoy"),
		dir("domains/main.example/public_html/shop/"),
		file("domains/main.example/public_html/shop/index.html", "shop"),
		dir("domains/main.example/public_ftp/"),
		dir("domains/other.example/"),
		dir("domains/other.example/public_html/"),
		file("domains/other.example/public_html/index.html", "other"),
		dir("domains/main.example/logs/"),
		file("domains/main.example/logs/main.example.log", "GET /"),
		dir("domains/main.example/stats/"),
		file("domains/main.example/stats/index.html", "stats"),
		{name: "domains/main.example/public_html/link", body: "/etc/passwd", typ: tar.TypeSymlink},
		file("../../escape", "x"),
		file("/abs-escape", "x"),
		dir("imap/"),
		dir("imap/main.example/"),
		dir("imap/main.example/info/"),
		dir("imap/main.example/info/Maildir/"),
		dir("imap/main.example/info/Maildir/new/"),
		file("imap/main.example/info/Maildir/new/3", "Subject: info\n\nx\n"),
		file("imap/main.example/bad name!/x", "x"),
		dir("imap/other.example/"),
	}
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func writeArchive(t *testing.T, raw []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "user.admin.acct.tar.zst")
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func parseFixture(t *testing.T, wrap func(*testing.T, []byte) []byte) (string, *NativeBackup) {
	t.Helper()
	path := writeArchive(t, wrap(t, daArchive(t)))
	x := filepath.Join(t.TempDir(), "extract")
	nb, err := ParseNativeBackup(path, x)
	if err != nil {
		t.Fatal(err)
	}
	return x, nb
}

func TestParseNativeBackup_RealLayout(t *testing.T) {
	for _, tc := range []struct {
		name string
		wrap func(*testing.T, []byte) []byte
	}{
		{"zstd", zstdBytes},
		{"gzip", gzipBytes},
		{"plain", func(_ *testing.T, b []byte) []byte { return b }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeArchive(t, tc.wrap(t, daArchive(t)))
			if !IsNativeArchive(path) {
				t.Fatal("not recognized as a DirectAdmin backup")
			}
			x := filepath.Join(t.TempDir(), "extract")
			nb, err := ParseNativeBackup(path, x)
			if err != nil {
				t.Fatal(err)
			}
			checkNative(t, x, nb)
		})
	}
}

func checkNative(t *testing.T, x string, nb *NativeBackup) {
	t.Helper()
	if nb.SourceUser != "acct" || nb.Email != "owner@example.net" || nb.PrimaryDomain != "main.example" {
		t.Errorf("account: user=%q email=%q domain=%q", nb.SourceUser, nb.Email, nb.PrimaryDomain)
	}
	if nb.ShadowHash != "$6$sourcesalt$sourcehash" {
		t.Errorf("shadow hash %q", nb.ShadowHash)
	}
	if !reflect.DeepEqual(nb.Domains, []string{"main.example", "other.example"}) {
		t.Errorf("domains %v", nb.Domains)
	}
	if !reflect.DeepEqual(nb.Subdomains, map[string]string{"shop.main.example": "main.example"}) {
		t.Errorf("subdomains %v", nb.Subdomains)
	}
	if len(nb.ZoneFiles) != 2 {
		t.Errorf("zones %v", nb.ZoneFiles)
	}
	// Mail stays on only where the source had accounts or forwarders; DA's
	// empty imap/other.example/ is removed so the import doesn't turn mail on.
	if !reflect.DeepEqual(nb.MailDomains, []string{"main.example"}) {
		t.Errorf("mail domains %v", nb.MailDomains)
	}

	// Site files land in the jabali home layout; DA's logs and stats don't.
	for _, p := range []string{
		"home/domains/main.example/public_html/index.php",
		"home/domains/main.example/public_html/" + shopDir + "/wp-config.php",
		"home/domains/main.example/public_ftp",
		"home/domains/shop.main.example/public_html/index.html",
		"home/.bashrc",
		"home/.wp-cli/config.yml",
		"home/.ssh/authorized_keys",
		"imap/new/1794769494.M676395P435978.srchost",
		"imap/.INBOX.spam/cur/2:2,S",
		"imap/main.example/info/Maildir/new/3",
		"mysql/acct_wp.sql",
		"mysql/acct_shop.sql",
		"backup/main.example/ftp.passwd",
		"home/domains/other.example/public_html/index.html",
		"imap/main.example/conf/passwd",
	} {
		if !exists(filepath.Join(x, p)) {
			t.Errorf("missing %s", p)
		}
	}
	for _, p := range []string{
		"home/domains/main.example/public_html/shop", // moved to the subdomain's own docroot
		"home/domains/main.example/logs",
		"home/domains/main.example/stats",
		"home/domains/main.example/public_html/link",
		"home/domains/stray.txt",
		"home/.redis",
		"home/.wp-cli/cache",
		"home/.imunify_patch_id",
		"home/Maildir",
		"backup/login_keys",
		"backup/user.db",
		"backup/home.tar.zst",
		"backup/acct_wp.sql",
		"abs-escape",
		"imap/other.example",
		"etc/other.example",
	} {
		if exists(filepath.Join(x, p)) {
			t.Errorf("%s should not be there", p)
		}
	}
	if exists(filepath.Join(filepath.Dir(x), "escape")) || exists(filepath.Join(filepath.Dir(x), "escape-home")) {
		t.Error("an escaping entry was written outside the extract dir")
	}
	joined := strings.Join(nb.Skipped, "\n")
	for _, want := range []string{
		"path_escape:../../escape",
		"path_escape:/abs-escape",
		"entry_type:2:domains/main.example/public_html/link",
		"home_path_escape:../escape-home",
		"da_generated: 2 file(s)",
		"home_cache: 3 file(s)",
		`subdomain_name:main.example:"bad_label!"`,
		"db_user acct_ro: no mysql_native_password hash",
		"db_user acct_ed: no mysql_native_password hash",
		"imap_local_name:imap/main.example/bad name!/x",
		"cron_line:2",
		"alias_local_target:support@main.example→info",
		"alias_directive:bounce@main.example::fail:",
		"alias_directive:pipe@main.example:|/usr/local/bin/x",
		`alias_malformed:main.example:"bad line"`,
		"ftp_accounts:main.example: 1 FTP account(s)",
		"mail_autoresponder:main.example",
		"da_not_carried:main.example: domain.pointers (1 file(s))",
		"da_not_carried:main.example: email/majordomo (2 file(s))",
		`mail_catchall:other.example: "postmaster@other.example" not carried`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("skipped lacks %q:\n%s", want, joined)
		}
	}
	for _, unwanted := range []string{"domain_dir_name", "cron_line:PATH", "cron_line:SHELL", "cron_line:MAILTO", "ftp_accounts:other.example", "domain.usage", "email.conf (", "email/reply", "main.example.db (", "mail_vacation", "acct@main.example→acct", "owner_mailbox"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("skipped has %q:\n%s", unwanted, joined)
		}
	}

	// Databases and their users (the hash upper-cased).
	if len(nb.MySQLDumps) != 2 {
		t.Errorf("dumps %v", nb.MySQLDumps)
	}
	users := map[string]string{}
	for _, u := range nb.CompatUsers {
		users[u.Name] = u.Hash
		if len(u.Grant) != 1 || u.Grant[0].SourceDB != u.Name || u.Host != "localhost" {
			t.Errorf("compat user %+v", u)
		}
	}
	if !reflect.DeepEqual(users, map[string]string{"acct_wp": wpHash, "acct_shop": shopHash}) {
		t.Errorf("compat users %v", users)
	}

	// Cron: the job, not the source's PATH or jail shell.
	cron, err := os.ReadFile(nb.CronFile)
	if err != nil {
		t.Fatalf("cron file: %v", err)
	}
	if string(cron) != "*/5 * * * * /usr/local/bin/php /home/acct/domains/main.example/public_html/wp-cron.php\n" {
		t.Errorf("cron %q", cron)
	}

	// Certificate, forwarders and mailbox hashes, staged where the writers read.
	certs, _ := os.ReadFile(filepath.Join(x, "apache_tls", "main.example", "certificates"))
	key, _ := os.ReadFile(filepath.Join(x, "apache_tls", "main.example", "key"))
	if string(certs) != certPEM+"\n"+caPEM+"\n" || string(key) != keyPEM+"\n" {
		t.Errorf("certificate staged as %q / %q", certs, key)
	}
	if fi, err := os.Stat(filepath.Join(x, "apache_tls", "main.example", "key")); err != nil || fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("key file mode: %v %v", fi, err)
	}
	if b, _ := os.ReadFile(filepath.Join(x, "etc", "main.example", "aliases")); string(b) != "sales: x@example.com\nsupport: y@example.org\n" {
		t.Errorf("aliases %q", b)
	}
	accounts := filepath.Join(x, "imap", "main.example", "conf", "passwd")
	if b, _ := os.ReadFile(accounts); string(b) != "info\n" {
		t.Errorf("account list %q (local parts only: the hashes come from the mail shadow)", b)
	}
	if fi, err := os.Stat(accounts); err != nil || fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("account list mode: %v %v", fi, err)
	}
	sh := filepath.Join(x, "mailshadow", "main.example", "shadow")
	if b, _ := os.ReadFile(sh); !strings.HasPrefix(string(b), "info:$6$salt$hash:") {
		t.Errorf("mail shadow %q", b)
	}
	if fi, err := os.Stat(sh); err != nil || fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("mail shadow: %v %v", fi, err)
	}

	if !reflect.DeepEqual(nb.AppRoots, map[string][]string{"main.example": {shopDir}}) {
		t.Errorf("app roots %v", nb.AppRoots)
	}
	if nb.SSHKeys == "" || !nb.OwnerMaildir {
		t.Errorf("ssh=%q owner maildir=%v", nb.SSHKeys, nb.OwnerMaildir)
	}
}

func TestNativeBackup_ToCpanelParsed(t *testing.T) {
	x, nb := parseFixture(t, zstdBytes)
	p := nb.ToCpanelParsed("newuser")
	if p.SourceUser != "acct" || p.HomeDir != filepath.Join(x, "home") || p.MailRoot != filepath.Join(x, "imap") {
		t.Errorf("roots: user=%q home=%q mail=%q", p.SourceUser, p.HomeDir, p.MailRoot)
	}
	if !reflect.DeepEqual(p.DomainNames, []string{"main.example", "other.example", "shop.main.example"}) {
		t.Errorf("domain names %v", p.DomainNames)
	}
	want := map[string]string{
		"main.example":      "/home/newuser/domains/main.example/public_html",
		"other.example":     "/home/newuser/domains/other.example/public_html",
		"shop.main.example": "/home/newuser/domains/shop.main.example/public_html",
	}
	if !reflect.DeepEqual(p.DocRoots, want) {
		t.Errorf("docroots %v", p.DocRoots)
	}
	if !reflect.DeepEqual(p.ExtraAppRoots, []string{"/home/newuser/domains/main.example/public_html/" + shopDir}) {
		t.Errorf("extra app roots %v", p.ExtraAppRoots)
	}
	if p.OwnerEmail != "acct@main.example" {
		t.Errorf("owner email %q", p.OwnerEmail)
	}
	if p.MailShadowDir != filepath.Join(x, "mailshadow") || len(p.CronFiles) != 1 || len(p.SSHAuthorized) != 1 ||
		len(p.CompatUsers) != 2 || len(p.ZoneFiles) != 2 || len(p.MySQLDumps) != 2 {
		t.Errorf("shadow=%q cron=%v ssh=%v compat=%d zones=%v dumps=%v",
			p.MailShadowDir, p.CronFiles, p.SSHAuthorized, len(p.CompatUsers), p.ZoneFiles, p.MySQLDumps)
	}
}

// A subdomain's records live in its parent's zone on DA: it must come out
// web-only, not as a zone of its own.
func TestNativeBackup_SubdomainIsWebOnly(t *testing.T) {
	_, nb := parseFixture(t, zstdBytes)
	p := nb.ToCpanelParsed("newuser")
	if !p.SeparateWebDNSLists {
		t.Fatal("web and DNS lists not marked separate")
	}
	for _, z := range p.ZoneFiles {
		if strings.HasPrefix(filepath.Base(z), "shop.") {
			t.Errorf("subdomain has a zone: %s", z)
		}
	}
}

// With no mail account or forwarder on the primary domain, its empty imap
// directory goes and the account's own mailbox (DA's notices) is not carried,
// so the import doesn't turn mail on for the domain.
func TestParseNativeBackup_NoMailAccounts(t *testing.T) {
	raw := daArchiveWithout(t, func(name string) bool {
		return strings.HasSuffix(name, "/email/passwd") || strings.HasPrefix(name, "imap/main.example/info")
	})
	// The aliases left: only DA's own "acct: acct" route, plus lines
	// that are not forwarders.
	path := writeArchive(t, zstdBytes(t, raw))
	x := filepath.Join(t.TempDir(), "extract")
	nb, err := ParseNativeBackup(path, x)
	if err != nil {
		t.Fatal(err)
	}
	if len(nb.MailDomains) != 1 || nb.MailDomains[0] != "main.example" {
		// sales/support forwarders still make main.example a mail domain.
		t.Fatalf("mail domains %v", nb.MailDomains)
	}

	raw = daArchiveWithout(t, func(name string) bool {
		return strings.HasSuffix(name, "/email/passwd") || strings.HasSuffix(name, "/email/aliases") ||
			strings.HasPrefix(name, "imap/main.example/info")
	})
	path = writeArchive(t, zstdBytes(t, raw))
	x = filepath.Join(t.TempDir(), "extract")
	nb, err = ParseNativeBackup(path, x)
	if err != nil {
		t.Fatal(err)
	}
	if len(nb.MailDomains) != 0 || nb.OwnerMaildir {
		t.Errorf("mail domains %v, owner maildir %v", nb.MailDomains, nb.OwnerMaildir)
	}
	if exists(filepath.Join(x, "imap", "main.example")) {
		t.Error("the empty imap/main.example was left: the import would turn mail on")
	}
	if p := nb.ToCpanelParsed("newuser"); p.OwnerEmail != "" || p.MailShadowDir != "" {
		t.Errorf("owner email %q, shadow %q", p.OwnerEmail, p.MailShadowDir)
	}
	if !strings.Contains(strings.Join(nb.Skipped, "\n"), "owner_mailbox: acct@main.example (2 file(s)) not carried") {
		t.Errorf("the dropped owner mailbox is not reported: %v", nb.Skipped)
	}
}

// A resumed job parses the backup again into the same directory; the
// subdomain's files still end up in its own docroot only.
func TestParseNativeBackup_Rerun(t *testing.T) {
	path := writeArchive(t, zstdBytes(t, daArchive(t)))
	x := filepath.Join(t.TempDir(), "extract")
	if _, err := ParseNativeBackup(path, x); err != nil {
		t.Fatal(err)
	}
	nb, err := ParseNativeBackup(path, x)
	if err != nil {
		t.Fatal(err)
	}
	checkNative(t, x, nb)
	for _, s := range nb.Skipped {
		if strings.HasPrefix(s, "subdomain ") {
			t.Errorf("re-run reported %q", s)
		}
	}
}

func TestPeekNativeMeta(t *testing.T) {
	path := writeArchive(t, zstdBytes(t, daArchive(t)))
	m, err := PeekNativeMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	want := NativeMeta{Username: "acct", Email: "owner@example.net", PrimaryDomain: "main.example", ShadowHash: "$6$sourcesalt$sourcehash"}
	if *m != want {
		t.Errorf("meta %+v", *m)
	}
}

func TestPeekNativeMeta_RefusesWhatIsNotADABackup(t *testing.T) {
	for name, entries := range map[string][]entry{
		"cpmove":       {dir("cpmove-bob/"), file("cpmove-bob/cp/bob", "USER=bob\n")},
		"no user.conf": {dir("backup/"), file("backup/crontab.conf", "MAILTO=\n"), dir("domains/")},
		"bad username": {dir("backup/"), file("backup/user.conf", "username=../root\n")},
	} {
		t.Run(name, func(t *testing.T) {
			path := writeArchive(t, tarBytes(t, entries))
			if _, err := PeekNativeMeta(path); err == nil {
				t.Fatal("accepted")
			}
			if name == "cpmove" && IsNativeArchive(path) {
				t.Error("a cpmove recognized as a DirectAdmin backup")
			}
			if _, err := ParseNativeBackup(path, filepath.Join(t.TempDir(), "x")); err == nil {
				t.Error("ParseNativeBackup accepted it")
			}
		})
	}
}

func TestNativeStagedPath(t *testing.T) {
	stage := t.TempDir()
	if NativeStagedPath(stage, "acct") != "" {
		t.Fatal("found a backup that isn't staged")
	}
	p := filepath.Join(stage, NativeStagedName("acct"))
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := NativeStagedPath(stage, "acct"); got != p {
		t.Errorf("got %q", got)
	}
	if NativeStagedPath(stage, "../acct") != "" {
		t.Error("an invalid account name resolved")
	}
	link := filepath.Join(stage, NativeStagedName("linked"))
	if err := os.Symlink(p, link); err != nil {
		t.Fatal(err)
	}
	if NativeStagedPath(stage, "linked") != "" {
		t.Error("a symlink accepted as the staged backup")
	}
}

func TestCheckNativeDiskSpace(t *testing.T) {
	path := writeArchive(t, zstdBytes(t, daArchive(t)))
	n, err := MeasureNative(path)
	if err != nil || n == 0 {
		t.Fatalf("measured %d, %v", n, err)
	}
	if err := CheckNativeDiskSpace(path, t.TempDir()); err != nil {
		t.Errorf("a tiny archive refused: %v", err)
	}
}

// A zstd archive that fails part way is an error, not a shorter import:
// zstd's exit status is read at the end of its output.
func TestParseNativeBackup_BrokenZstd(t *testing.T) {
	good := zstdBytes(t, daArchive(t))
	for name, raw := range map[string][]byte{
		"truncated":         good[:len(good)/2],
		"garbage after end": append(append([]byte(nil), good...), []byte("not a zstd frame, just bytes")...),
	} {
		t.Run(name, func(t *testing.T) {
			path := writeArchive(t, raw)
			if _, err := ParseNativeBackup(path, filepath.Join(t.TempDir(), "x")); err == nil || !strings.Contains(err.Error(), "zstd") {
				t.Errorf("parsed without a zstd error: %v", err)
			}
			if _, err := MeasureNative(path); err == nil {
				t.Error("measured without an error")
			}
		})
	}
}

// The same for the nested home archive.
func TestParseNativeBackup_BrokenHomeArchive(t *testing.T) {
	home := homeArchive(t)
	var entries []entry
	for _, e := range daEntries(t) {
		if e.name == "backup/home.tar.zst" {
			e.body = string(home[:len(home)/2])
		}
		entries = append(entries, e)
	}
	path := writeArchive(t, tarBytes(t, entries))
	if _, err := ParseNativeBackup(path, filepath.Join(t.TempDir(), "x")); err == nil || !strings.Contains(err.Error(), "home.tar.zst") {
		t.Errorf("a truncated home archive parsed: %v", err)
	}
}

// The shapes of the real main.example archive (DA 1.714): no mail account
// (email/passwd empty), only DA's own "<user>: <user>" alias, no subdomain,
// and ftp.passwd holding the system login alone.
func TestParseNativeBackup_RealShape(t *testing.T) {
	home := zstdBytes(t, tarBytes(t, []entry{
		dir("Maildir/"), dir("Maildir/new/"),
		file("Maildir/new/1.M1.srchost", "Subject: notice\n\nx\n"),
	}))
	raw := tarBytes(t, []entry{
		dir("backup/"), dir("backup/main.example/"), dir("backup/main.example/email/"),
		file("backup/main.example/email/passwd", ""),
		file("backup/main.example/email/aliases", "acct: acct\n"),
		file("backup/main.example/email/email.conf", "catchall=:fail:\n"),
		file("backup/main.example/subdomain.list", ""),
		file("backup/main.example/ftp.passwd", "acct=passwd=$6$REDACTED$REDACTED&path=/home/acct/&type=system\n"),
		file("backup/main.example/domain.conf", "domain=main.example\n"),
		file("backup/user.conf", "username=acct\ndomain=main.example\nemail=owner@example.net\n"),
		file("backup/home.tar.zst", string(home)),
		dir("domains/"), dir("domains/main.example/"), dir("domains/main.example/public_html/"),
		file("domains/main.example/public_html/index.php", "<?php"),
		dir("imap/"), dir("imap/main.example/"),
	})
	x := filepath.Join(t.TempDir(), "x")
	nb, err := ParseNativeBackup(writeArchive(t, zstdBytes(t, raw)), x)
	if err != nil {
		t.Fatal(err)
	}
	if len(nb.MailDomains) != 0 || len(nb.Subdomains) != 0 || nb.OwnerMaildir || nb.MailShadowDir != "" {
		t.Errorf("mail=%v subs=%v owner=%v shadow=%q", nb.MailDomains, nb.Subdomains, nb.OwnerMaildir, nb.MailShadowDir)
	}
	if exists(filepath.Join(x, "etc", "main.example", "aliases")) || exists(filepath.Join(x, "imap", "main.example")) {
		t.Error("a forwarder or mail directory was staged for a domain with no mail")
	}
	joined := strings.Join(nb.Skipped, "\n")
	if strings.Contains(joined, "ftp_accounts") || strings.Contains(joined, "alias_") {
		t.Errorf("the system FTP login or DA's own alias reported:\n%s", joined)
	}
	if !strings.Contains(joined, "owner_mailbox: acct@main.example (1 file(s)) not carried") {
		t.Errorf("owner mailbox not reported:\n%s", joined)
	}
}

// email/passwd in the name=<url-query> shape DA uses for ftp.passwd is read too.
func TestStageMailAccounts_QueryShape(t *testing.T) {
	x := t.TempDir()
	ddir := filepath.Join(x, "backup", "a.example")
	if err := os.MkdirAll(filepath.Join(ddir, "email"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ddir, "email", "passwd"), []byte("info=passwd=$6$s$h&quota=0\nsales:{SHA512-CRYPT}$6$a$b=:1:1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	nb := &NativeBackup{ExtractDir: x, MailRoot: filepath.Join(x, "imap")}
	if n := nb.stageMailAccounts("a.example", ddir); n != 2 {
		t.Fatalf("accounts %d", n)
	}
	sh, _ := os.ReadFile(filepath.Join(x, "mailshadow", "a.example", "shadow"))
	if string(sh) != "info:$6$s$h\nsales:{SHA512-CRYPT}$6$a$b=:1:1\n" {
		t.Errorf("shadow %q", sh)
	}
	list, _ := os.ReadFile(filepath.Join(x, "imap", "a.example", "conf", "passwd"))
	if string(list) != "info\nsales\n" {
		t.Errorf("account list %q", list)
	}
}
