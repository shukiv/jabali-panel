// Package cloudpanel implements the GH #522 Discoverer + Restorer for
// CloudPanel source panels. SSH-based connect (parity with the cpanel /
// directadmin packages); discovery reads CloudPanel's SQLite config
// database directly.
//
// CloudPanel stores its entire inventory in a single SQLite database at
// /home/clp/htdocs/app/data/db.sq3 (tables: site, php_settings, database,
// user). Enumeration is therefore plain read-only SELECTs via the `sqlite3`
// CLI over SSH — cleaner than the CLI-output scraping the cpanel/DA sources
// do. `clpctl` is used only for DB credentials + dumps (Step 2+), and
// `mysqldump` for the dumps themselves. We NEVER run a mutating command
// (`clpctl *:add/*:delete`, `site:*`) against the source.
//
// CloudPanel is web-only — it has no mail server — so the manifest's
// Mailboxes area is always empty (no mail phase, unlike cpanel/Plesk).
//
// Step 1 (this file) ships the Discoverer scaffold + Connect + ListAccounts
// and returns a valid (SchemaVersion + Source) empty manifest that the Step 2
// per-area builders fill — the same incremental shape the directadmin package
// took (see directadmin/discover.go).
package cloudpanel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// DefaultDBPath is where CloudPanel keeps its SQLite config database.
const DefaultDBPath = "/home/clp/htdocs/app/data/db.sq3"

// siteUserRe is the strict allow-list a CloudPanel site user / account id must
// match before it is ever interpolated into a SQL or shell string. CloudPanel
// site users are Linux usernames; anything outside this set is rejected rather
// than escaped, so no SQL/shell metacharacter can reach the source (defence in
// depth — DescribeAccount also validates via the Go-side account list).
var siteUserRe = regexp.MustCompile(`^[a-z_][a-z0-9._-]{0,63}$`)

// Discoverer is the CloudPanel-side implementation of migrate.Discoverer.
type Discoverer struct {
	// AllowPrivate — when true the SSRF guard permits RFC1918 / ULA targets.
	// Default false; the admin toggle (migration_allow_private_hosts) sets it
	// via migrate.ApplyAllowPrivate.
	AllowPrivate bool
	// Port is the source SSH port (GH #429 PortSetter). Default 22.
	Port int
	// CommandTimeout bounds each remote command. Default 30s.
	CommandTimeout time.Duration
	// DBPath is the CloudPanel SQLite database path on the source. Overridable
	// only for tests; production always uses DefaultDBPath.
	DBPath string
}

// New returns a Discoverer with sensible defaults.
func New() *Discoverer {
	return &Discoverer{
		Port:           22,
		CommandTimeout: 30 * time.Second,
		DBPath:         DefaultDBPath,
	}
}

// Compile-time interface assertions.
var _ migrate.Discoverer = (*Discoverer)(nil)
var _ migrate.AllowPrivateSetter = (*Discoverer)(nil)
var _ migrate.PortSetter = (*Discoverer)(nil)

// SetAllowPrivate — see cpanel/discover.go for rationale.
func (d *Discoverer) SetAllowPrivate(b bool) { d.AllowPrivate = b }

// SetPort sets the source SSH port; ApplyPort calls it via PortSetter. Ports
// outside 1..65535 keep the default 22.
func (d *Discoverer) SetPort(p int) {
	if p >= 1 && p <= 65535 {
		d.Port = p
	}
}

// runnerFunc executes one command on the source and returns stdout. Real
// sessions run over SSH (session.sshRun); tests inject a fake so the
// SQLite-parsing logic in ListAccounts / DescribeAccount is exercised without
// a live CloudPanel host.
type runnerFunc func(ctx context.Context, timeout time.Duration, cmd string) ([]byte, error)

type session struct {
	client         *ssh.Client
	connectedUser  string
	dbPath         string
	commandTimeout time.Duration
	// run is the command executor. Connect wires it to sshRun; tests set it
	// directly on a hand-built session.
	run runnerFunc
}

func (s *session) Kind() string { return models.MigrationSourceCloudPanel }

