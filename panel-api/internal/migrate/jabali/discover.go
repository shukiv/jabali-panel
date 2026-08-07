// Package jabali implements the Jabali→Jabali Discoverer (+ Restorer, later
// phases) for the migration engine (GH #954). The source is itself a Jabali
// install, so — unlike the cpanel/plesk/cyberpanel importers, which read a
// foreign panel's database — discovery talks to the source's OWN `jabali … list
// --json` CLI over SSH. That is authoritative (the CLI reads the source's live
// state) and version-tolerant (it adapts to the source's schema, so a minor
// version skew between the two boxes doesn't break enumeration).
//
// One source account = one NON-admin Jabali user, which maps 1:1 onto a
// destination Jabali user. Admins are skipped: they own no hosting home.
//
// SSH-based connect (parity with the sibling importers); the SSH user is root so
// the source `jabali` CLI can read the panel database. We only ever run READ
// subcommands (`version`, `user list`); no mutating command touches the source.
//
// Phase 1 (this file) ships the Discoverer scaffold + Connect + ListAccounts and
// returns a valid (SchemaVersion + Source) empty manifest that the Phase 2
// per-area builders fill — the same incremental shape the sibling importers took.
package jabali

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
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

// usernameRe is the strict allow-list a source Jabali username must match before
// it is ever interpolated into a later `jabali … --user <name>` shell argument.
// Lowercase Linux-username shape; anything outside is rejected, not escaped, so
// no shell/SQL metacharacter can reach the source (defence in depth — the value
// also comes from the source's own CLI, never operator free-text).
var usernameRe = regexp.MustCompile(`^[a-z_][a-z0-9._-]{0,63}$`)

// Discoverer is the Jabali-source implementation of migrate.Discoverer.
type Discoverer struct {
	// AllowPrivate — when true the SSRF guard permits RFC1918 / ULA targets.
	// Default false; the admin toggle (migration_allow_private_hosts) sets it
	// via migrate.ApplyAllowPrivate.
	AllowPrivate bool
	// Port is the source SSH port. Default 22.
	Port int
	// CommandTimeout bounds each remote command. Default 30s.
	CommandTimeout time.Duration
}

// New returns a Discoverer with sensible defaults.
func New() *Discoverer {
	return &Discoverer{Port: 22, CommandTimeout: 30 * time.Second}
}

// Compile-time interface assertions.
var (
	_ migrate.Discoverer         = (*Discoverer)(nil)
	_ migrate.AllowPrivateSetter = (*Discoverer)(nil)
	_ migrate.PortSetter         = (*Discoverer)(nil)
)

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
// sessions run over SSH (session.sshRun); tests inject a fake so the JSON-parsing
// logic in ListAccounts is exercised without a live host.
type runnerFunc func(ctx context.Context, timeout time.Duration, cmd string) ([]byte, error)

type session struct {
	client         *ssh.Client
	connectedUser  string
	commandTimeout time.Duration
	// run is the command executor. Connect wires it to sshRun; tests set it
	// directly on a hand-built session.
	run runnerFunc
}

func (s *session) Kind() string { return models.MigrationSourceJabali }

// jabaliVersion is the subset of `jabali version --json` we read to prove the
// source is a Jabali box and (later) to warn on a large version skew.
type jabaliVersion struct {
	RepoHead string `json:"repo_head"`
	Commit   string `json:"commit"`
	OS       string `json:"os"`
}

// jabaliUser is the subset of one `jabali user list --json` row we need.
type jabaliUser struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Username  string `json:"username"`
	IsAdmin   bool   `json:"is_admin"`
	Suspended bool   `json:"suspended"`
}

type jabaliUserList struct {
	Total int          `json:"total"`
	Users []jabaliUser `json:"users"`
}

// Connect dials the source over SSH, verifies the host key, and validates the
// connection with a single read: `jabali version --json`. A successful parse
// proves (a) the SSH creds work, (b) the `jabali` CLI is present, and (c) this
// really is a Jabali host — all before returning.
func (d *Discoverer) Connect(ctx context.Context, host, user string, secret migrate.SecretRef) (migrate.Session, error) {
	port := d.Port
	if port == 0 {
		port = 22
	}
	auth, err := loadSecret(secret.Path)
	if err != nil {
		return nil, fmt.Errorf("jabali.Connect: load secret: %w", err)
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
		return nil, fmt.Errorf("jabali.Connect: tcp dial %s: %w", addr, err)
	}
	c, ch, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		if strings.Contains(err.Error(), "unable to authenticate") || strings.Contains(err.Error(), "no supported methods remain") {
			return nil, fmt.Errorf("jabali.Connect: source SSH server rejected the credentials for user %q — check the password or key is correct for that user, that the user exists on the source, and note the server may have PasswordAuthentication=no (upload an SSH PRIVATE KEY in the wizard's Connection step instead): %w", user, err)
		}
		return nil, fmt.Errorf("jabali.Connect: ssh handshake: %w", err)
	}
	client := ssh.NewClient(c, ch, reqs)
	s := &session{client: client, connectedUser: user, commandTimeout: d.CommandTimeout}
	s.run = s.sshRun

	subctx, cancel := context.WithTimeout(ctx, d.CommandTimeout)
	defer cancel()
	if err := probeJabali(subctx, s, d.CommandTimeout); err != nil {
		client.Close()
		return nil, err
	}
	return s, nil
}

