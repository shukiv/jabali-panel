package imap

import (
	"errors"
	"net"
	"sort"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

func TestRoleOf(t *testing.T) {
	cases := []struct {
		name  string
		mbox  string
		attrs []imap.MailboxAttr
		want  string
	}{
		{"inbox by name", "INBOX", nil, "inbox"},
		{"inbox case-insensitive", "inbox", nil, "inbox"},
		{"sent special-use", "[Gmail]/Sent Mail", []imap.MailboxAttr{imap.MailboxAttrSent}, "sent"},
		{"drafts", "Drafts", []imap.MailboxAttr{imap.MailboxAttrDrafts}, "drafts"},
		{"junk", "Spam", []imap.MailboxAttr{imap.MailboxAttrJunk}, "junk"},
		{"trash", "Bin", []imap.MailboxAttr{imap.MailboxAttrTrash}, "trash"},
		{"archive", "All Mail", []imap.MailboxAttr{imap.MailboxAttrArchive}, "archive"},
		{"all", "[Gmail]/All Mail", []imap.MailboxAttr{imap.MailboxAttrAll}, "all"},
		{"ordinary folder", "Projects/2026", nil, ""},
		{"unrelated attr", "Projects", []imap.MailboxAttr{imap.MailboxAttrHasChildren}, ""},
		// INBOX wins over a stray special-use attr on the same node.
		{"inbox precedence", "INBOX", []imap.MailboxAttr{imap.MailboxAttrSent}, "inbox"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := roleOf(tc.mbox, tc.attrs); got != tc.want {
				t.Errorf("roleOf(%q, %v) = %q, want %q", tc.mbox, tc.attrs, got, tc.want)
			}
		})
	}
}

func TestSelectable(t *testing.T) {
	cases := []struct {
		name  string
		attrs []imap.MailboxAttr
		want  bool
	}{
		{"plain folder", nil, true},
		{"has children only", []imap.MailboxAttr{imap.MailboxAttrHasChildren}, true},
		{"noselect container", []imap.MailboxAttr{imap.MailboxAttrNoSelect}, false},
		{"nonexistent", []imap.MailboxAttr{imap.MailboxAttrNonExistent}, false},
		{"noselect among others", []imap.MailboxAttr{imap.MailboxAttrHasChildren, imap.MailboxAttrNoSelect}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := selectable(tc.attrs); got != tc.want {
				t.Errorf("selectable(%v) = %v, want %v", tc.attrs, got, tc.want)
			}
		})
	}
}

// startMemServer spins up an in-memory IMAP server on a loopback
// listener with one user + the given mailboxes, and returns its address
// plus a cleanup func.
func startMemServer(t *testing.T, username, password string, mailboxes []string) string {
	t.Helper()

	mem := imapmemserver.New()
	user := imapmemserver.NewUser(username, password)
	for _, mb := range mailboxes {
		if err := user.Create(mb, nil); err != nil {
			t.Fatalf("create mailbox %q: %v", mb, err)
		}
	}
	mem.AddUser(user)

	srv := imapserver.New(&imapserver.Options{
		NewSession: func(_ *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		// Advertise IMAP4rev2 so LIST-EXTENDED (RETURN SPECIAL-USE) is
		// handled — the probe issues an extended LIST, which an
		// IMAP4rev1-only server can't parse. Gmail/M365 advertise
		// SPECIAL-USE + LIST-EXTENDED in production.
		Caps: imap.CapSet{imap.CapIMAP4rev2: {}},
		// The test transport is plaintext loopback; allow LOGIN without TLS.
		InsecureAuth: true,
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = ln.Close()
	})
	return ln.Addr().String()
}

func TestProbeWithClient(t *testing.T) {
	const user, pass = "user@example.com", "app-password"
	addr := startMemServer(t, user, pass, []string{"INBOX", "Sent", "Drafts", "Projects"})

	c, err := imapclient.DialInsecure(addr, nil)
	if err != nil {
		t.Fatalf("dial memserver: %v", err)
	}
	defer c.Close()

	res, err := probeWithClient(c, user, pass)
	if err != nil {
		t.Fatalf("probeWithClient: %v", err)
	}

	got := make([]string, 0, len(res.Folders))
	var inboxRole string
	for _, f := range res.Folders {
		got = append(got, f.Name)
		if f.Name == "INBOX" {
			inboxRole = f.Role
		}
		if !f.Selectable {
			t.Errorf("folder %q unexpectedly not selectable", f.Name)
		}
	}
	sort.Strings(got)
	want := []string{"Drafts", "INBOX", "Projects", "Sent"}
	if len(got) != len(want) {
		t.Fatalf("folders = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("folders = %v, want %v", got, want)
		}
	}
	if inboxRole != "inbox" {
		t.Errorf("INBOX role = %q, want inbox", inboxRole)
	}
}

func TestProbeWithClientBadPassword(t *testing.T) {
	const user, pass = "user@example.com", "app-password"
	addr := startMemServer(t, user, pass, []string{"INBOX"})

	c, err := imapclient.DialInsecure(addr, nil)
	if err != nil {
		t.Fatalf("dial memserver: %v", err)
	}
	defer c.Close()

	_, err = probeWithClient(c, user, "wrong-password")
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("probeWithClient with bad password: got %v, want ErrAuth", err)
	}
}
