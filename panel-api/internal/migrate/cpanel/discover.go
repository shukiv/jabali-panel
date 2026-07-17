package cpanel

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

// Discoverer is the cPanel-side implementation of migrate.Discoverer.
// One instance is reusable across multiple Connect calls (no per-
// instance state) but every Connect produces its own Session that
// owns the SSH client lifetime.
type Discoverer struct {
	// AllowPrivate — ADR-0095 decision 8. When true, SSRF
	// guard permits RFC1918 / ULA targets. Default false.
	AllowPrivate bool
	// Port is the SSH port on the source. 0 → 22 (cPanel default).
	Port int
	// CommandTimeout caps a single SSH exec. A pkgacct-style long
	// command runs through Restore, not Discoverer. 30s suffices
	// for every UAPI / whmapi1 call.
	CommandTimeout time.Duration
}

// New returns a Discoverer with sensible defaults.
func New() *Discoverer {
	return &Discoverer{
		Port:           22,
		CommandTimeout: 30 * time.Second,
	}
}

// Compile-time interface assertions.
var _ migrate.Discoverer = (*Discoverer)(nil)
var _ migrate.AllowPrivateSetter = (*Discoverer)(nil)

// SetAllowPrivate honors the migrate.AllowPrivateSetter capability —
// callers pull server_settings.migration_allow_private_hosts at request
// time + apply via migrate.ApplyAllowPrivate.
func (d *Discoverer) SetAllowPrivate(b bool) { d.AllowPrivate = b }

// SetPort sets the source SSH port (GH #429); ApplyPort calls it via the
// PortSetter interface. Ports outside 1..65535 keep the default 22.
func (d *Discoverer) SetPort(p int) {
	if p >= 1 && p <= 65535 {
		d.Port = p
	}
}

type session struct {
	client *ssh.Client
	// principal records whether the connected user is the cpanel
	// user (UAPI access only) or root / a wheel user (whmapi1
	// access — sees every account on the box).
	principal Principal
	// connectedUser is the source-side username we authenticated
	// as. For Principal=User this is the cPanel account login.
	connectedUser string
}

func (s *session) Kind() string { return models.MigrationSourceCpanel }

// Principal is the access tier of the connected SSH user.
type Principal int

const (
	// PrincipalUser is a single cPanel user — UAPI access only,
	// can only describe its own account.
	PrincipalUser Principal = iota
	// PrincipalAdmin is root or a wheel-group user — whmapi1
	// access, can list+describe every cPanel user on the box.
	PrincipalAdmin
)

// Connect dials the source via SSH, validates the credentials, and
// probes the principal tier. The SecretRef.Path is read once and
// zeroed before returning so the plaintext credential never lives
// past auth.
func (d *Discoverer) Connect(ctx context.Context, host, user string, secret migrate.SecretRef) (migrate.Session, error) {
	port := d.Port
	if port == 0 {
		port = 22
	}
	auth, err := loadSecret(secret.Path)
	if err != nil {
		return nil, fmt.Errorf("cpanel.Connect: load secret: %w", err)
	}
	// Verify the source host key (Gitea #461) instead of blindly trusting it.
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            auth,
		HostKeyCallback: migrate.PinningHostKeyCallback(secret.ExpectedHostKey),
		Timeout:         15 * time.Second,
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := migrate.DialTCP(ctx, host, port, d.AllowPrivate, 15*time.Second)
	if err != nil {
		return nil, fmt.Errorf("cpanel.Connect: tcp dial %s: %w", addr, err)
	}
	c, ch, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		if strings.Contains(err.Error(), "attempted methods [none]") {
			return nil, fmt.Errorf("cpanel.Connect: source SSH server rejected the supplied auth method (likely PasswordAuthentication=no — upload an SSH PRIVATE KEY in the wizard's Connection step instead): %w", err)
		}
		return nil, fmt.Errorf("cpanel.Connect: ssh handshake: %w", err)
	}
	client := ssh.NewClient(c, ch, reqs)
	s := &session{client: client, connectedUser: user}
	// Probe principal: try whmapi1 first; if it returns access
	// denied we're a single-user.
	if err := probePrincipal(ctx, d, s); err != nil {
		client.Close()
		return nil, fmt.Errorf("cpanel.Connect: principal probe: %w", err)
	}
	return s, nil
}

