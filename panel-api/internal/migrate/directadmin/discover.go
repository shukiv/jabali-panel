// Package directadmin implements the M35 Discoverer + Restorer for
// DirectAdmin source panels. SSH-based connect (parity with the
// cpanel package); discovery via the `da` admin CLI.
//
// Read-only contract: every command this package shells out to is a
// list / show / json-export variant. We never run an `add`, `create`,
// or `delete` admin call against the source. Restore-side mutations
// (mariadb-import, JMAP push, BIND-zone upsert) all run on the
// destination panel host, which is what migrate package's other
// importers do as well.
//
// Wave A Step 4 ships the Discoverer scaffold + Connect + ListAccounts.
// DescribeAccount returns a valid (SchemaVersion + Source) manifest
// shell that downstream area builders fill — same shape the cpanel
// package took before its Step 3-area-builders commit (27edeb71).
// Per-area builders (DomainSpec / DatabaseSpec / etc.) ship in
// follow-up commits as the DA-side `da admin user.show` JSON shape
// is documented + tested against a live DA fixture.
package directadmin

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

// Discoverer is the DA-side implementation of migrate.Discoverer.
type Discoverer struct {
	// AllowPrivate — ADR-0095 decision 8. When true, SSRF
	// guard permits RFC1918 / ULA targets. Default false.
	AllowPrivate bool
	Port           int
	CommandTimeout time.Duration
}

// New returns a Discoverer with sensible defaults. DA SSH port is
// usually plain 22; the panel HTTP API at 2222 is NOT what we
// connect to here — we go through SSH so credential model matches
// cpanel's SecretRef (env-file with SSH_PASSWORD or SSH_PRIVATE_KEY).
func New() *Discoverer {
	return &Discoverer{
		Port:           22,
		CommandTimeout: 30 * time.Second,
	}
}

// Compile-time interface assertions.
var _ migrate.Discoverer = (*Discoverer)(nil)
var _ migrate.AllowPrivateSetter = (*Discoverer)(nil)

// SetAllowPrivate — see cpanel/discover.go for rationale.
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
	// connectedUser is the SSH-side principal — must be `admin` (or
	// have the `da` admin CLI in PATH with admin privileges) to run
	// `da admin user.list`. Single-user DA accounts can't enumerate;
	// this importer is admin-only by design.
	connectedUser string
}

func (s *session) Kind() string { return models.MigrationSourceDirectAdmin }

// Connect dials the source via SSH + validates the credentials by
// running a cheap admin probe (`da admin info`). Refuses non-admin
// principals — DA's per-account principals can't enumerate, so a
// migration that needs ListAccounts can only run as admin.
func (d *Discoverer) Connect(ctx context.Context, host, user string, secret migrate.SecretRef) (migrate.Session, error) {
	port := d.Port
	if port == 0 {
		port = 22
	}
	auth, err := loadSecret(secret.Path)
	if err != nil {
		return nil, fmt.Errorf("directadmin.Connect: load secret: %w", err)
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
		return nil, fmt.Errorf("directadmin.Connect: tcp dial %s: %w", addr, err)
	}
	c, ch, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		// Go ssh client filters our offered methods against the
		// server's advertised list. "attempted methods [none]"
		// means we offered something (e.g. password) but the
		// server didn't accept that method — usually source has
		// PasswordAuthentication=no. Surface a concrete hint.
		if strings.Contains(err.Error(), "unable to authenticate") || strings.Contains(err.Error(), "no supported methods remain") {
			return nil, fmt.Errorf("directadmin.Connect: source SSH server rejected the supplied auth method (likely PasswordAuthentication=no — upload an SSH PRIVATE KEY in the wizard's Connection step instead): %w", err)
		}
		return nil, fmt.Errorf("directadmin.Connect: ssh handshake: %w", err)
	}
	client := ssh.NewClient(c, ch, reqs)
	s := &session{client: client, connectedUser: user}

	// Admin probe — refuse non-admin principals up front. `da admin
	// info` returns `Status: OK` on success; non-admin SSH user
	// running it gets a permission-denied / 'not allowed' style
	// error. Either way the run() returning non-nil is the gate.
	subctx, cancel := context.WithTimeout(ctx, d.CommandTimeout)
	defer cancel()
	// `da admin` prints the admin username on success; non-admin
	// SSH user exits 1 ("admin: privileges required"). Non-empty
	// stdout = root or admin. Earlier code used `da admin info`
	// which doesn't exist on real DA installs (the binary errors
	// "Unrecognized arguments [info]" — DA's `admin` subcommand
	// takes no args).
	if _, err := s.run(subctx, d.CommandTimeout, "da admin"); err != nil {
		client.Close()
		return nil, fmt.Errorf("directadmin.Connect: admin probe failed (need root or admin SSH user): %w", err)
	}
	return s, nil
}

// loadSecret matches the cpanel package's loadSecret semantics.
// Two recognised env-file lines: SSH_PASSWORD=<plaintext> and
// SSH_PRIVATE_KEY=<base64-PEM>. In-memory raw is zeroed before
// returning so the plaintext doesn't sit in process heap.
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
			// Preferred key format — agent's secrets_write writes
			// the operator-uploaded PEM as a single-line base64
			// blob so multi-line OpenSSH PEMs survive the env-file
			// round-trip. Mirrors cpanel/discover.go loadSecret.
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

// run executes one DA CLI command via SSH and returns stdout.
// Hard timeout via context; SIGKILL on expiry so a stuck command
// doesn't pin the SSH session.
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

