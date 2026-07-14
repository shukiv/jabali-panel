// Package imap is the source-side of the IMAP mail migration (GH #390
// Google Workspace / #374 M365). Phase A step 1: connect to a remote
// IMAP account and enumerate its folders + SPECIAL-USE roles, so the UI
// can preview what will be migrated and map remote folders to Stalwart
// mailboxes. Later steps FETCH the messages into a staging Maildir and
// hand them to the shared migration.import_mailboxes agent step.
//
// See plans/gh374-imap-mail-migration.md.
package imap

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
)

// DefaultIMAPSPort is implicit-TLS IMAP (RFC 8314). DefaultIMAPPort is
// the cleartext/STARTTLS port.
const (
	DefaultIMAPSPort = 993
	DefaultIMAPPort  = 143
	dialTimeout      = 20 * time.Second
)

// ProbeConfig is one remote IMAP account's connection parameters.
type ProbeConfig struct {
	Host     string
	Port     int    // 0 → 993 (implicit TLS) or 143 (STARTTLS)
	Username string // full remote address, e.g. user@gmail.com
	Password string // app-password (GH #390/#374 phase A auth)
	STARTTLS bool   // false → implicit TLS on 993; true → STARTTLS on 143

	// AllowPrivate lets the SSRF guard reach RFC1918 / loopback targets.
	// Production callers leave this false; only tests / an operator
	// migrating from a LAN server set it.
	AllowPrivate bool
}

// Folder is one remote mailbox and its resolved role.
type Folder struct {
	// Name is the raw IMAP mailbox name (server hierarchy, e.g.
	// "[Gmail]/Sent Mail" or "INBOX/Archive").
	Name string `json:"name"`
	// Role is the normalised SPECIAL-USE role: inbox / sent / drafts /
	// junk / trash / archive / all, or "" for an ordinary folder.
	Role string `json:"role"`
	// Selectable is false for \Noselect / \NonExistent container nodes
	// that hold no messages (skip them when migrating).
	Selectable bool `json:"selectable"`
}

// ProbeResult is the enumerated remote account.
type ProbeResult struct {
	Folders []Folder `json:"folders"`
}

// ErrAuth is returned when the remote server rejects the credentials —
// distinguished from connect errors so the UI can tell the operator to
// check the app-password vs the host/port. The underlying server text is
// wrapped for logs but callers surfacing to clients should show a fixed
// message (don't leak remote banners).
var ErrAuth = errors.New("imap: authentication failed")

// Probe dials the remote account over the SSRF-guarded transport, logs
// in, lists folders, and returns the enumeration. It is the network
// entrypoint; the login + list + mapping logic lives in probeWithClient
// so it can be unit-tested against an in-memory server.
func Probe(ctx context.Context, cfg ProbeConfig) (*ProbeResult, error) {
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		return nil, errors.New("imap: host required")
	}
	if cfg.Username == "" || cfg.Password == "" {
		return nil, errors.New("imap: username and password required")
	}

	port := cfg.Port
	if port == 0 {
		if cfg.STARTTLS {
			port = DefaultIMAPPort
		} else {
			port = DefaultIMAPSPort
		}
	}

	// SSRF-guarded TCP dial (resolve + range check + rebind guard) —
	// every migrate/* outbound goes through this.
	conn, err := migrate.DialTCP(ctx, host, port, cfg.AllowPrivate, dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("imap: connect %s:%d: %w", host, port, err)
	}

	var client *imapclient.Client
	if cfg.STARTTLS {
		client, err = imapclient.NewStartTLS(conn, &imapclient.Options{
			TLSConfig: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12},
		})
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("imap: starttls %s: %w", host, err)
		}
	} else {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		if herr := tlsConn.HandshakeContext(ctx); herr != nil {
			conn.Close()
			return nil, fmt.Errorf("imap: tls handshake %s: %w", host, herr)
		}
		client = imapclient.New(tlsConn, nil)
	}
	defer client.Close()

	return probeWithClient(client, cfg.Username, cfg.Password)
}

// probeWithClient runs LOGIN + LIST against an already-connected client
// and maps the result. Split out so tests drive it with an in-memory
// server (no TLS / no real network).
func probeWithClient(c *imapclient.Client, username, password string) (*ProbeResult, error) {
	if err := c.Login(username, password).Wait(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAuth, err)
	}
	// Closure, not `defer c.Logout().Wait()` — the latter evaluates the
	// c.Logout() receiver immediately, sending LOGOUT before the LIST below.
	defer func() { _ = c.Logout().Wait() }()

	// RETURN (SPECIAL-USE) so the server tags Sent/Drafts/Junk/Trash/
	// Archive/All folders (RFC 6154) — both Gmail and M365 advertise
	// SPECIAL-USE, and it's how roleOf classifies folders. A non-nil
	// options value is also required by RFC 5258 LIST-EXTENDED.
	entries, err := c.List("", "*", &imap.ListOptions{ReturnSpecialUse: true}).Collect()
	if err != nil {
		return nil, fmt.Errorf("imap: list folders: %w", err)
	}

	folders := make([]Folder, 0, len(entries))
	for _, e := range entries {
		if e == nil {
			continue
		}
		folders = append(folders, Folder{
			Name:       e.Mailbox,
			Role:       roleOf(e.Mailbox, e.Attrs),
			Selectable: selectable(e.Attrs),
		})
	}
	return &ProbeResult{Folders: folders}, nil
}

// roleOf normalises a mailbox to a SPECIAL-USE role. INBOX has no
// SPECIAL-USE attribute (RFC 6154) so it is matched by name.
func roleOf(name string, attrs []imap.MailboxAttr) string {
	if strings.EqualFold(name, "INBOX") {
		return "inbox"
	}
	for _, a := range attrs {
		switch a {
		case imap.MailboxAttrSent:
			return "sent"
		case imap.MailboxAttrDrafts:
			return "drafts"
		case imap.MailboxAttrJunk:
			return "junk"
		case imap.MailboxAttrTrash:
			return "trash"
		case imap.MailboxAttrArchive:
			return "archive"
		case imap.MailboxAttrAll:
			return "all"
		}
	}
	return ""
}

// selectable reports whether the folder holds messages — \Noselect and
// \NonExistent nodes are hierarchy containers only.
func selectable(attrs []imap.MailboxAttr) bool {
	for _, a := range attrs {
		if a == imap.MailboxAttrNoSelect || a == imap.MailboxAttrNonExistent {
			return false
		}
	}
	return true
}
