package imap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// appendMsg APPENDs one RFC822 message to a mailbox on the test server.
func appendMsg(t *testing.T, addr, user, pass, mailbox, body string, seen bool) {
	t.Helper()
	c, err := imapclient.DialInsecure(addr, nil)
	if err != nil {
		t.Fatalf("dial for append: %v", err)
	}
	defer c.Close()
	if err := c.Login(user, pass).Wait(); err != nil {
		t.Fatalf("login for append: %v", err)
	}
	defer func() { _ = c.Logout().Wait() }()

	var opts *imap.AppendOptions
	if seen {
		opts = &imap.AppendOptions{Flags: []imap.Flag{imap.FlagSeen}}
	}
	cmd := c.Append(mailbox, int64(len(body)), opts)
	if _, err := cmd.Write([]byte(body)); err != nil {
		t.Fatalf("append write: %v", err)
	}
	if err := cmd.Close(); err != nil {
		t.Fatalf("append close: %v", err)
	}
	if _, err := cmd.Wait(); err != nil {
		t.Fatalf("append %q: %v", mailbox, err)
	}
}

func msg(id, subject, bodyText string) string {
	return strings.Join([]string{
		"Message-ID: <" + id + "@example.com>",
		"Subject: " + subject,
		"From: sender@example.com",
		"To: user@example.com",
		"",
		bodyText,
		"",
	}, "\r\n")
}

func countFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("readdir %s: %v", dir, err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			n++
		}
	}
	return n
}

func TestFetchWithClient(t *testing.T) {
	const user, pass = "user@example.com", "app-password"
	addr := startMemServer(t, user, pass, []string{"INBOX", "Sent", "Projects"})

	// INBOX: one read (Seen → cur/), one unread (→ new/). Sent: one read.
	appendMsg(t, addr, user, pass, "INBOX", msg("1", "read", "already read"), true)
	appendMsg(t, addr, user, pass, "INBOX", msg("2", "unread", "still unread"), false)
	appendMsg(t, addr, user, pass, "Sent", msg("3", "sent", "i sent this"), true)

	staging := t.TempDir()
	c, err := imapclient.DialInsecure(addr, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	res, err := fetchWithClient(context.Background(), c, user, pass, nil, staging)
	if err != nil {
		t.Fatalf("fetchWithClient: %v", err)
	}

	if res.TotalMessages != 3 {
		t.Errorf("TotalMessages = %d, want 3", res.TotalMessages)
	}

	// INBOX → Maildir root: 1 in cur/, 1 in new/.
	if got := countFiles(t, filepath.Join(staging, "cur")); got != 1 {
		t.Errorf("INBOX cur/ has %d files, want 1 (the \\Seen message)", got)
	}
	if got := countFiles(t, filepath.Join(staging, "new")); got != 1 {
		t.Errorf("INBOX new/ has %d files, want 1 (the unseen message)", got)
	}

	// Sent → Maildir++ .Sent subfolder: 1 in cur/ (it was \Seen).
	if got := countFiles(t, filepath.Join(staging, ".Sent", "cur")); got != 1 {
		t.Errorf(".Sent/cur has %d files, want 1", got)
	}

	// The \Seen message's filename carries the :2,S info suffix.
	curEntries, _ := os.ReadDir(filepath.Join(staging, "cur"))
	if len(curEntries) == 1 && !strings.HasSuffix(curEntries[0].Name(), ":2,S") {
		t.Errorf("cur/ file %q missing :2,S Seen suffix", curEntries[0].Name())
	}

	// Projects is empty — it should have been selected (dirs created) but
	// contributed no messages.
	if got := countFiles(t, filepath.Join(staging, ".Projects", "new")); got != 0 {
		t.Errorf(".Projects/new has %d files, want 0", got)
	}
}

func TestMaildirSub(t *testing.T) {
	cases := []struct {
		name     string
		folder   Folder
		wantSub  string
		wantSkip bool
	}{
		{"inbox root", Folder{Name: "INBOX", Role: "inbox"}, "", false},
		{"sent role", Folder{Name: "[Gmail]/Sent Mail", Role: "sent"}, "Sent", false},
		{"drafts role", Folder{Name: "Drafts", Role: "drafts"}, "Drafts", false},
		{"all-mail skipped", Folder{Name: "[Gmail]/All Mail", Role: "all"}, "", true},
		{"ordinary flat", Folder{Name: "Receipts", Role: ""}, "Receipts", false},
		{"ordinary nested slash", Folder{Name: "Work/2026", Role: "", Delim: '/'}, "Work.2026", false},
		{"ordinary nested dot", Folder{Name: "Work.2026", Role: "", Delim: '.'}, "Work.2026", false},
		{"empty normalises to skip", Folder{Name: "/", Role: "", Delim: '/'}, "", true},
		// Untrusted remote names must not escape the staging dir. Separators
		// are neutralised to dots; residual ".." → skip.
		{"traversal slash delim", Folder{Name: "../../etc/passwd", Role: "", Delim: '/'}, "etc.passwd", false},
		{"traversal dot delim", Folder{Name: "../../secret", Role: "", Delim: '.'}, "secret", false},
		{"traversal backslash", Folder{Name: "..\\..\\x", Role: "", Delim: 0}, "x", false},
		{"nul stripped", Folder{Name: "a\x00b", Role: "", Delim: 0}, "ab", false},
		{"double-dot only skips", Folder{Name: "..", Role: "", Delim: 0}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub, skip := maildirSub(tc.folder)
			if sub != tc.wantSub || skip != tc.wantSkip {
				t.Errorf("maildirSub(%+v) = (%q, %v), want (%q, %v)", tc.folder, sub, skip, tc.wantSub, tc.wantSkip)
			}
		})
	}
}