// ListAccounts returns one row per DA account visible to the admin
// principal. Uses `da admin user.list` which prints one username per
// line. We don't pull per-account size info here (cheap mode) —
// that lands on DescribeAccount.
func (d *Discoverer) ListAccounts(ctx context.Context, raw migrate.Session) ([]migrate.AccountSummary, error) {
	s, ok := raw.(*session)
	if !ok {
		return nil, errors.New("ListAccounts: wrong session type")
	}
	// DA doesn't ship a `da admin user.list` CLI — earlier code
	// errored on every live source. Real path: enumerate
	// /usr/local/directadmin/data/users/ (one dir per hosting
	// user). The admin "user" itself shows up there + we filter
	// it below.
	out, err := s.run(ctx, d.CommandTimeout, "ls -1 /usr/local/directadmin/data/users/ 2>/dev/null")
	if err != nil {
		return nil, fmt.Errorf("list DA users: %w", err)
	}
	rows := []migrate.AccountSummary{}
	for _, line := range strings.Split(string(out), "\n") {
		login := strings.TrimSpace(line)
		if login == "" || strings.HasPrefix(login, "#") {
			continue
		}
		// Filter the special 'admin' login — that's the principal
		// running the migration, not a transferable hosting user.
		if login == "admin" {
			continue
		}
		rows = append(rows, migrate.AccountSummary{
			ID:    login,
			Login: login,
		})
	}
	return rows, nil
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

// DescribeAccount returns a manifest scaffold for one DA account.
// Per-area builders (domains via `da admin user.show`, databases via
// `da admin db.list`, mailboxes via `da admin pop.list`) ship as
// follow-up commits — same incremental shape the cpanel package
// took. Step 4 ships the Discoverer + ListAccounts so dispatcher
// plumbing (registry, source-kind switch in migrate import CLI)
// can wire DA without waiting for the area builders.
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
	// Validate the account exists on the source by stat'ing the
	// DA-side user.conf file. `da admin user.show` doesn't exist as
	// a CLI verb (only `da admin` for self-identification) so the
	// previous shellout always failed with "Unrecognized arguments".
	//
	// When the operator typed an SSH-side principal (`root`, `admin`)
	// rather than an actual hosting account, fall back to a single-
	// account auto-pick so the common single-tenant case Just Works.
	// Multi-account sources error with the visible list so the
	// operator can re-run the wizard with the right ID.
	probeCmd := fmt.Sprintf("test -f /usr/local/directadmin/data/users/'%s'/user.conf",
		strings.ReplaceAll(accountID, "'", `'\''`))
	if _, err := s.run(ctx, d.CommandTimeout, probeCmd); err != nil {
		// accountID not a hosting user. Auto-detect.
		accounts, listErr := d.ListAccounts(ctx, s)
		if listErr != nil {
			return nil, fmt.Errorf("user %q not found on source (no /usr/local/directadmin/data/users/%s/user.conf) and auto-detect failed: %w",
				accountID, accountID, listErr)
		}
		switch len(accounts) {
		case 0:
			return nil, fmt.Errorf("no DA hosting accounts found on source (looked in /usr/local/directadmin/data/users/); supplied %q is not a hosting user", accountID)
		case 1:
			// Single-tenant DA — silently pivot to the only account.
			accountID = accounts[0].ID
		default:
			names := make([]string, 0, len(accounts))
			for _, a := range accounts {
				names = append(names, a.ID)
			}
			return nil, fmt.Errorf("user %q is not a DA hosting account; pick one of: %s",
				accountID, strings.Join(names, ", "))
		}
	}
	m := &migrate.AccountManifest{
		SchemaVersion: migrate.ManifestSchemaVersion,
		Source: migrate.SourceRef{
			Kind: models.MigrationSourceDirectAdmin,
			Host: host,
			User: accountID,
		},
		Warnings: []migrate.Warning{},
	}

	// Per-area builders. Best-effort: each area's failure stashes a
	// warning + sets firstErr but does NOT short-circuit. Domains is
	// load-bearing — if it fails AND zero domains parsed, we return
	// the error so the operator sees a hard fail.
	var firstErr error

	if doms, err := d.describeDomains(ctx, s, accountID); err != nil {
		firstErr = fmt.Errorf("domains: %w", err)
		m.Warnings = append(m.Warnings, migrate.Warning{
			Code: "domains_failed", Detail: err.Error(),
		})
	} else {
		m.Domains = doms
	}

	if dbs, warn, err := d.describeDatabases(ctx, s, accountID); err != nil {
		if firstErr == nil {
			firstErr = fmt.Errorf("databases: %w", err)
		}
		m.Warnings = append(m.Warnings, migrate.Warning{
			Code: "databases_failed", Detail: err.Error(),
		})
	} else {
		m.Databases = dbs
		m.Warnings = append(m.Warnings, warn...)
	}

	if boxes, warn, err := d.describeMailboxes(ctx, s, accountID, m.Domains); err != nil {
		m.Warnings = append(m.Warnings, migrate.Warning{
			Code: "mailboxes_failed", Detail: err.Error(),
		})
		_ = warn
	} else {
		m.Mailboxes = boxes
		m.Warnings = append(m.Warnings, warn...)
	}

	// Cron + SSH keys not exposed via 'da admin' CLI on DA. Land
	// when the DA tarball parser ships in a follow-up — the cpanel
	// writers (cron + ssh-keys) work against any extracted layout
	// that has cron/<user> + .ssh/authorized_keys.
	m.Warnings = append(m.Warnings, migrate.Warning{
		Code:   "directadmin_cron_ssh_via_tarball",
		Detail: "DA admin CLI doesn't expose cron + ssh keys; pull v-backup-user-style tarball + reuse cpanel writers (Step 4 follow-up).",
	})

	if firstErr != nil && len(m.Domains) == 0 {
		return nil, firstErr
	}
	return m, nil
}
