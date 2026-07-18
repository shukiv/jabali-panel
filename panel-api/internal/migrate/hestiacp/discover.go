// Package hestiacp implements the M35 Discoverer + Restorer for
// HestiaCP source panels. SSH-based connect (parity with cpanel +
// directadmin); discovery via the v-list-* admin scripts.
//
// Read-only contract: every command this package shells out to is
// a v-list-* / v-show-* variant. Restore-side mutations
// (mariadb-import, JMAP push, BIND-zone upsert) all run on the
// destination panel host, same as cpanel + DA.
//
// Wave B Step 5 ships the Discoverer scaffold + Connect +
// ListAccounts. DescribeAccount returns a manifest shell with one
// warning marking the per-area-builders gap. v-list-user JSON
// output schema gets parsed in follow-up commits as the field set
// is documented + tested against a live Hestia fixture.
package hestiacp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
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

// Discoverer is the Hestia-side implementation of migrate.Discoverer.
type Discoverer struct {
	// AllowPrivate — ADR-0095 decision 8. When true, SSRF
	// guard permits RFC1918 / ULA targets. Default false.
	AllowPrivate bool
	Port           int
	CommandTimeout time.Duration
}

func New() *Discoverer {
	return &Discoverer{
		Port:           22,
		CommandTimeout: 30 * time.Second,
	}
}

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
	client        *ssh.Client
	connectedUser string
}

func (s *session) Kind() string { return models.MigrationSourceHestia }

// Connect dials the source via SSH + validates by running a cheap
// admin probe (`v-list-users json`). Hestia's v-list-* scripts are
// admin-only; non-root SSH user gets permission-denied stderr.
func (d *Discoverer) Connect(ctx context.Context, host, user string, secret migrate.SecretRef) (migrate.Session, error) {
	port := d.Port
	if port == 0 {
		port = 22
	}
	auth, err := loadSecret(secret.Path)
	if err != nil {
		return nil, fmt.Errorf("hestiacp.Connect: load secret: %w", err)
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
		return nil, fmt.Errorf("hestiacp.Connect: tcp dial %s: %w", addr, err)
	}
	c, ch, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		if strings.Contains(err.Error(), "unable to authenticate") || strings.Contains(err.Error(), "no supported methods remain") {
			return nil, fmt.Errorf("hestiacp.Connect: source SSH server rejected the supplied auth method (likely PasswordAuthentication=no — upload an SSH PRIVATE KEY in the wizard's Connection step instead): %w", err)
		}
		return nil, fmt.Errorf("hestiacp.Connect: ssh handshake: %w", err)
	}
	client := ssh.NewClient(c, ch, reqs)
	s := &session{client: client, connectedUser: user}

	subctx, cancel := context.WithTimeout(ctx, d.CommandTimeout)
	defer cancel()
	if _, err := s.run(subctx, d.CommandTimeout, "v-list-users json"); err != nil {
		client.Close()
		return nil, fmt.Errorf("hestiacp.Connect: admin probe failed (need root SSH user): %w", err)
	}
	return s, nil
}

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

// hestiaUser is the v-list-users JSON shape:
//
//   { "<username>": { "FNAME": "...", "EMAIL": "...", "U_DISK": "1.5",
//                     "U_BANDWIDTH": "...", "PACKAGE": "default",
//                     "SUSPENDED": "no", ... } }
//
// One top-level key per user, value = string-keyed map of attrs.
type hestiaUser struct {
	FNAME      string `json:"FNAME"`
	LNAME      string `json:"LNAME"`
	EMAIL      string `json:"EMAIL"`
	UDisk      string `json:"U_DISK"`
	UBandwidth string `json:"U_BANDWIDTH"`
	Package    string `json:"PACKAGE"`
	Suspended  string `json:"SUSPENDED"`
}

// ListAccounts returns one row per Hestia user. v-list-users emits
// a JSON object keyed by username; we walk the keys and build
// AccountSummary rows with a best-effort byte conversion of
// U_DISK ('1.5' → MB). For exact byte counts the operator
// runs DescribeAccount which calls v-list-user <user> json.
func (d *Discoverer) ListAccounts(ctx context.Context, raw migrate.Session) ([]migrate.AccountSummary, error) {
	s, ok := raw.(*session)
	if !ok {
		return nil, errors.New("ListAccounts: wrong session type")
	}
	out, err := s.run(ctx, d.CommandTimeout, "v-list-users json")
	if err != nil {
		return nil, fmt.Errorf("v-list-users: %w", err)
	}
	var users map[string]hestiaUser
	if err := json.Unmarshal(out, &users); err != nil {
		return nil, fmt.Errorf("v-list-users decode: %w", err)
	}
	rows := make([]migrate.AccountSummary, 0, len(users))
	for login, u := range users {
		// Skip the special 'admin' login — that's Hestia's root
		// principal, not a transferable hosting user.
		if login == "admin" {
			continue
		}
		rows = append(rows, migrate.AccountSummary{
			ID:         login,
			Login:      login,
			Email:      u.EMAIL,
			BytesTotal: parseHestiaMB(u.UDisk),
			Suspended:  strings.EqualFold(u.Suspended, "yes"),
		})
	}
	return rows, nil
}

// parseHestiaMB converts Hestia's U_DISK string ('1.5', '2048') to
// bytes assuming the value is megabytes. Hestia's docs are
// inconsistent on the unit; v1 we trust MB.
func parseHestiaMB(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(f * 1024 * 1024)
}

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

// DescribeAccount returns a manifest scaffold for one Hestia account.
// Per-area builders (DomainSpec via v-list-user-domains json,
// DatabaseSpec via v-list-databases json, MailboxSpec via
// v-list-mail-domains-with-info, etc.) ship as follow-up commits
// once a live Hestia fixture is wired into the test corpus.
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
	// Cheap user.show probe so an unknown account fails up-front.
	if _, err := s.run(ctx, d.CommandTimeout, fmt.Sprintf("v-list-user '%s' json", strings.ReplaceAll(accountID, "'", `'\''`))); err != nil {
		return nil, fmt.Errorf("v-list-user %q: %w", accountID, err)
	}
	m := &migrate.AccountManifest{
		SchemaVersion: migrate.ManifestSchemaVersion,
		Source: migrate.SourceRef{
			Kind: models.MigrationSourceHestia,
			Host: host,
			User: accountID,
		},
		Warnings: []migrate.Warning{},
	}

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

	if boxes, warn, err := d.describeMailboxes(ctx, s, accountID); err != nil {
		m.Warnings = append(m.Warnings, migrate.Warning{
			Code: "mailboxes_failed", Detail: err.Error(),
		})
		_ = warn
	} else {
		m.Mailboxes = boxes
		m.Warnings = append(m.Warnings, warn...)
	}

	// Cron + SSH keys via tarball — same shape as DA. v-backup-user
	// tarball has user/cron + user/ssh.keys; cpanel restore writers
	// walk those when DA/Hestia tarball parser ships.
	m.Warnings = append(m.Warnings, migrate.Warning{
		Code:   "hestiacp_cron_ssh_via_tarball",
		Detail: "Hestia v-list-* doesn't expose cron + ssh keys at the user level; pull v-backup-user tarball + reuse cpanel writers (Step 5 follow-up).",
	})

	if firstErr != nil && len(m.Domains) == 0 {
		return nil, firstErr
	}
	return m, nil
}