// Connect dials the source over SSH, verifies the host key, and validates the
// credentials with a single read: counting rows in CloudPanel's `site` table.
// A successful count proves (a) the SSH creds work, (b) `sqlite3` is present,
// and (c) the db.sq3 schema is really CloudPanel's — all before returning.
func (d *Discoverer) Connect(ctx context.Context, host, user string, secret migrate.SecretRef) (migrate.Session, error) {
	port := d.Port
	if port == 0 {
		port = 22
	}
	auth, err := loadSecret(secret.Path)
	if err != nil {
		return nil, fmt.Errorf("cloudpanel.Connect: load secret: %w", err)
	}
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            auth,
		HostKeyCallback: migrate.PinningHostKeyCallback(secret.ExpectedHostKey),
		Timeout:         15 * time.Second,
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := migrate.DialTCP(ctx, host, port, d.AllowPrivate, 15*time.Second)
	if err != nil {
		return nil, fmt.Errorf("cloudpanel.Connect: tcp dial %s: %w", addr, err)
	}
	c, ch, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		if strings.Contains(err.Error(), "unable to authenticate") || strings.Contains(err.Error(), "no supported methods remain") {
			return nil, fmt.Errorf("cloudpanel.Connect: source SSH server rejected the supplied auth method (likely PasswordAuthentication=no — upload an SSH PRIVATE KEY in the wizard's Connection step instead): %w", err)
		}
		return nil, fmt.Errorf("cloudpanel.Connect: ssh handshake: %w", err)
	}
	client := ssh.NewClient(c, ch, reqs)
	dbPath := d.DBPath
	if dbPath == "" {
		dbPath = DefaultDBPath
	}
	s := &session{
		client:         client,
		connectedUser:  user,
		dbPath:         dbPath,
		commandTimeout: d.CommandTimeout,
	}
	s.run = s.sshRun

	subctx, cancel := context.WithTimeout(ctx, d.CommandTimeout)
	defer cancel()
	if _, err := s.run(subctx, d.CommandTimeout, sqliteQuery(dbPath, "SELECT count(*) FROM site")); err != nil {
		client.Close()
		return nil, fmt.Errorf("cloudpanel.Connect: read probe failed — is this a CloudPanel host with %s readable by the SSH user (need root), and is sqlite3 installed? %w", dbPath, err)
	}
	return s, nil
}

// ListAccounts returns one row per CloudPanel SITE USER (a Linux user that may
// own several sites). Cheap mode — no per-account size lookup here; that lands
// on DescribeAccount / SizeProber (Step 2). The query is static (no user input)
// so it needs no sanitisation.
func (d *Discoverer) ListAccounts(ctx context.Context, raw migrate.Session) ([]migrate.AccountSummary, error) {
	s, ok := raw.(*session)
	if !ok {
		return nil, errors.New("cloudpanel.ListAccounts: wrong session type")
	}
	// user | comma-joined domains — one row per site user, so the admin picker
	// can show which domains ride along. group_concat keeps it a single round
	// trip. `clp` is CloudPanel's own service account, never a hosting user.
	q := "SELECT user, group_concat(domain_name, ',') FROM site " +
		"WHERE user IS NOT NULL AND user != '' GROUP BY user ORDER BY user"
	out, err := s.run(ctx, s.commandTimeout, sqliteQuerySep(s.dbPath, "|", q))
	if err != nil {
		return nil, fmt.Errorf("cloudpanel.ListAccounts: read site users: %w", err)
	}
	rows := []migrate.AccountSummary{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		user, domains, _ := strings.Cut(line, "|")
		user = strings.TrimSpace(user)
		if user == "" || user == "clp" {
			continue
		}
		primary := ""
		if d := strings.SplitN(domains, ",", 2); len(d) > 0 {
			primary = strings.TrimSpace(d[0])
		}
		rows = append(rows, migrate.AccountSummary{
			ID:     user,
			Login:  user,
			Domain: primary,
		})
	}
	return rows, nil
}

