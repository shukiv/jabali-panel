// Package cyberpanel implements the CyberPanel Discoverer (+ Restorer, later
// steps) for the jabali migration engine. SSH-based connect (parity with the
// cpanel / directadmin / cloudpanel packages); discovery reads CyberPanel's
// MySQL config database directly.
//
// CyberPanel keeps its whole inventory in the `cyberpanel` MySQL database:
//
//	websiteFunctions_websites   one row per site — domain, externalApp (the
//	                            Linux user), adminEmail, admin_id, package_id;
//	                            the site home lives at /home/<domain>/
//	databases_databases         (dbName, dbUser, website_id) MySQL databases
//	e_users                     mailboxes (email, DiskUsage, emailOwner_id)
//	e_forwardings               (source, destination) mail forwarders
//	e_domains                   maps a mail domain to its website
//	websiteFunctions_childdomains  child / addon domains
//	packages_package            hosting packages
//
// Enumeration is plain read-only SELECTs via the `mysql` CLI over SSH (the SSH
// user is root; CyberPanel's mysqld grants root passwordless local access). We
// NEVER run a mutating statement against the source.
//
// Unlike CloudPanel, CyberPanel HAS a mail server, so mailboxes + forwarders
// are part of the manifest (filled by the Step 2 per-area builders).
//
// Step 1 (this file) ships the Discoverer scaffold + Connect + ListAccounts and
// returns a valid (SchemaVersion + Source) empty manifest that the Step 2
// per-area builders fill — the same incremental shape cloudpanel/directadmin
// took.
package cyberpanel