// probeJabali proves the source is a Jabali host with the CLI on PATH. It tries
// `jabali version --json` first, but falls back to plain `jabali version` when
// that fails — an OLDER source (exactly what people migrate FROM) may predate
// the global --json flag or print plain text, and rejecting it would make the
// version-tolerant CLI approach fail against the very hosts it exists to serve.
// Either a parseable JSON version or a plain "version:" line proves Jabali.
func probeJabali(ctx context.Context, s *session, timeout time.Duration) error {
	if out, err := s.run(ctx, timeout, "jabali version --json"); err == nil {
		var ver jabaliVersion
		if json.Unmarshal(out, &ver) == nil && (ver.RepoHead != "" || ver.Commit != "" || ver.OS != "") {
			return nil
		}
	}
	// Fallback: plain `jabali version` (the human-readable "version:  <sha>" form).
	out, err := s.run(ctx, timeout, "jabali version")
	if err != nil {
		return fmt.Errorf("jabali.Connect: read probe failed — is this a Jabali host with the `jabali` CLI on PATH for the SSH user (need root)? %w", err)
	}
	if !strings.Contains(strings.ToLower(string(out)), "version:") {
		return fmt.Errorf("jabali.Connect: `jabali version` did not look like a Jabali install (unexpected output); the source may not be a Jabali host")
	}
	return nil
}

// ListAccounts returns one row per NON-admin Jabali user on the source. Cheap
// mode — no per-account size lookup here; that lands on DescribeAccount /
// SizeProber (Phase 2). The command is static (no user input) so it needs no
// sanitisation.
func (d *Discoverer) ListAccounts(ctx context.Context, raw migrate.Session) ([]migrate.AccountSummary, error) {
	s, ok := raw.(*session)
	if !ok {
		return nil, errors.New("jabali.ListAccounts: wrong session type")
	}
	out, err := s.run(ctx, s.commandTimeout, "jabali user list --json")
	if err != nil {
		return nil, fmt.Errorf("jabali.ListAccounts: `jabali user list --json`: %w", err)
	}
	var list jabaliUserList
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("jabali.ListAccounts: parse user list: %w", err)
	}
	rows := []migrate.AccountSummary{}
	for _, u := range list.Users {
		// Admins own no hosting home; skip. A hosting user without a username
		// can't own a Linux account either, so it isn't a migratable account.
		if u.IsAdmin || strings.TrimSpace(u.Username) == "" {
			continue
		}
		rows = append(rows, migrate.AccountSummary{
			ID:        u.Username,
			Login:     u.Username,
			Email:     u.Email,
			Suspended: u.Suspended,
		})
	}
	return rows, nil
}

// DescribeAccount returns a manifest scaffold for one source Jabali user. The
// per-area builders (Domains / Databases / Mailboxes / DNS / Cron) ship in Phase
// 2. The account is validated against the Go-side account list (no user input
// reaches the source) plus a strict format check so later interpolating builders
// are safe.
func (d *Discoverer) DescribeAccount(ctx context.Context, raw migrate.Session, accountID string) (*migrate.AccountManifest, error) {
	s, ok := raw.(*session)
	if !ok {
		return nil, errors.New("jabali.DescribeAccount: wrong session type")
	}
	if accountID == "" {
		return nil, errors.New("jabali.DescribeAccount: accountID empty")
	}
	if !usernameRe.MatchString(accountID) {
		return nil, fmt.Errorf("jabali.DescribeAccount: invalid account id %q (must be a source Jabali username)", accountID)
	}

	accounts, err := d.ListAccounts(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("jabali.DescribeAccount: enumerate accounts: %w", err)
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
			return nil, fmt.Errorf("no non-admin Jabali users found on source; %q is not a hosting account", accountID)
		case 1:
			accountID = accounts[0].ID // single-tenant source — pivot to the only account
		default:
			names := make([]string, 0, len(accounts))
			for _, a := range accounts {
				names = append(names, a.ID)
			}
			return nil, fmt.Errorf("user %q is not a Jabali hosting account; pick one of: %s", accountID, strings.Join(names, ", "))
		}
	}

	host := ""
	if s.client != nil {
		host = s.client.RemoteAddr().String()
	}
	return &migrate.AccountManifest{
		SchemaVersion: migrate.ManifestSchemaVersion,
		Source: migrate.SourceRef{
			Kind: models.MigrationSourceJabali,
			Host: host,
			User: accountID,
		},
		Domains:   []migrate.DomainSpec{},
		Mailboxes: []migrate.MailboxSpec{},
		Databases: []migrate.DatabaseSpec{},
		Warnings:  []migrate.Warning{},
	}, nil
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

// loadSecret matches the sibling importers' loadSecret semantics:
// SSH_PASSWORD=<plaintext> or SSH_PRIVATE_KEY[_B64]=<PEM|base64-PEM>. Raw is
// zeroed before returning so the plaintext doesn't linger in the heap.
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