// DescribeAccount returns a manifest scaffold for one CloudPanel site user.
// Per-area builders (Domains / Databases / Cron / SSH) ship in Step 2. The
// account is validated against the Go-side account list (no user input reaches
// SQL) — plus a strict format check so later interpolating builders are safe.
func (d *Discoverer) DescribeAccount(ctx context.Context, raw migrate.Session, accountID string) (*migrate.AccountManifest, error) {
	s, ok := raw.(*session)
	if !ok {
		return nil, errors.New("cloudpanel.DescribeAccount: wrong session type")
	}
	if accountID == "" {
		return nil, errors.New("cloudpanel.DescribeAccount: accountID empty")
	}
	if !siteUserRe.MatchString(accountID) {
		return nil, fmt.Errorf("cloudpanel.DescribeAccount: invalid account id %q (must be a CloudPanel site user)", accountID)
	}

	accounts, err := d.ListAccounts(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("cloudpanel.DescribeAccount: enumerate accounts: %w", err)
	}
	found := false
	for _, a := range accounts {
		if a.ID == accountID {
			found = true
			break
		}
	}
	if !found {
		switch len(accounts) {
		case 0:
			return nil, fmt.Errorf("no CloudPanel site users found on source (queried %s); %q is not a hosting user", s.dbPath, accountID)
		case 1:
			// Single-tenant source — pivot to the only account, matching the
			// directadmin auto-detect convenience.
			accountID = accounts[0].ID
		default:
			names := make([]string, 0, len(accounts))
			for _, a := range accounts {
				names = append(names, a.ID)
			}
			return nil, fmt.Errorf("user %q is not a CloudPanel site user; pick one of: %s", accountID, strings.Join(names, ", "))
		}
	}

	host := ""
	if s.client != nil {
		host = s.client.RemoteAddr().String()
	}
	m := &migrate.AccountManifest{
		SchemaVersion: migrate.ManifestSchemaVersion,
		Source: migrate.SourceRef{
			Kind: models.MigrationSourceCloudPanel,
			Host: host,
			User: accountID,
		},
		Domains:   []migrate.DomainSpec{},
		Databases: []migrate.DatabaseSpec{},
		// CloudPanel has no mail server — Mailboxes stays empty by design.
		Mailboxes: []migrate.MailboxSpec{},
		Warnings:  []migrate.Warning{},
	}
	m.Domains = s.buildDomains(ctx, accountID, &m.Warnings)
	m.Databases = s.buildDatabases(ctx, accountID, &m.Warnings)
	m.Cron = s.buildCron(ctx, accountID, &m.Warnings)
	m.SSH = s.buildSSH(ctx, accountID, &m.Warnings)
	return m, nil
}

// buildCron returns the account's CloudPanel cron jobs. CloudPanel stores each
// job's schedule as five columns (minute..weekday) + command in cron_job,
// linked to a site. RunAs is the site user (the account).
func (s *session) buildCron(ctx context.Context, user string, warns *[]migrate.Warning) []migrate.CronSpec {
	q := "SELECT cj.minute, cj.hour, cj.day, cj.month, cj.weekday, cj.command " +
		"FROM cron_job cj JOIN site s ON cj.site_id = s.id WHERE s.user = '" + user + "' ORDER BY cj.id"
	b, err := s.run(ctx, s.commandTimeout, sqliteQuerySep(s.dbPath, "|", q))
	if err != nil {
		*warns = append(*warns, migrate.Warning{Code: "cloudpanel_cron", Detail: err.Error()})
		return []migrate.CronSpec{}
	}
	out := []migrate.CronSpec{}
	for _, line := range splitLines(b) {
		// SplitN keeps a command that itself contains a pipe intact in the last field.
		cols := strings.SplitN(line, "|", 6)
		if len(cols) < 6 {
			continue
		}
		command := strings.TrimSpace(cols[5])
		if command == "" {
			continue
		}
		schedule := strings.Join([]string{
			strings.TrimSpace(cols[0]), strings.TrimSpace(cols[1]), strings.TrimSpace(cols[2]),
			strings.TrimSpace(cols[3]), strings.TrimSpace(cols[4]),
		}, " ")
		out = append(out, migrate.CronSpec{Schedule: schedule, Command: command, RunAs: user})
	}
	return out
}