// loadSecret reads the per-job env file. Three recognised formats:
//
//	SSH_PASSWORD=<plaintext>
//	SSH_PRIVATE_KEY=<single-line-PEM>     (legacy; works only when
//	                                       the PEM body is one line)
//	SSH_PRIVATE_KEY_B64=<base64-of-PEM>   (preferred — survives
//	                                       multi-line OpenSSH PEM
//	                                       keys; written by the
//	                                       SPA's secrets-upload
//	                                       endpoint).
//
// loadSecret returns auth methods for whichever is present. After
// the file is read its in-memory copy is zeroed; subsequent
// reconnects re-read from disk (slow + intentional).
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
		return nil, errors.New("no SSH_PASSWORD or SSH_PRIVATE_KEY in secret file")
	}
	return auths, nil
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// probePrincipal sets s.principal. Tries whmapi1 first; falls back
// to UAPI on failure.
func probePrincipal(ctx context.Context, d *Discoverer, s *session) error {
	// Cheap whmapi1 call — version returns immediately on a real
	// WHM principal, errors with permission-denied otherwise.
	out, err := s.run(ctx, d.CommandTimeout, "whmapi1 --output=jsonpretty version")
	if err == nil && len(out) > 0 {
		// Did we actually get a JSON envelope back?
		if bytes.Contains(out, []byte(`"metadata"`)) {
			s.principal = PrincipalAdmin
			return nil
		}
	}
	// Try UAPI on the connected user.
	cmd := fmt.Sprintf("uapi --output=jsonpretty Variables get_user_information")
	out, err = s.run(ctx, d.CommandTimeout, cmd)
	if err != nil {
		return fmt.Errorf("uapi probe: %w", err)
	}
	var info userInformation
	if err := decodeUAPI(out, &info); err != nil {
		return fmt.Errorf("uapi probe decode: %w", err)
	}
	if info.User == "" {
		return errors.New("connected user is not a cPanel user (UAPI returned empty user)")
	}
	s.principal = PrincipalUser
	return nil
}

