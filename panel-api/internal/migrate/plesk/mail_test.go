package plesk

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
)

func TestDescribeMailboxes_EnumeratesAndSizes(t *testing.T) {
	d := New()
	s := fixtureSession(map[string]string{
		"ls -1 '/var/qmail/mailnames/example.com'": "info\nsales\n.skipme\n",
		// du per mailbox
		"du -sb '/var/qmail/mailnames/example.com/info/Maildir'":  "1048576\t/path\n",
		"du -sb '/var/qmail/mailnames/example.com/sales/Maildir'": "2097152\t/path\n",
	})
	domains := []migrate.DomainSpec{{Name: "example.com", DocRoot: "/var/www/vhosts/example.com/httpdocs"}}
	boxes, warns := d.describeMailboxes(context.Background(), s, domains)
	if len(boxes) != 2 {
		t.Fatalf("got %d boxes, want 2 (dotfile skipped): %+v", len(boxes), boxes)
	}
	if boxes[0].Address != "info@example.com" {
		t.Errorf("box0 address = %q, want info@example.com", boxes[0].Address)
	}
	if boxes[0].MaildirPath != "/var/qmail/mailnames/example.com/info/Maildir" {
		t.Errorf("box0 maildir = %q", boxes[0].MaildirPath)
	}
	if boxes[0].BytesUsed != 1048576 || boxes[1].BytesUsed != 2097152 {
		t.Errorf("sizes wrong: %d, %d", boxes[0].BytesUsed, boxes[1].BytesUsed)
	}
	if len(warns) != 0 {
		t.Errorf("clean run should have no warnings: %+v", warns)
	}
}

func TestDescribeMailboxes_NoMailForDomainIsQuiet(t *testing.T) {
	d := New()
	// ls returns empty (no mailnames dir) → no boxes, no warning (ls
	// with 2>/dev/null + empty output is the "no mail" case).
	s := fixtureSession(map[string]string{})
	domains := []migrate.DomainSpec{{Name: "nomail.example.org"}}
	boxes, warns := d.describeMailboxes(context.Background(), s, domains)
	if len(boxes) != 0 {
		t.Errorf("expected no boxes, got %+v", boxes)
	}
	if len(warns) != 0 {
		t.Errorf("empty listing is not an error: %+v", warns)
	}
}

func TestParseFirstInt(t *testing.T) {
	cases := map[string]int64{
		"5242880\t/path": 5242880,
		"42":             42,
		"":               0,
		"not-a-number":   0,
		"  99  x":        99,
	}
	for in, want := range cases {
		if got := parseFirstInt(in); got != want {
			t.Errorf("parseFirstInt(%q) = %d, want %d", in, got, want)
		}
	}
}
