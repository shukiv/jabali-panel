package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// The agent reports rsync exit 23/24 as success with partial=true. The panel
// must turn that into a summary line, or a partial copy reads as complete.
func TestRsyncPartialNote(t *testing.T) {
	vanished := json.RawMessage(`{"bytes_copied":934,"files":12,"partial":true,"exit_code":24,
		"errors":["file has vanished: \"/home/siteacct/mail/new/1.M1\""]}`)
	got := rsyncPartialNote("mail_rsync: /home/siteacct/mail", vanished)
	for _, want := range []string{"mail_rsync: /home/siteacct/mail: partial copy", "exit 24", "vanished", "a re-run picks them up", "/home/siteacct/mail/new/1.M1"} {
		if !strings.Contains(got, want) {
			t.Errorf("note = %q, want it to contain %q", got, want)
		}
	}

	unreadable := json.RawMessage(`{"partial":true,"exit_code":23,"errors":["rsync: a","rsync: b"]}`)
	got = rsyncPartialNote("home_rsync_remote: example.com", unreadable)
	if !strings.Contains(got, "exit 23") || !strings.Contains(got, "could not be read") || !strings.Contains(got, "rsync: a; rsync: b") {
		t.Errorf("note = %q, want exit 23, the reason and both rsync lines", got)
	}

	for name, raw := range map[string]json.RawMessage{
		"full copy":     json.RawMessage(`{"bytes_copied":1,"files":1}`),
		"old agent":     json.RawMessage(`{"bytes_copied":1,"files":1,"dest_path":"/home/x"}`),
		"not json":      json.RawMessage(`nope`),
		"empty":         nil,
		"partial false": json.RawMessage(`{"partial":false,"exit_code":24}`),
	} {
		if got := rsyncPartialNote("x", raw); got != "" {
			t.Errorf("%s: note = %q, want none", name, got)
		}
	}
}