// run executes one cPanel CLI command via SSH and returns stdout.
// Surfaces stderr in the error message on non-zero exit.
func (s *session) run(ctx context.Context, timeout time.Duration, cmd string) ([]byte, error) {
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

// ListAccounts returns one row per cPanel account visible to the
// connected principal.
func (d *Discoverer) ListAccounts(ctx context.Context, raw migrate.Session) ([]migrate.AccountSummary, error) {
	s, ok := raw.(*session)
	if !ok {
		return nil, errors.New("ListAccounts: wrong session type")
	}
	switch s.principal {
	case PrincipalAdmin:
		return d.listAccountsAsAdmin(ctx, s)
	case PrincipalUser:
		return d.listAccountsAsUser(ctx, s)
	default:
		return nil, fmt.Errorf("unknown principal %d", s.principal)
	}
}

func (d *Discoverer) listAccountsAsAdmin(ctx context.Context, s *session) ([]migrate.AccountSummary, error) {
	out, err := s.run(ctx, d.CommandTimeout, "whmapi1 --output=jsonpretty listaccts")
	if err != nil {
		return nil, err
	}
	var data listAccts
	if err := decodeWHMAPI1(out, &data); err != nil {
		return nil, err
	}
	rows := make([]migrate.AccountSummary, 0, len(data.Acct))
	for _, a := range data.Acct {
		rows = append(rows, migrate.AccountSummary{
			ID:         a.User,
			Login:      a.User,
			Email:      a.Email,
			Domain:     a.Domain,
			BytesTotal: parseHumanBytes(a.DiskUsed),
			Suspended:  a.Suspended != 0,
		})
	}
	return rows, nil
}

func (d *Discoverer) listAccountsAsUser(ctx context.Context, s *session) ([]migrate.AccountSummary, error) {
	cmd := "uapi --output=jsonpretty Variables get_user_information"
	out, err := s.run(ctx, d.CommandTimeout, cmd)
	if err != nil {
		return nil, err
	}
	var info userInformation
	if err := decodeUAPI(out, &info); err != nil {
		return nil, err
	}
	return []migrate.AccountSummary{{
		ID:         info.User,
		Login:      info.User,
		Email:      info.Email,
		BytesTotal: info.BandwidthUsed,
	}}, nil
}

// parseHumanBytes converts cPanel's "1234M" / "12.5G" disk-used
// strings to a byte count. Unknown suffix → 0 (the operator sees
// the cell as 0 + can drill in).
func parseHumanBytes(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	mul := int64(1)
	switch s[len(s)-1] {
	case 'K', 'k':
		mul = 1024
		s = s[:len(s)-1]
	case 'M', 'm':
		mul = 1024 * 1024
		s = s[:len(s)-1]
	case 'G', 'g':
		mul = 1024 * 1024 * 1024
		s = s[:len(s)-1]
	case 'T', 't':
		mul = 1024 * 1024 * 1024 * 1024
		s = s[:len(s)-1]
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(f * float64(mul))
}

// Close releases the SSH client. Best-effort.
func (d *Discoverer) Close(ctx context.Context, raw migrate.Session) error {
	s, ok := raw.(*session)
	if !ok {
		return nil
	}
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}

// DescribeAccount produces the AccountManifest for one source-side
// cPanel account. Step 3 ships a manifest scaffold — domains +
// mailboxes + databases + cron + ssh-keys land in follow-up sub-
// steps as the per-area UAPI calls are wired. The returned
// manifest is always valid (SchemaVersion + Source set) so resume
// code never sees a half-built struct.
func (d *Discoverer) DescribeAccount(ctx context.Context, raw migrate.Session, accountID string) (*migrate.AccountManifest, error) {
	s, ok := raw.(*session)
	if !ok {
		return nil, errors.New("DescribeAccount: wrong session type")
	}
	if accountID == "" {
		return nil, errors.New("DescribeAccount: accountID empty")
	}
	host := ""
	if s.client != nil {
		host = s.client.RemoteAddr().String()
	}
	m := &migrate.AccountManifest{
		SchemaVersion: migrate.ManifestSchemaVersion,
		Source: migrate.SourceRef{
			Kind: models.MigrationSourceCpanel,
			Host: host,
			User: accountID,
		},
		Warnings: []migrate.Warning{},
	}

	// Per-area builders. We accumulate warnings rather than fail
	// the whole describe on any single missing module — a host
	// without Postgresql / SSH UAPI returns 'unknown function' on
	// those calls, which is recoverable. A real connectivity
	// failure (SSH dropped) bubbles back as the first error
	// returned.
	var firstErr error
	stash := func(err error, area string) {
		if err == nil {
			return
		}
		m.Warnings = append(m.Warnings, migrate.Warning{
			Code:   area + "_failed",
			Detail: err.Error(),
			At:     time.Now().UTC(),
		})
		if firstErr == nil {
			firstErr = err
		}
	}

	if doms, err := d.describeDomains(ctx, s, accountID); err != nil {
		stash(err, "domains")
	} else {
		m.Domains = doms
	}
	if boxes, err := d.describeMailboxes(ctx, s, accountID); err != nil {
		stash(err, "mailboxes")
	} else {
		m.Mailboxes = boxes
	}
	if dbs, warn, err := d.describeDatabases(ctx, s, accountID); err != nil {
		stash(err, "databases")
	} else {
		m.Databases = dbs
		m.Warnings = append(m.Warnings, warn...)
	}
	if crons, err := d.describeCron(ctx, s, accountID); err != nil {
		stash(err, "cron")
	} else {
		m.Cron = crons
	}
	if keys, err := d.describeSSHKeys(ctx, s, accountID); err != nil {
		stash(err, "ssh")
	} else {
		m.SSH = keys
	}
	// Apps detection (WordPress / Joomla / Drupal walk of public_html)
	// lands when restore-stage tarball parsing arrives — that path
	// has the file tree locally + can do strong version-string
	// matching without round-tripping over SSH.

	// Domains is the load-bearing area; if it failed return the
	// error so the operator sees a hard fail rather than a
	// half-empty manifest. Other areas are advisory.
	if firstErr != nil && len(m.Domains) == 0 {
		return nil, firstErr
	}
	return m, nil
}

// AccountSize implements migrate.SizeProber. Runs `du -sb /home/<user>`
// over the existing SSH session and returns the byte count. Single
// round-trip; expected wall time ~5–30s depending on home size.
//
// Sanitizes the login as a defence in depth — listAccounts upstream
// already returns valid cPanel usernames, but a defensive regex stops
// any callsite from injecting shell metachars.
func (d *Discoverer) AccountSize(ctx context.Context, raw migrate.Session, login string) (int64, error) {
	s, ok := raw.(*session)
	if !ok {
		return 0, errors.New("AccountSize: wrong session type")
	}
	if !cpanelUserRe.MatchString(login) {
		return 0, fmt.Errorf("AccountSize: invalid login %q", login)
	}
	// du -sb prints "<bytes>\t<path>" on stdout.
	out, err := s.run(ctx, d.CommandTimeout, fmt.Sprintf("du -sb /home/%s 2>/dev/null", login))
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return 0, errors.New("AccountSize: empty du output")
	}
	n, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("AccountSize: parse %q: %w", fields[0], err)
	}
	return n, nil
}

// cpanelUserRe matches cPanel-shape usernames: 1-16 chars, letters/
// digits/underscore, must start with a letter. Defence in depth
// against shell injection through the du command above.
var cpanelUserRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,15}$`)