import (
	"bytes"
	"context"
	"encoding/base64"
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

// DefaultDBName is the CyberPanel MySQL config database.
const DefaultDBName = "cyberpanel"

// domainRe is the strict allow-list a CyberPanel account id (a website domain)
// must match before it is ever interpolated into a SQL or shell string.
// Lowercase host-name characters only; anything outside is rejected rather than
// escaped, so no SQL/shell metacharacter can reach the source (defence in depth
// — DescribeAccount also validates against the Go-side account list).
var domainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,253})$`)

// phpVerRe pulls the bare X.Y version out of CyberPanel's "PHP 8.1" strings.
var phpVerRe = regexp.MustCompile(`[0-9]+\.[0-9]+`)

// Discoverer is the CyberPanel-side implementation of migrate.Discoverer.
type Discoverer struct {
	// AllowPrivate — when true the SSRF guard permits RFC1918 / ULA targets.
	// Default false; the admin toggle (migration_allow_private_hosts) sets it
	// via migrate.ApplyAllowPrivate.
	AllowPrivate bool
	// Port is the source SSH port. Default 22.
	Port int
	// CommandTimeout bounds each remote command. Default 30s.
	CommandTimeout time.Duration
	// DBName is the CyberPanel MySQL database name. Overridable only for tests;
	// production always uses DefaultDBName.
	DBName string
}

// New returns a Discoverer with sensible defaults.
func New() *Discoverer {
	return &Discoverer{
		Port:           22,
		CommandTimeout: 30 * time.Second,
		DBName:         DefaultDBName,
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
// sessions run over SSH (session.sshRun); tests inject a fake so the SQL-parsing
// logic in ListAccounts / DescribeAccount is exercised without a live host.
type runnerFunc func(ctx context.Context, timeout time.Duration, cmd string) ([]byte, error)

type session struct {
	client         *ssh.Client
	connectedUser  string
	dbName         string
	commandTimeout time.Duration
	// run is the command executor. Connect wires it to sshRun; tests set it
	// directly on a hand-built session.
	run runnerFunc
}

func (s *session) Kind() string { return models.MigrationSourceCyberPanel }

// Connect dials the source over SSH, verifies the host key, and validates the
// credentials with a single read: counting rows in websiteFunctions_websites.
// A successful count proves (a) the SSH creds work, (b) `mysql` is present and
// grants access, and (c) the database is really CyberPanel's — all before
// returning.
func (d *Discoverer) Connect(ctx context.Context, host, user string, secret migrate.SecretRef) (migrate.Session, error) {
	port := d.Port
	if port == 0 {
		port = 22
	}
	auth, err := loadSecret(secret.Path)
	if err != nil {
		return nil, fmt.Errorf("cyberpanel.Connect: load secret: %w", err)
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
		return nil, fmt.Errorf("cyberpanel.Connect: tcp dial %s: %w", addr, err)
	}
	c, ch, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		if strings.Contains(err.Error(), "unable to authenticate") || strings.Contains(err.Error(), "no supported methods remain") {
			return nil, fmt.Errorf("cyberpanel.Connect: source SSH server rejected the supplied auth method (likely PasswordAuthentication=no — upload an SSH PRIVATE KEY in the wizard's Connection step instead): %w", err)
		}
		return nil, fmt.Errorf("cyberpanel.Connect: ssh handshake: %w", err)
	}
	client := ssh.NewClient(c, ch, reqs)
	dbName := d.DBName
	if dbName == "" {
		dbName = DefaultDBName
	}
	s := &session{
		client:         client,
		connectedUser:  user,
		dbName:         dbName,
		commandTimeout: d.CommandTimeout,
	}
	s.run = s.sshRun

	subctx, cancel := context.WithTimeout(ctx, d.CommandTimeout)
	defer cancel()
	if _, err := s.run(subctx, d.CommandTimeout, mysqlQuery(dbName, "SELECT count(*) FROM websiteFunctions_websites")); err != nil {
		client.Close()
		return nil, fmt.Errorf("cyberpanel.Connect: read probe failed — is this a CyberPanel host with the %q database readable by the SSH user (need root)? %w", dbName, err)
	}
	return s, nil
}

// ListAccounts returns one row per CyberPanel WEBSITE (domain). Each website is
// a single Linux user (externalApp) with its home at /home/<domain>. Cheap mode
// — no per-account size lookup here; that lands on DescribeAccount / SizeProber
// (Step 2). The query is static (no user input) so it needs no sanitisation.
func (d *Discoverer) ListAccounts(ctx context.Context, raw migrate.Session) ([]migrate.AccountSummary, error) {
	s, ok := raw.(*session)
	if !ok {
		return nil, errors.New("cyberpanel.ListAccounts: wrong session type")
	}
	// domain \t externalApp — one row per website. externalApp is the Linux
	// user that owns /home/<domain>.
	q := "SELECT domain, externalApp FROM websiteFunctions_websites " +
		"WHERE domain IS NOT NULL AND domain != '' ORDER BY domain"
	out, err := s.run(ctx, s.commandTimeout, mysqlQuery(s.dbName, q))
	if err != nil {
		return nil, fmt.Errorf("cyberpanel.ListAccounts: read websites: %w", err)
	}
	rows := []migrate.AccountSummary{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		domain, extApp, _ := strings.Cut(line, "\t")
		domain = strings.TrimSpace(domain)
		if domain == "" {
			continue
		}
		rows = append(rows, migrate.AccountSummary{
			ID:     domain,
			Login:  strings.TrimSpace(extApp),
			Domain: domain,
		})
	}
	return rows, nil
}

// DescribeAccount returns a manifest scaffold for one CyberPanel website. The
// per-area builders (Domains / Databases / Mailboxes / Forwarders / Cron / SSH)
// ship in Step 2. The account is validated against the Go-side account list (no
// user input reaches SQL) plus a strict format check so later interpolating
// builders are safe.
func (d *Discoverer) DescribeAccount(ctx context.Context, raw migrate.Session, accountID string) (*migrate.AccountManifest, error) {
	s, ok := raw.(*session)
	if !ok {
		return nil, errors.New("cyberpanel.DescribeAccount: wrong session type")
	}
	if accountID == "" {
		return nil, errors.New("cyberpanel.DescribeAccount: accountID empty")
	}
	if !domainRe.MatchString(accountID) {
		return nil, fmt.Errorf("cyberpanel.DescribeAccount: invalid account id %q (must be a CyberPanel website domain)", accountID)
	}

	accounts, err := d.ListAccounts(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("cyberpanel.DescribeAccount: enumerate accounts: %w", err)
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
			return nil, fmt.Errorf("no CyberPanel websites found on source (queried %q); %q is not a hosting account", s.dbName, accountID)
		case 1:
			// Single-tenant source — pivot to the only account (matches the
			// directadmin/cloudpanel auto-detect convenience).
			accountID = accounts[0].ID
		default:
			names := make([]string, 0, len(accounts))
			for _, a := range accounts {
				names = append(names, a.ID)
			}
			return nil, fmt.Errorf("domain %q is not a CyberPanel website; pick one of: %s", accountID, strings.Join(names, ", "))
		}
	}

	host := ""
	if s.client != nil {
		host = s.client.RemoteAddr().String()
	}
	m := &migrate.AccountManifest{
		SchemaVersion: migrate.ManifestSchemaVersion,
		Source: migrate.SourceRef{
			Kind: models.MigrationSourceCyberPanel,
			Host: host,
			User: accountID,
		},
		Domains:   []migrate.DomainSpec{},
		Mailboxes: []migrate.MailboxSpec{},
		Databases: []migrate.DatabaseSpec{},
		Warnings:  []migrate.Warning{},
	}

	// Resolve the website's numeric id + PHP version once; the per-area builders
	// key their joins off the id (an int from the DB, injection-safe). accountID
	// is already domainRe-validated, so embedding it in a SQL string literal is
	// safe (no quote/metachar can occur), and mysqlQuery shell-quotes the whole
	// statement on top.
	wid, php, werr := s.websiteInfo(ctx, accountID)
	if werr != nil {
		return nil, fmt.Errorf("cyberpanel.DescribeAccount: resolve website %q: %w", accountID, werr)
	}
	m.Domains = s.buildDomains(ctx, accountID, wid, php, &m.Warnings)
	m.Databases = s.buildDatabases(ctx, wid, &m.Warnings)
	m.Mailboxes = s.buildMailboxes(ctx, wid, &m.Warnings)
	return m, nil
}

// websiteInfo returns the numeric website id + parsed PHP version for a domain.
func (s *session) websiteInfo(ctx context.Context, domain string) (int, string, error) {
	q := "SELECT id, phpSelection FROM websiteFunctions_websites WHERE domain='" + domain + "'"
	out, err := s.run(ctx, s.commandTimeout, mysqlQuery(s.dbName, q))
	if err != nil {
		return 0, "", err
	}
	line := firstLine(out)
	if line == "" {
		return 0, "", fmt.Errorf("website %q not found", domain)
	}
	cols := strings.Split(line, "	")
	id, aerr := strconv.Atoi(strings.TrimSpace(cols[0]))
	if aerr != nil {
		return 0, "", fmt.Errorf("bad website id %q: %w", cols[0], aerr)
	}
	php := ""
	if len(cols) > 1 {
		php = parsePHP(cols[1])
	}
	return id, php, nil
}

// buildDomains returns the primary website domain plus its child + alias
// domains. DocRoot follows CyberPanel's /home/<domain>/public_html layout.
func (s *session) buildDomains(ctx context.Context, domain string, wid int, php string, warns *[]migrate.Warning) []migrate.DomainSpec {
	out := []migrate.DomainSpec{{
		Name:      domain,
		DocRoot:   "/home/" + domain + "/public_html",
		IsPrimary: true,
		HasPHP:    true,
		PHPVer:    php,
	}}
	cq := fmt.Sprintf("SELECT domain, path, phpSelection FROM websiteFunctions_childdomains WHERE master_id=%d", wid)
	if b, err := s.run(ctx, s.commandTimeout, mysqlQuery(s.dbName, cq)); err == nil {
		for _, line := range splitLines(b) {
			cols := strings.Split(line, "	")
			name := strings.TrimSpace(cols[0])
			if name == "" {
				continue
			}
			cd := migrate.DomainSpec{Name: name, IsPrimary: false, HasPHP: true}
			if len(cols) > 1 && strings.TrimSpace(cols[1]) != "" {
				cd.DocRoot = strings.TrimSpace(cols[1])
			} else {
				cd.DocRoot = "/home/" + domain + "/public_html"
			}
			if len(cols) > 2 {
				cd.PHPVer = parsePHP(cols[2])
			}
			out = append(out, cd)
		}
	} else {
		*warns = append(*warns, migrate.Warning{Code: "cyberpanel_childdomains", Detail: err.Error()})
	}
	aq := fmt.Sprintf("SELECT aliasDomain FROM websiteFunctions_aliasdomains WHERE master_id=%d", wid)
	if b, err := s.run(ctx, s.commandTimeout, mysqlQuery(s.dbName, aq)); err == nil {
		for _, line := range splitLines(b) {
			name := strings.TrimSpace(line)
			if name == "" {
				continue
			}
			out = append(out, migrate.DomainSpec{
				Name: name, DocRoot: "/home/" + domain + "/public_html",
				IsPrimary: false, HasPHP: true, PHPVer: php,
			})
		}
	} else {
		*warns = append(*warns, migrate.Warning{Code: "cyberpanel_aliasdomains", Detail: err.Error()})
	}
	return out
}

// buildDatabases returns the MySQL databases attached to the website.
func (s *session) buildDatabases(ctx context.Context, wid int, warns *[]migrate.Warning) []migrate.DatabaseSpec {
	q := fmt.Sprintf("SELECT dbName, dbUser FROM databases_databases WHERE website_id=%d", wid)
	b, err := s.run(ctx, s.commandTimeout, mysqlQuery(s.dbName, q))
	if err != nil {
		*warns = append(*warns, migrate.Warning{Code: "cyberpanel_databases", Detail: err.Error()})
		return []migrate.DatabaseSpec{}
	}
	out := []migrate.DatabaseSpec{}
	for _, line := range splitLines(b) {
		cols := strings.Split(line, "	")
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

// buildMailboxes returns the mailboxes on the website's mail domains
// (e_users joined to e_domains, which owns the domain). CyberPanel stores mail
// under /home/vmail/<domain>/<local>/. DiskUsage is CyberPanel's MB figure.
func (s *session) buildMailboxes(ctx context.Context, wid int, warns *[]migrate.Warning) []migrate.MailboxSpec {
	q := fmt.Sprintf("SELECT u.email, u.DiskUsage FROM e_users u JOIN e_domains d ON u.emailOwner_id=d.domain WHERE d.domainOwner_id=%d", wid)
	b, err := s.run(ctx, s.commandTimeout, mysqlQuery(s.dbName, q))
	if err != nil {
		*warns = append(*warns, migrate.Warning{Code: "cyberpanel_mailboxes", Detail: err.Error()})
		return []migrate.MailboxSpec{}
	}
	out := []migrate.MailboxSpec{}
	for _, line := range splitLines(b) {
		cols := strings.Split(line, "	")
		email := strings.TrimSpace(cols[0])
		if email == "" {
			continue
		}
		local, mdom, _ := strings.Cut(email, "@")
		mb := migrate.MailboxSpec{
			Address:     email,
			MaildirPath: "/home/vmail/" + mdom + "/" + local + "/",
		}
		if len(cols) > 1 {
			if mbUsed, aerr := strconv.Atoi(strings.TrimSpace(cols[1])); aerr == nil && mbUsed > 0 {
				mb.BytesUsed = int64(mbUsed) * 1024 * 1024
			}
		}
		out = append(out, mb)
	}
	return out
}

// parsePHP extracts a bare "8.1" from CyberPanel's phpSelection ("PHP 8.1").
func parsePHP(s string) string {
	if m := phpVerRe.FindString(s); m != "" {
		return m
	}
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "PHP"))
}

// firstLine returns the first non-empty line of b, trimmed.
func firstLine(b []byte) string {
	for _, line := range splitLines(b) {
		return line
	}
	return ""
}

// splitLines splits mysql output into non-empty, CR-trimmed lines.
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

// mysqlQuery builds a read-only `mysql -N -B <db> -e "<query>"` command. -N
// drops the column-name header and -B (batch) makes the output TAB-separated;
// the db name is single-quoted for the shell and the query is a package-
// controlled literal (never operator input) so no escaping of it is required.
// We only ever issue SELECTs, so the source is never mutated.
func mysqlQuery(db, query string) string {
	return fmt.Sprintf("mysql -N -B %s -e %s", shellSingleQuote(db), shellSingleQuote(query))
}

// shellSingleQuote wraps s in single quotes, escaping embedded single quotes,
// so it is safe as one shell argument.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// loadSecret matches the cpanel / directadmin / cloudpanel loadSecret
// semantics: SSH_PASSWORD=<plaintext> or SSH_PRIVATE_KEY[_B64]=<PEM|base64-PEM>.
// Raw is zeroed before returning so the plaintext doesn't linger in the heap.
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