// buildSSH returns the authorized SSH keys across the account's sites
// (ssh_user.ssh_keys is an authorized_keys blob). Each line becomes an
// SSHKeySpec; a valid key gets its real SHA256 fingerprint, otherwise a stable
// hash of the raw line (so re-runs still de-dup).
func (s *session) buildSSH(ctx context.Context, user string, warns *[]migrate.Warning) []migrate.SSHKeySpec {
	q := "SELECT su.ssh_keys FROM ssh_user su JOIN site s ON su.site_id = s.id " +
		"WHERE s.user = '" + user + "' AND su.ssh_keys IS NOT NULL AND su.ssh_keys != ''"
	b, err := s.run(ctx, s.commandTimeout, sqliteQuerySep(s.dbPath, "|", q))
	if err != nil {
		*warns = append(*warns, migrate.Warning{Code: "cloudpanel_ssh", Detail: err.Error()})
		return []migrate.SSHKeySpec{}
	}
	out := []migrate.SSHKeySpec{}
	seen := map[string]bool{}
	for _, line := range splitLines(b) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		spec := migrate.SSHKeySpec{PublicKey: line}
		if pk, comment, _, _, perr := ssh.ParseAuthorizedKey([]byte(line)); perr == nil {
			spec.PublicKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk)))
			spec.Comment = comment
			spec.Fingerprint = ssh.FingerprintSHA256(pk)
		} else {
			// Malformed / placeholder key: keep the raw line, derive a stable
			// hash + best-effort comment (trailing field).
			sum := sha256.Sum256([]byte(line))
			spec.Fingerprint = "sha256:" + hex.EncodeToString(sum[:])
			if f := strings.Fields(line); len(f) >= 3 {
				spec.Comment = f[len(f)-1]
			}
		}
		if seen[spec.Fingerprint] {
			continue
		}
		seen[spec.Fingerprint] = true
		out = append(out, spec)
	}
	return out
}

// buildDomains returns one DomainSpec per site the account owns. CloudPanel
// stores each site's docroot as root_directory under /home/<user>/htdocs; the
// first site (alphabetical) is marked primary so the restore has a main domain.
// accountID is already siteUserRe-validated, so embedding it in a SQL string
// literal is safe and sqliteQuerySep shell-quotes the whole statement.
func (s *session) buildDomains(ctx context.Context, user string, warns *[]migrate.Warning) []migrate.DomainSpec {
	q := "SELECT s.domain_name, s.root_directory, s.type, coalesce(ps.php_version, '') " +
		"FROM site s LEFT JOIN php_settings ps ON ps.site_id = s.id " +
		"WHERE s.user = '" + user + "' ORDER BY s.domain_name"
	b, err := s.run(ctx, s.commandTimeout, sqliteQuerySep(s.dbPath, "|", q))
	if err != nil {
		*warns = append(*warns, migrate.Warning{Code: "cloudpanel_domains", Detail: err.Error()})
		return []migrate.DomainSpec{}
	}
	out := []migrate.DomainSpec{}
	first := true
	for _, line := range splitLines(b) {
		cols := strings.Split(line, "|")
		name := strings.TrimSpace(cols[0])
		if name == "" {
			continue
		}
		root := ""
		if len(cols) > 1 {
			root = strings.TrimSpace(cols[1])
		}
		docroot := "/home/" + user + "/htdocs/" + name
		if root != "" {
			docroot = "/home/" + user + "/htdocs/" + root
		}
		hasPHP := len(cols) > 2 && strings.TrimSpace(cols[2]) == "php"
		php := ""
		if len(cols) > 3 {
			php = strings.TrimSpace(cols[3])
		}
		out = append(out, migrate.DomainSpec{
			Name: name, DocRoot: docroot, IsPrimary: first, HasPHP: hasPHP, PHPVer: php,
		})
		first = false
	}
	return out
}

// buildDatabases returns the MySQL databases across all of the account's sites,
// with the CloudPanel database user as the grant user.
func (s *session) buildDatabases(ctx context.Context, user string, warns *[]migrate.Warning) []migrate.DatabaseSpec {
	q := "SELECT d.name, coalesce(du.user_name, '') FROM \"database\" d " +
		"JOIN site s ON d.site_id = s.id " +
		"LEFT JOIN database_user du ON du.database_id = d.id " +
		"WHERE s.user = '" + user + "' ORDER BY d.name"
	b, err := s.run(ctx, s.commandTimeout, sqliteQuerySep(s.dbPath, "|", q))
	if err != nil {
		*warns = append(*warns, migrate.Warning{Code: "cloudpanel_databases", Detail: err.Error()})
		return []migrate.DatabaseSpec{}
	}
	out := []migrate.DatabaseSpec{}
	for _, line := range splitLines(b) {
		cols := strings.Split(line, "|")
		name := strings.TrimSpace(cols[0])
		if name == "" {
			continue
		}
		spec := migrate.DatabaseSpec{Engine: "mysql", Name: name}
		if len(cols) > 1 {
			spec.GrantUser = strings.TrimSpace(cols[1])
		}
		out = append(out, spec)
	}
	return out
}

