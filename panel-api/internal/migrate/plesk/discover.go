// Package plesk implements the GH #429 Discoverer for Plesk source
// panels. SSH-based connect (parity with the cpanel + directadmin
// packages); discovery via the `plesk bin` / `plesk ext wp-toolkit`
// admin CLI.
//
// Read-only contract: every command this package shells out to is a
// list / info / json-export variant (`--list`, `--info`,
// `-format json`). We never run a create / delete / modify call
// against the source panel. Restore-side mutations (mariadb-import,
// JMAP push, pdns/BIND zone upsert) run on the destination host, via
// the cpmove-tarball synthesis path the directadmin package uses.
//
// Step 1 (this commit) ships the Discoverer scaffold + Connect +
// ListAccounts + an empty-but-valid AccountManifest. Per-area builders
// (Domains / Databases / DNS / Cron), WordPress, mail, and the
// customers/packages layer land in follow-up steps — the same
// incremental shape the directadmin package took (see its Wave A
// scaffold). See plans/gh429-plesk-migration.md.
package plesk

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// Discoverer is the Plesk-side implementation of migrate.Discoverer.
type Discoverer struct {
	// AllowPrivate — when true the SSRF guard permits RFC1918 / ULA
	// targets (server_settings.migration_allow_private_hosts). Default false.
	AllowPrivate   bool
	Port           int
	CommandTimeout time.Duration
}

// New returns a Discoverer with sensible defaults. Plesk is managed
// over plain SSH (port 22); the credential model matches cpanel/DA's
// SecretRef env-file (SSH_PASSWORD or SSH_PRIVATE_KEY_B64). The
// principal must be root (or a sudo-capable admin) so `plesk bin`
// can enumerate every subscription.
func New() *Discoverer {
	return &Discoverer{
		Port:           22,
		CommandTimeout: 30 * time.Second,
	}
}

// Compile-time interface assertions.
var _ migrate.Discoverer = (*Discoverer)(nil)
var _ migrate.AllowPrivateSetter = (*Discoverer)(nil)

// SetAllowPrivate honours the server_settings private-host toggle.
func (d *Discoverer) SetAllowPrivate(b bool) { d.AllowPrivate = b }

// SetPort sets the source SSH port (GH #429); ApplyPort calls it via the
// PortSetter interface. Ports outside 1..65535 keep the default 22.
func (d *Discoverer) SetPort(p int) {
	if p >= 1 && p <= 65535 {
		d.Port = p
	}
}

type session struct {
	client        *ssh.Client
	connectedUser string
	// execFn lets tests substitute the SSH round-trip with a fixture
	// runner. nil in production → run() shells out over SSH.
	execFn func(ctx context.Context, timeout time.Duration, cmd string) ([]byte, error)
}

func (s *session) Kind() string { return models.MigrationSourcePlesk }

// Connect dials the source over SSH, pins the host key, and validates
// the principal can run the Plesk CLI via a cheap `plesk version`
// probe. A principal without `plesk` in PATH / without privileges
// fails here so the operator sees the auth problem early.
func (d *Discoverer) Connect(ctx context.Context, host, user string, secret migrate.SecretRef) (migrate.Session, error) {
	port := d.Port
	if port == 0 {
		port = 22
	}
	auth, err := loadSecret(secret.Path)
	if err != nil {
		return nil, fmt.Errorf("plesk.Connect: load secret: %w", err)
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
		return nil, fmt.Errorf("plesk.Connect: tcp dial %s: %w", addr, err)
	}
	c, ch, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		if strings.Contains(err.Error(), "unable to authenticate") || strings.Contains(err.Error(), "no supported methods remain") {
			return nil, fmt.Errorf("plesk.Connect: source SSH server rejected the supplied auth method (likely PasswordAuthentication=no — upload an SSH PRIVATE KEY in the wizard's Connection step instead): %w", err)
		}
		return nil, fmt.Errorf("plesk.Connect: ssh handshake: %w", err)
	}
	client := ssh.NewClient(c, ch, reqs)
	s := &session{client: client, connectedUser: user}

	// Admin probe: `plesk version` prints the Plesk build on success and
	// exits non-zero when the CLI is absent or the principal lacks
	// privileges. Either way a non-nil error is the gate.
	subctx, cancel := context.WithTimeout(ctx, d.CommandTimeout)
	defer cancel()
	if _, err := s.run(subctx, d.CommandTimeout, "plesk version"); err != nil {
		client.Close()
		return nil, fmt.Errorf("plesk.Connect: `plesk version` probe failed (need root or an admin SSH user on a Plesk host): %w", err)
	}
	return s, nil
}

// loadSecret mirrors the cpanel/directadmin loadSecret: two recognised
// env-file auth lines, SSH_PASSWORD=<plaintext> and
// SSH_PRIVATE_KEY_B64=<base64-PEM> (plus raw SSH_PRIVATE_KEY). The
// in-memory file bytes are zeroed before returning.
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
			auths = append(auths, migrate.SSHPasswordAuthMethods(v)...)
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

// run executes one command against the source, dispatching to the
// injected fixture runner in tests or SSH in production.
func (s *session) run(ctx context.Context, timeout time.Duration, cmd string) ([]byte, error) {
	if s.execFn != nil {
		return s.execFn(ctx, timeout, cmd)
	}
	return s.sshExec(ctx, timeout, cmd)
}

// sshExec runs one command via SSH and returns stdout. Hard timeout via
// context; SIGKILL on expiry so a stuck command doesn't pin the session.
func (s *session) sshExec(ctx context.Context, timeout time.Duration, cmd string) ([]byte, error) {
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

// ListAccounts returns one row per Plesk subscription visible to the
// admin principal, via `plesk bin subscription --list` (one
// subscription name — the primary domain — per line). Per-account
// sizes land on DescribeAccount, not here (cheap mode).
func (d *Discoverer) ListAccounts(ctx context.Context, raw migrate.Session) ([]migrate.AccountSummary, error) {
	s, ok := raw.(*session)
	if !ok {
		return nil, errors.New("ListAccounts: wrong session type")
	}
	out, err := s.run(ctx, d.CommandTimeout, "plesk bin subscription --list")
	if err != nil {
		return nil, fmt.Errorf("list Plesk subscriptions: %w", err)
	}
	return parsePleskList(string(out)), nil
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

// streamCommandToFile runs cmd on the source and streams its stdout to
// localPath WITHOUT buffering — essential for a multi-GB mysqldump (a real
// box carried a 6.9 GB DB; buffering it through run() would OOM the panel).
func (s *session) streamCommandToFile(ctx context.Context, timeout time.Duration, cmd, localPath string) error {
	subctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	sess, err := s.client.NewSession()
	if err != nil {
		return fmt.Errorf("ssh new session: %w", err)
	}
	defer sess.Close()
	f, err := os.OpenFile(localPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return fmt.Errorf("open local %s: %w", localPath, err)
	}
	defer f.Close()
	sess.Stdout = f
	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("ssh stream %q: %w", cmd, err)
		}
		return nil
	case <-subctx.Done():
		_ = sess.Signal(ssh.SIGKILL)
		return fmt.Errorf("ssh stream %q: timed out after %s", cmd, timeout)
	}
}