// splitLines splits sqlite output into non-empty, CR-trimmed lines.
func splitLines(b []byte) []string {
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

// Close releases the SSH client. Best-effort.
func (d *Discoverer) Close(ctx context.Context, raw migrate.Session) error {
	s, ok := raw.(*session)
	if !ok || s.client == nil {
		return nil
	}
	return s.client.Close()
}

// sshRun executes one command via SSH and returns stdout. Hard timeout via
// context; SIGKILL on expiry so a stuck command doesn't pin the session.
// Mirrors directadmin/discover.go's run().
func (s *session) sshRun(ctx context.Context, timeout time.Duration, cmd string) ([]byte, error) {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	subctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sess, err := s.client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("ssh new session: %w", err)
	}
	defer sess.Close()

	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()

	select {
	case err := <-done:
		if err != nil {
			return nil, fmt.Errorf("ssh exec %q: %w (stderr=%q)", cmd, err, stderr.String())
		}
		return stdout.Bytes(), nil
	case <-subctx.Done():
		_ = sess.Signal(ssh.SIGKILL)
		return nil, fmt.Errorf("ssh exec %q: timed out after %s", cmd, timeout)
	}
}

// sqliteQuery builds a read-only `sqlite3 <db> "<query>"` command. The db path
// is single-quoted for the shell; the query is a package-controlled literal
// (never operator input) so no escaping of it is required. `-readonly` opens
// the database without a write lock, guaranteeing we cannot mutate the source.
func sqliteQuery(dbPath, query string) string {
	return fmt.Sprintf("sqlite3 -readonly %s %s", shellSingleQuote(dbPath), shellSingleQuote(query))
}

// sqliteQuerySep is sqliteQuery with an explicit column separator.
func sqliteQuerySep(dbPath, sep, query string) string {
	return fmt.Sprintf("sqlite3 -readonly -separator %s %s %s",
		shellSingleQuote(sep), shellSingleQuote(dbPath), shellSingleQuote(query))
}

// shellSingleQuote wraps s in single quotes, escaping embedded single quotes,
// so it is safe as one shell argument.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// loadSecret matches the cpanel / directadmin loadSecret semantics: two
// recognised env-file lines, SSH_PASSWORD=<plaintext> and
// SSH_PRIVATE_KEY[_B64]=<PEM|base64-PEM>. Raw is zeroed before returning so the
// plaintext doesn't sit in the process heap.
func loadSecret(path string) ([]ssh.AuthMethod, error) {
	if path == "" {
		return nil, errors.New("secret path empty")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	defer zero(raw)

	var auths []ssh.AuthMethod
	for _, line := range strings.Split(string(raw), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "SSH_PASSWORD":
			auths = append(auths, ssh.Password(v))
		case "SSH_PRIVATE_KEY":
			signer, perr := ssh.ParsePrivateKey([]byte(v))
			if perr != nil {
				return nil, fmt.Errorf("parse SSH_PRIVATE_KEY: %w", perr)
			}
			auths = append(auths, ssh.PublicKeys(signer))
		case "SSH_PRIVATE_KEY_B64":
			pem, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
			if derr != nil {
				return nil, fmt.Errorf("base64-decode SSH_PRIVATE_KEY_B64: %w", derr)
			}
			signer, perr := ssh.ParsePrivateKey(pem)
			zero(pem)
			if perr != nil {
				return nil, fmt.Errorf("parse SSH_PRIVATE_KEY_B64: %w", perr)
			}
			auths = append(auths, ssh.PublicKeys(signer))
		}
	}
	if len(auths) == 0 {
		return nil, errors.New("no SSH_PASSWORD / SSH_PRIVATE_KEY / SSH_PRIVATE_KEY_B64 in secret file")
	}
	return auths, nil
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
